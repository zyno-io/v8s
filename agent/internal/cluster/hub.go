package cluster

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyno-io/v8s/agent/internal/disco"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/cache"
)

// Watch lifecycle defaults (docs/performance.md §4, §5).
const (
	DefaultLinger         = 5 * time.Minute
	DefaultBudget         = 96
	DefaultDiscoveryEvery = 5 * time.Minute
	// DefaultDiscoveryFallbackEvery is the discovery period while the CRD invalidation watch is unusable.
	DefaultDiscoveryFallbackEvery = 60 * time.Second
	// DefaultDiscoveryFollowUp is the extra refresh after CRD/APIService activity: the API server publishes a new
	// CRD in discovery slightly after the CRD object exists.
	DefaultDiscoveryFollowUp = 5 * time.Second
	// DefaultForbiddenRetry is how long an unreferenced (baseline) watch whose LIST was refused (Forbidden or
	// NotFound) stays parked before it is tried again.
	DefaultForbiddenRetry = 5 * time.Minute
)

// DefaultEventsLinger is how long an object-events watch stays warm after its last reference (these are
// per-object etcd watches, so the linger is short).
const DefaultEventsLinger = 30 * time.Second

// ErrBudget is returned when a cluster is at its watch budget and nothing lingering can be evicted.
var ErrBudget = errors.New("watch budget exhausted")

const crdGVR = "apiextensions.k8s.io/v1/customresourcedefinitions"

// invalidationKinds are watched (Table mode, metadata only) to refresh discovery when they change.
var invalidationKinds = []string{
	crdGVR,
	"apiregistration.k8s.io/v1/apiservices",
}

type watchEntry struct {
	gvr       string
	st        *store.Store
	cancel    context.CancelFunc
	done      chan struct{} // closed when the current reflector run ends (replaced by each restart, guarded by r.mu)
	invSynced atomic.Bool   // an invalidation watch finished its first list
	refs      int
	bgRefs    int          // of refs, those held by background scopes: they do not keep a failing watch retrying (parkIfUnheld)
	errv      atomic.Value // listErr: the latest list/watch error, cleared by the next success
	tier0     bool
	lastUse   time.Time
	linger    *time.Timer
	gen       uint64 // bumped on every acquire and linger arm: a timer that fired for an older gen is stale
	// part is set for an object-events watch (docs/protocol.md "Events of one object"): its rows live in the
	// runner's shared oev store, st stays nil, and synced closes after the first list.
	part   *store.Part
	synced chan struct{}

	// parked: the reflector is stopped after a terminal LIST error while nothing held the watch (guarded by
	// r.mu). The entry stays registered (attached, reported unavailable) and is restarted by retry, or by acquire.
	parked bool
	retry  *time.Timer
	cur    atomic.Pointer[context.Context] // the context of the current reflector run
	lw     *ingest.ListerWatcher
}

type listErr struct{ err error }

func (w *watchEntry) setErr(err error) { w.errv.Store(listErr{err}) }
func (w *watchEntry) lastErr() error {
	v, _ := w.errv.Load().(listErr)
	return v.err
}

type discState struct {
	list []disco.Resource
	byG  map[string]*disco.Resource
}

// runner is one selected cluster: its phase, its watches and its discovery cache.
type runner struct {
	m   *Manager
	id  string
	be  Backend
	ctx context.Context

	cancel context.CancelFunc
	done   chan struct{} // closed when every goroutine of the runner has ended

	phase   atomic.Value // protocol.ClusterPhase
	synced  atomic.Int64
	message atomic.Value // string
	everOK  atomic.Bool
	vpaPods atomic.Bool // some pod's requests were set by a VPA (protocol.ClusterState.VPA)

	mu        sync.Mutex
	watches   map[string]*watchEntry
	attached  bool
	stopTimer *time.Timer // pending grace-period stop
	wg        sync.WaitGroup

	disc     atomic.Pointer[discState]
	discKick chan struct{}
	discGen  atomic.Uint64 // bumped when the discovered resource set changes (protocol.ClusterState.Kinds)

	pe     *problems.Engine // the cluster's problems engine (nil: problems are not enabled)
	gcOnce sync.Once

	// oev is the one store all of the cluster's object-events watches write into (created on first use,
	// guarded by mu); it is attached to the view engine once as ingest.ObjectEventsGVR.
	oev *store.Store

	usage *usageState // the metrics poller (nil for a failed runner)
	// capStore holds the cluster's one capacity row (the Overview CPU/Memory card), computed by usageState.stepCapacity.
	// Attached like the problems store; nil for a failed runner.
	capStore *store.Store
}

