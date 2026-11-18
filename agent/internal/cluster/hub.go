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
)

// DefaultEventsLinger is how long an object-events watch stays warm after its last reference (these are
// per-object etcd watches, so the linger is short).
const DefaultEventsLinger = 30 * time.Second

// ErrBudget is returned when a cluster is at its watch budget and nothing lingering can be evicted.
var ErrBudget = errors.New("watch budget exhausted")

// invalidationKinds are watched (Table mode, metadata only) to refresh discovery when they change.
var invalidationKinds = []string{
	"apiextensions.k8s.io/v1/customresourcedefinitions",
	"apiregistration.k8s.io/v1/apiservices",
}

type watchEntry struct {
	gvr     string
	st      *store.Store
	cancel  context.CancelFunc
	done    chan struct{}
	refs    int
	errv    atomic.Value // listErr: the latest list/watch error, cleared by the next success
	tier0   bool
	lastUse time.Time
	linger  *time.Timer
	gen     uint64 // bumped on every acquire and linger arm: a timer that fired for an older gen is stale
	// part is set for an object-events watch (docs/protocol.md "Events of one object"): its rows live in the
	// runner's shared oev store, st stays nil, and synced closes after the first list.
	part   *store.Part
	synced chan struct{}
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

	mu        sync.Mutex
	watches   map[string]*watchEntry
	attached  bool
	stopTimer *time.Timer // pending grace-period stop
	wg        sync.WaitGroup

	disc     atomic.Pointer[discState]
	discKick chan struct{}

	pe     *problems.Engine // the cluster's problems engine (nil: problems are not enabled)
	gcOnce sync.Once

	// oev is the one store all of the cluster's object-events watches write into (created on first use,
	// guarded by mu); it is attached to the view engine once as ingest.ObjectEventsGVR.
	oev *store.Store
}

func (r *runner) state() protocol.ClusterState {
	msg, _ := r.message.Load().(string)
	return protocol.ClusterState{ID: r.id, Phase: r.phase.Load().(protocol.ClusterPhase), Synced: int(r.synced.Load()), Message: msg}
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

func (r *runner) discoverLoop() {
	defer r.wg.Done()
	for {
		wait := r.m.DiscoveryEvery
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
				d.byG[d.list[i].GVR()] = &d.list[i]
			}
			r.disc.Store(d)
			if r.pe != nil {
				grs := make(map[string]bool, len(res))
				for i := range res {
					grs[problems.GR(res[i].GVR())] = true
				}
				r.pe.SetDiscovery(grs)
			}
			r.m.kick()
			r.m.reconcileAll()
		}
		t := time.NewTimer(wait)
		select {
		case <-r.ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-r.discKick:
			t.Stop()
			select { // debounce bursts of CRD changes
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
	wctx, cancel := context.WithCancel(r.ctx)
	w.cancel = cancel
	hooks := ingest.Hooks{
		OnRows: w.st.Provisional,
		OnOK: func() {
			w.setErr(nil)
			if r.pe != nil {
				r.pe.SetUnavailable(problems.GR(gvr), "")
			}
		},
		OnError: func(err error) {
			if wctx.Err() != nil || apierrors.IsResourceExpired(err) || apierrors.IsGone(err) {
				return // a routine relist, not a cluster problem
			}
			w.setErr(err)
			if r.pe != nil && r.pe.Interested(gvr) {
				if reason := unavailableReason(gvr, err); reason != "" {
					r.pe.SetUnavailable(problems.GR(gvr), reason)
				}
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
			r.m.log.Warn("cluster error", "cluster", r.id, "phase", p, "err", msg)
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
		w.st.OnActivity = func() {
			if p := r.phase.Load(); p == protocol.PhaseReconnecting || p == protocol.PhaseDegraded {
				r.setPhase(protocol.PhaseReady, "")
			}
		}
	}
	for _, ik := range invalidationKinds {
		if gvr == ik {
			var synced atomic.Bool
			w.st.OnReplace = func(int) { synced.Store(true) }
			w.st.OnActivity = func() {
				if synced.Load() {
					r.invalidate()
				}
			}
		}
	}
	lw := &ingest.ListerWatcher{API: api, Mod: mod, Generic: spec, Hooks: hooks}
	refl := cache.NewReflectorWithOptions(lw, &ingest.Row{}, w.st, cache.ReflectorOptions{Name: gvr + "/" + r.id, TypeDescription: gvr})
	refl.WatchListPageSize = 500 // the fallback LIST (and relists) page instead of one huge response
	r.watches[gvr] = w
	if r.pe != nil {
		r.pe.Attach(gvr, w.st)
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer close(w.done)
		refl.RunWithContext(wctx)
	}()
	if r.attached {
		r.m.eng.AttachKind(r.id, gvr, w.st)
	}
	return w, nil
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
	go func() {
		<-w.done
		w.st.Reset()
	}()
}

// acquire takes a reference on the watch for gvr, starting it when needed. release drops it; the last
// release starts the linger timer (the watch then stays warm for Linger, or until evicted).
func (r *runner) acquire(gvr string) (release func(), err error) {
	return r.acquireKey(gvr, r.m.Linger, func() (*watchEntry, error) {
		if err := r.makeRoomLocked(); err != nil {
			return nil, err
		}
		return r.startLocked(gvr, false)
	})
}

// acquireKey takes a reference on the watch stored under key, calling start (with r.mu held) when there is none.
// The last release starts a linger timer of linger.
func (r *runner) acquireKey(key string, linger time.Duration, start func() (*watchEntry, error)) (release func(), err error) {
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
	for gvr, w := range r.watches {
		if w.part == nil {
			r.m.eng.AttachKind(r.id, gvr, w.st)
		}
	}
	if r.oev != nil {
		r.m.eng.AttachKind(r.id, ingest.ObjectEventsGVR, r.oev)
	}
	r.mu.Unlock()
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
		if w.part != nil {
			w.part.Close()
		} else {
			w.st.Reset()
		}
	}
	if r.oev != nil {
		r.oev.Reset()
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

// WatchInfo describes one running watch (diagnostics and tests).
type WatchInfo struct {
	GVR       string
	Refs      int
	Tier0     bool
	Lingering bool
}

func (r *runner) info() []WatchInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]WatchInfo, 0, len(r.watches))
	for _, w := range r.watches {
		out = append(out, WatchInfo{GVR: w.gvr, Refs: w.refs, Tier0: w.tier0, Lingering: w.refs == 0 && !w.tier0})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GVR < out[j].GVR })
	return out
}