func (r *runner) state() protocol.ClusterState {
	msg, _ := r.message.Load().(string)
	ma, mm := r.usage.avail()
	return protocol.ClusterState{ID: r.id, Phase: r.phase.Load().(protocol.ClusterPhase), Synced: int(r.synced.Load()), Message: msg,
		Metrics: ma, MetricsMessage: mm, VPA: r.vpaPods.Load(), Kinds: r.discGen.Load()}
}

func (r *runner) setPhase(p protocol.ClusterPhase, msg string) {
	if r.phase.Load() != p || r.message.Load() != msg {
		r.phase.Store(p)
		r.message.Store(msg)
		if r.pe != nil {
			r.pe.SetPhase(p, msg)
			if p == protocol.PhaseReady {
				r.gcOnce.Do(func() { r.scheduleAckGC(30 * time.Second) })
			}
		}
		r.m.kick()
	}
}

// scheduleAckGC drops long-unseen acknowledgements of the cluster after d, then every few hours.
func (r *runner) scheduleAckGC(d time.Duration) {
	time.AfterFunc(d, func() {
		if r.ctx.Err() != nil || r.m.problems == nil {
			return
		}
		r.m.problems.GCAcks(r.id)
		r.scheduleAckGC(6 * time.Hour)
	})
}

// ---- discovery ---------------------------------------------------------------------------

// discSig is a cheap FNV-1a signature of the discovered resource set (list order is already sorted).
func discSig(res []disco.Resource) uint64 {
	h := uint64(14695981039346656037)
	add := func(s string) {
		for i := 0; i < len(s); i++ {
			h = (h ^ uint64(s[i])) * 1099511628211
		}
		h = (h ^ 0xff) * 1099511628211
	}
	for i := range res {
		x := &res[i]
		if x.Placeholder() {
			continue
		}
		add(x.GVR())
		add(x.Kind)
		var b byte
		if x.Namespaced {
			b |= 1
		}
		if x.Listable() {
			b |= 2
		}
		if x.Stale {
			b |= 4
		}
		h = (h ^ uint64(b)) * 1099511628211
	}
	return h
}

// discoveryPeriod is the periodic refresh interval. Without a usable CRD invalidation watch (a read context that
// cannot list/watch CRDs, as namespace-scoped ones often cannot) nothing signals new CRDs, so poll faster;
// discovery is only two aggregated GETs.
func (r *runner) discoveryPeriod() time.Duration {
	r.mu.Lock()
	w := r.watches[crdGVR]
	usable := w != nil && !w.parked && w.invSynced.Load() && w.lastErr() == nil
	r.mu.Unlock()
	if usable || r.m.DiscoveryFallbackEvery <= 0 {
		return r.m.DiscoveryEvery
	}
	return min(r.m.DiscoveryEvery, r.m.DiscoveryFallbackEvery)
}

func (r *runner) discoverLoop() {
	defer r.wg.Done()
	var sig uint64
	var haveSig, followUp bool
	for {
		wait := r.discoveryPeriod()
		res, err := r.be.Discover(r.ctx)
		if r.ctx.Err() != nil {
			return
		}
		if err != nil {
			r.m.log.Debug("discovery failed", "cluster", r.id, "err", err)
			wait = min(wait, 30*time.Second)
		} else {
			d := &discState{list: res, byG: make(map[string]*disco.Resource, len(res))}
			for i := range d.list {
				if !d.list[i].Placeholder() { // a group-version listed but not answering has no resource
					d.byG[d.list[i].GVR()] = &d.list[i]
				}
			}
			r.disc.Store(d)
			if ns := discSig(res); !haveSig || ns != sig {
				sig, haveSig = ns, true
				r.discGen.Add(1)
			}
			if r.pe != nil {
				grs := make(map[string]bool, len(res))
				for i := range res {
					if !res[i].Placeholder() {
						grs[problems.GR(res[i].GVR())] = true
					}
				}
				r.pe.SetDiscovery(grs)
			}
			r.m.kick()
			r.m.reconcileAll()
			r.usage.kickNow()
		}
		if followUp {
			wait = min(wait, r.m.DiscoveryFollowUp)
			followUp = false
		}
		t := time.NewTimer(wait)
		select {
		case <-r.ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-r.discKick:
			t.Stop()
			followUp = true // discovery can lag the CRD object: look once more shortly
			select {        // debounce bursts of CRD changes
			case <-time.After(time.Second):
			case <-r.ctx.Done():
				return
			}
		}
	}
}

// invalidate asks the discovery loop to refresh soon (CRD or APIService changed).
func (r *runner) invalidate() {
	select {
	case r.discKick <- struct{}{}:
	default:
	}
}

// serves reports whether the cluster serves gvr: true when discovery has not loaded yet and the
// kind is curated (those watch from the start), false for unknown generic kinds until discovery lands.
func (r *runner) serves(gvr string) (known, ok bool) {
	d := r.disc.Load()
	if d == nil {
		m := ingest.Lookup(gvr)
		return false, m != nil && (m.Virtual || (m.Curated && !m.Optional))
	}
	if m := ingest.Lookup(gvr); m != nil && m.Virtual {
		return true, true
	}
	_, ok = d.byG[gvr]
	return true, ok
}

// ---- watches ----------------------------------------------------------------------------

func (r *runner) module(gvr string) (*ingest.Module, *ingest.GenericSpec, error) {
	if m := ingest.Lookup(gvr); m != nil {
		return m, nil, nil
	}
	d := r.disc.Load()
	if d == nil {
		return nil, nil, errors.New("discovery not ready")
	}
	res := d.byG[gvr]
	if res == nil {
		return nil, nil, apierrors.NewNotFound(schemaGR(gvr), gvr)
	}
	if m := ingest.ResolveDiscovered(res.Group, res.Version, res.Resource, res.Kind, res.Namespaced); m != nil {
		return m, nil, nil
	}
	return nil, &ingest.GenericSpec{GVR: gvr, Kind: res.Kind, Namespaced: res.Namespaced, Short: res.ShortNames}, nil
}

// startLocked starts the watch for gvr. Caller holds r.mu.
func (r *runner) startLocked(gvr string, tier0 bool) (*watchEntry, error) {
	mod, spec, err := r.module(gvr)
	if err != nil {
		return nil, err
	}
	api, err := r.be.API(gvr, mod)
	if err != nil {
		return nil, err
	}
	w := &watchEntry{gvr: gvr, st: store.New(), done: make(chan struct{}), tier0: tier0, lastUse: time.Now()}
	hooks := ingest.Hooks{
		OnRows: w.st.Provisional,
		OnOK: func() {
			w.setErr(nil)
			if r.pe != nil {
				r.pe.SetUnavailable(problems.GR(gvr), "")
			}
		},
		OnWatchListTruncated: func() {
			r.m.log.Warn("watch-list stream truncated; using LIST+watch", "cluster", r.id, "gvr", gvr)
		},
		OnError: func(err error) {
			if c := w.cur.Load(); c == nil || (*c).Err() != nil || apierrors.IsResourceExpired(err) || apierrors.IsGone(err) {
				return // a routine relist, not a cluster problem
			}
			w.setErr(err)
			if r.pe != nil && r.pe.Interested(gvr) {
				if reason := unavailableReason(gvr, err); reason != "" {
					r.pe.SetUnavailable(problems.GR(gvr), reason)
				}
			}
			if gvr != ingest.PodsGVR && terminalListErr(err) {
				r.parkIfUnheld(w)
			}
			primary := gvr == ingest.PodsGVR
			if !primary && !apierrors.IsUnauthorized(err) {
				r.m.log.Debug("watch error", "cluster", r.id, "gvr", gvr, "err", err.Error())
				return
			}
			p, msg := classify(err)
			if r.everOK.Load() && p == protocol.PhaseUnreachable {
				p = protocol.PhaseReconnecting
			} else if r.everOK.Load() && p != protocol.PhaseAuthRequired {
				p = protocol.PhaseDegraded
			}
			r.m.log.Warn("cluster error", "cluster", r.id, "phase", p, "err", msg, "detail", err.Error())
			r.setPhase(p, msg)
		},
	}
	if gvr == ingest.PodsGVR {
		hooks.OnSyncProgress = func(n int) {
			r.synced.Store(int64(n))
			if r.phase.Load() == protocol.PhaseConnecting {
				r.setPhase(protocol.PhaseSyncing, "")
			}
			r.m.kickThrottled()
		}
		hooks.OnOK = func() {
			w.setErr(nil)
			if r.pe != nil {
				r.pe.SetUnavailable(problems.GR(gvr), "")
			}
			if r.phase.Load() == protocol.PhaseConnecting {
				r.setPhase(protocol.PhaseSyncing, "")
			}
		}
		w.st.OnReplace = func(n int) {
			r.synced.Store(int64(n))
			r.everOK.Store(true)
			r.setPhase(protocol.PhaseReady, "")
		}
		w.st.OnVPA = func(bool) {
			// Read the store, not the argument: edge callbacks run outside its lock and may land out of order.
			r.vpaPods.Store(w.st.VPAAny())
			r.m.kick()
		}
		w.st.OnActivity = func() {
			if p := r.phase.Load(); p == protocol.PhaseReconnecting || p == protocol.PhaseDegraded {
				r.setPhase(protocol.PhaseReady, "")
			}
		}
	}
	for _, ik := range invalidationKinds {
		if gvr == ik {
			var synced atomic.Bool
			w.st.OnReplace = func(int) {
				w.invSynced.Store(true)
				if synced.Swap(true) { // a relist (after a 410 or a reconnect) may carry CRDs added meanwhile
					r.invalidate()
				}
			}
			w.st.OnActivity = func() {
				if synced.Load() {
					r.invalidate()
				}
			}
		}
	}
	w.lw = &ingest.ListerWatcher{API: api, Mod: mod, Generic: spec, Hooks: hooks}
	r.watches[gvr] = w
	if r.pe != nil {
		r.pe.Attach(gvr, w.st)
	}
	r.runLocked(w)
	if r.attached {
		r.m.eng.AttachKind(r.id, gvr, w.st)
	}
	return w, nil
}

// runLocked starts a reflector run for w (a new one after a park). Caller holds r.mu.
func (r *runner) runLocked(w *watchEntry) {
	wctx, cancel := context.WithCancel(r.ctx)
	w.cancel = cancel
	w.cur.Store(&wctx)
	w.parked = false
	w.invSynced.Store(false) // every run lists again (discoveryPeriod polls faster until it has)
	done := make(chan struct{})
	w.done = done
	refl := cache.NewReflectorWithOptions(w.lw, &ingest.Row{}, w.st, cache.ReflectorOptions{Name: w.gvr + "/" + r.id, TypeDescription: w.gvr})
	refl.WatchListPageSize = 500 // the fallback LIST (and relists) page instead of one huge response
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer close(done)
		refl.RunWithContext(wctx)
	}()
}

// terminalListErr: the API refused the list for this user (or does not serve the kind); retrying every few
// seconds cannot help. Unauthorized is not included: it is often transient (credentials refreshing) and
// cluster-wide, and the pods watch drives the auth-required phase.
func terminalListErr(err error) bool {
	return apierrors.IsForbidden(err) || apierrors.IsNotFound(err)
}

// parkIfUnheld stops the reflector of a watch nobody is looking at after a terminal LIST error, instead of
// letting client-go retry (and hit the API server through the gateway) every ~30 s for the whole session. The
// watch stays registered with its error, so it is still reported unavailable; it is retried after
// ForbiddenRetry, or at once when a view acquires it. A watch held by a view keeps retrying: the user is
// looking at it and expects it to recover as soon as access is granted.
func (r *runner) parkIfUnheld(w *watchEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.watches[w.gvr] != w || w.refs > w.bgRefs || w.parked || w.part != nil || r.ctx.Err() != nil {
		return
	}
	w.parked = true
	w.cancel()
	d := r.m.ForbiddenRetry
	if d <= 0 {
		d = DefaultForbiddenRetry
	}
	w.retry = time.AfterFunc(d, func() { r.unpark(w) })
}

func (r *runner) unpark(w *watchEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unparkLocked(w)
}

func (r *runner) unparkLocked(w *watchEntry) {
	if r.watches[w.gvr] != w || !w.parked || r.ctx.Err() != nil {
		return
	}
	if w.retry != nil {
		w.retry.Stop()
		w.retry = nil
	}
	w.errv.Store(listErr{}) // do not answer scopeFailure / IP search from the stale refusal before the new LIST lands
	r.runLocked(w)
}

// retryIfGoneLocked restarts a baseline watch parked on NotFound: discovery now serves the kind again (a CRD or
// APIService was reinstalled). Caller holds r.mu.
func (r *runner) retryIfGoneLocked(w *watchEntry) {
	if w.parked && apierrors.IsNotFound(w.lastErr()) {
		r.unparkLocked(w)
	}
}

// unavailableReason explains a watch error that makes a kind unreadable for the problems engine ("" for
// transient errors): no permission, or its (aggregated) API is down.
func unavailableReason(gvr string, err error) string {
	res := gvr[strings.LastIndexByte(gvr, '/')+1:]
	switch {
	case apierrors.IsForbidden(err):
		return "no permission to list " + res
	case apierrors.IsServiceUnavailable(err):
		return "the API serving " + res + " is unavailable"
	}
	return ""
}

// promoteLocked makes a watch that was started on demand part of the baseline: it no longer lingers out or
// gets evicted. Caller holds r.mu.
func (r *runner) promoteLocked(w *watchEntry) {
	w.tier0 = true
	if w.linger != nil {
		w.linger.Stop()
		w.linger = nil
	}
}

// makeRoomLocked evicts the least recently used lingering watch when the budget is reached.
func (r *runner) makeRoomLocked() error {
	if len(r.watches) < r.m.Budget {
		return nil
	}
	var victim *watchEntry
	for _, w := range r.watches {
		if w.refs == 0 && !w.tier0 && (victim == nil || w.lastUse.Before(victim.lastUse)) {
			victim = w
		}
	}
	if victim == nil {
		return ErrBudget
	}
	r.stopLocked(victim)
	return nil
}

// stopLocked stops a watch and frees its store in the background. Caller holds r.mu.
func (r *runner) stopLocked(w *watchEntry) {
	delete(r.watches, w.gvr)
	if w.linger != nil {
		w.linger.Stop()
		w.linger = nil
	}
	if w.retry != nil {
		w.retry.Stop()
		w.retry = nil
	}
	if w.part != nil {
		// An object-events watch: remove its rows now, under r.mu, so a restart of the same key cannot have its
		// rows deleted by this watch later (late reflector writes are no-ops after Close). It never touches
		// the engine attachment or the problems engine.
		w.cancel()
		w.part.Close()
		return
	}
	r.m.eng.DetachKind(r.id, w.gvr)
	if r.pe != nil {
		r.pe.Detach(w.gvr)
		r.pe.SetUnavailable(problems.GR(w.gvr), "")
	}
	w.cancel()
	done := w.done
	go func() {
		<-done
		w.st.Reset()
	}()
}

// acquire takes a reference on the watch for gvr, starting it when needed. release drops it; the last
// release starts the linger timer (the watch then stays warm for Linger, or until evicted).
func (r *runner) acquire(gvr string) (release func(), err error) {
	return r.acquireBg(gvr, false)
}

// acquireBg is acquire; bg takes a background reference (protocol.ViewQuery.Background), which neither unparks
// the watch nor keeps it retrying after a terminal LIST error.
func (r *runner) acquireBg(gvr string, bg bool) (release func(), err error) {
	return r.acquireKey(gvr, r.m.Linger, bg, func() (*watchEntry, error) {
		if err := r.makeRoomLocked(); err != nil {
			return nil, err
		}
		return r.startLocked(gvr, false)
	})
}

// acquireKey takes a reference on the watch stored under key, calling start (with r.mu held) when there is none.
// The last release starts a linger timer of linger.
func (r *runner) acquireKey(key string, linger time.Duration, bg bool, start func() (*watchEntry, error)) (release func(), err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	gvr := key
	w := r.watches[gvr]
	if w == nil {
		if w, err = start(); err != nil {
			return nil, err
		}
	}
	w.refs++
	w.gen++
	w.lastUse = time.Now()
	if bg {
		w.bgRefs++
	} else {
		r.unparkLocked(w) // a view wants it now: try again at once
	}
	if w.linger != nil {
		w.linger.Stop()
		w.linger = nil
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.watches[gvr] != w {
				return
			}
			w.refs--
			if bg {
				w.bgRefs--
			}
			w.lastUse = time.Now()
			if w.refs == 0 && !w.tier0 {
				w.gen++
				gen := w.gen
				w.linger = time.AfterFunc(linger, func() { r.expire(w, gen) })
			}
		})
	}, nil
}

func (r *runner) expire(w *watchEntry, gen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.watches[w.gvr] == w && w.refs == 0 && !w.tier0 && w.gen == gen {
		r.stopLocked(w)
	}
}

// startTier0 starts the baseline watches (pods first) and the discovery loop.
func (r *runner) startTier0() {
	r.mu.Lock()
	for _, m := range ingest.Tier0Modules() {
		if _, err := r.startLocked(m.GVR, true); err != nil {
			r.m.log.Warn("tier-0 watch failed", "cluster", r.id, "gvr", m.GVR, "err", err)
		}
	}
	r.mu.Unlock()
	r.wg.Add(1)
	go r.discoverLoop()
}

// startInvalidation starts the CRD/APIService metadata watches once discovery knows them.
func (r *runner) startInvalidation() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, gvr := range invalidationKinds {
		if r.watches[gvr] != nil {
			continue
		}
		if known, ok := r.serves(gvr); known && ok {
			if _, err := r.startLocked(gvr, true); err != nil {
				r.m.log.Debug("invalidation watch failed", "cluster", r.id, "gvr", gvr, "err", err)
			}
		}
	}
}

// startOptionalBaseline starts the always-on watches of CRD-backed kinds (Flux: docs/integrations.md
// "Performance") for the versions the cluster serves, once discovery knows them.
func (r *runner) startOptionalBaseline() {
	d := r.disc.Load()
	if d == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, res := range d.list {
		if !res.Listable() {
			continue
		}
		m := ingest.ResolveDiscovered(res.Group, res.Version, res.Resource, res.Kind, res.Namespaced)
		if m == nil || !m.Optional || !(m.Tier0 || r.gated(m)) {
			continue
		}
		if w := r.watches[m.GVR]; w != nil { // an on-demand watch (a user opened the kind) becomes baseline
			r.promoteLocked(w)
			r.retryIfGoneLocked(w)
			continue
		}
		if _, err := r.startLocked(m.GVR, true); err != nil {
			r.m.log.Debug("baseline watch failed", "cluster", r.id, "gvr", m.GVR, "err", err)
		}
	}
}

// gated reports whether a module is part of the baseline because the problem class it feeds is enabled.
func (r *runner) gated(m *ingest.Module) bool {
	return m.GateClass != "" && r.m.problems != nil && r.m.problems.ClassEnabled(r.id, m.GateClass)
}

// startGatedBaseline starts the watches of built-in kinds that only feed an optional problem class (HPAs for
// hpa.unable, PVs for pv.failed) once that class is enabled.
func (r *runner) startGatedBaseline() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range ingest.CuratedModules() {
		if m.Optional || m.Tier0 || m.GateClass == "" || !r.gated(m) {
			continue
		}
		if w := r.watches[m.GVR]; w != nil {
			r.promoteLocked(w)
			r.retryIfGoneLocked(w)
			continue
		}
		if _, err := r.startLocked(m.GVR, true); err != nil {
			r.m.log.Debug("gated baseline watch failed", "cluster", r.id, "gvr", m.GVR, "err", err)
		}
	}
}

func (r *runner) attachAll() {
	r.mu.Lock()
	r.attached = true
	if r.pe != nil {
		r.m.eng.AttachKind(r.id, problems.GVR, r.pe.Store())
	}
	if r.capStore != nil {
		r.m.eng.AttachKind(r.id, protocol.CapacityGVR, r.capStore)
	}
	for gvr, w := range r.watches {
		if w.part == nil {
			r.m.eng.AttachKind(r.id, gvr, w.st)
		}
	}
	if r.oev != nil {
		r.m.eng.AttachKind(r.id, ingest.ObjectEventsGVR, r.oev)
	}
	r.mu.Unlock()
	r.usage.republish() // detach cleared the engine's copy; the snapshot belongs to the same store (invariant 1 is checked by SetUsage)
}

func (r *runner) detachAll() {
	r.mu.Lock()
	r.attached = false
	r.mu.Unlock()
	r.m.eng.Detach(r.id)
}

// stop ends every watch and releases memory.
func (r *runner) stop() {
	r.cancel()
	r.wg.Wait()
	r.mu.Lock()
	for _, w := range r.watches {
		if w.linger != nil {
			w.linger.Stop()
		}
		if w.retry != nil {
			w.retry.Stop()
		}
		if w.part != nil {
			w.part.Close()
		} else {
			w.st.Reset()
		}
	}
	if r.oev != nil {
		r.oev.Reset()
	}
	if r.capStore != nil {
		r.capStore.Reset()
	}
	r.watches = map[string]*watchEntry{}
	r.mu.Unlock()
}

// ScanKind calls fn for every row of gvr in the cluster's store until fn returns false. It reports whether
// the kind is currently watched (false: nothing was scanned).
func (m *Manager) ScanKind(clusterID, gvr string, fn func(*ingest.Row) bool) bool {
	m.mu.Lock()
	r := m.runners[clusterID]
	m.mu.Unlock()
	if r == nil {
		return false
	}
	r.mu.Lock()
	w := r.watches[gvr]
	r.mu.Unlock()
	if w == nil {
		return false
	}
	w.st.Scan(fn)
	return true
}

// KindSynced reports whether gvr is watched in the cluster, not parked, and its store has completed its first
// list (what ScanKind returns is then the whole kind, not a partial or empty read).
func (m *Manager) KindSynced(clusterID, gvr string) bool {
	m.mu.Lock()
	r := m.runners[clusterID]
	m.mu.Unlock()
	if r == nil {
		return false
	}
	r.mu.Lock()
	w := r.watches[gvr]
	parked := w != nil && w.parked
	r.mu.Unlock()
	return w != nil && !parked && w.st != nil && w.st.Synced()
}

// WatchInfo describes one running watch (diagnostics and tests).
type WatchInfo struct {
	GVR       string
	Refs      int
	Tier0     bool
	Lingering bool
	Parked    bool // stopped after a refused LIST; retried after ForbiddenRetry or when a view acquires it
}

func (r *runner) info() []WatchInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]WatchInfo, 0, len(r.watches))
	for _, w := range r.watches {
		out = append(out, WatchInfo{GVR: w.gvr, Refs: w.refs, Tier0: w.tier0, Lingering: w.refs == 0 && !w.tier0, Parked: w.parked})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GVR < out[j].GVR })
	return out
}
