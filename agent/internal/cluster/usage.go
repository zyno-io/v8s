package cluster

import (
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/internal/usage"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Usage polling defaults (docs/performance.md §5: metrics.k8s.io is polled only while a visible view needs it).
const (
	DefaultUsageInterval    = 30 * time.Second
	DefaultUsageBigInterval = 60 * time.Second // cluster-wide pod lists over BigPods items
	DefaultUsageKeep        = 5 * time.Minute  // how long the last snapshot is kept once nothing wants usage
	DefaultUsageMinGap      = 2 * time.Second  // minimum gap between poll attempts, so scope churn does not storm
	DefaultUsageMaxBackoff  = 5 * time.Minute
	BigPods                 = 10_000
	maxScopeNamespaces      = 5 // more namespaces than this is one cluster-wide list

	metricsPodsGVR = "metrics.k8s.io/v1beta1/pods"
)

// UsageDemand is one view's request for usage: while open (and usage is true) the clusters it covers poll
// metrics.k8s.io for its namespaces.
type UsageDemand struct {
	m          *Manager
	gvr        string
	clusters   []string // empty = every selected cluster
	namespaces []string // empty = all namespaces
	usage      bool
	closed     bool
}

// OpenUsage registers a demand for the usage of gvr's rows on clusters (empty = all selected) in namespaces
// (empty = all). With usage false it registers nothing that polls. Open the replacement of a demand before
// closing the old one so demand never drops to zero for an instant (invariant 4).
func (m *Manager) OpenUsage(gvr string, clusters, namespaces []string, usage bool) *UsageDemand {
	d := &UsageDemand{m: m, gvr: gvr, clusters: slices.Clone(clusters), namespaces: slices.Clone(namespaces), usage: usage}
	m.umu.Lock()
	if m.demands == nil {
		m.demands = map[*UsageDemand]struct{}{}
	}
	m.demands[d] = struct{}{}
	m.umu.Unlock()
	m.reconcileUsage()
	return d
}

// Close releases the demand. Polling stops as soon as no demand is left (the snapshot is kept for UsageKeep).
func (d *UsageDemand) Close() {
	if d == nil {
		return
	}
	d.m.umu.Lock()
	if d.closed {
		d.m.umu.Unlock()
		return
	}
	d.closed = true
	delete(d.m.demands, d)
	d.m.umu.Unlock()
	d.m.reconcileUsage()
}

// UsageDemands returns the number of open usage demands (diagnostics and tests).
func (m *Manager) UsageDemands() int {
	m.umu.Lock()
	defer m.umu.Unlock()
	return len(m.demands)
}

// usageWant is what the open demands ask of one cluster.
type usageWant struct {
	pods     bool
	podScope []string // namespaces; nil with pods = all
	nodes    bool     // phase 1b: the nodes poll
	capacity bool     // phase 1c: the Overview capacity aggregate
}

func (m *Manager) wantFor(id string) usageWant {
	m.umu.Lock()
	defer m.umu.Unlock()
	var w usageWant
	all := false
	set := map[string]struct{}{}
	for d := range m.demands {
		if !d.usage || (len(d.clusters) > 0 && !slices.Contains(d.clusters, id)) {
			continue
		}
		switch d.gvr {
		case ingest.PodsGVR:
			w.pods = true
			if len(d.namespaces) == 0 {
				all = true
			}
			for _, n := range d.namespaces {
				set[n] = struct{}{}
			}
		case "v1/nodes":
			w.nodes = true
		case protocol.CapacityGVR:
			w.capacity, w.nodes = true, true
		}
	}
	if w.pods && !all && len(set) <= maxScopeNamespaces {
		for n := range set {
			w.podScope = append(w.podScope, n)
		}
		sort.Strings(w.podScope)
	}
	return w
}

// reconcileUsage hands every selected runner the union of what the open demands want of it.
func (m *Manager) reconcileUsage() {
	// One reconcile at a time, compute and apply together: two concurrent calls (two tabs subscribing at once) would
	// otherwise each compute a want, and the older one could be applied last. Nothing takes usageMu while holding
	// m.mu, umu or a runner's usage lock, and this takes them in that order.
	m.usageMu.Lock()
	defer m.usageMu.Unlock()
	m.mu.Lock()
	sel := make(map[string]bool, len(m.selected))
	for _, id := range m.selected {
		sel[id] = true
	}
	rs := make([]*runner, 0, len(m.runners))
	for _, r := range m.runners {
		rs = append(rs, r)
	}
	m.mu.Unlock()
	for _, r := range rs {
		if r.usage == nil {
			continue
		}
		var w usageWant
		if sel[r.id] {
			w = m.wantFor(r.id)
		}
		r.usage.setWant(w)
	}
}

// pollState is the polling state of one source (pods or nodes).
type pollState struct {
	snap     *usage.Snap
	at       time.Time // last successful poll
	tried    time.Time // last attempt
	next     time.Time // no attempt before this (backoff, refused)
	unwanted time.Time // since when nothing wants it (zero while wanted)
	fails    int
	items    int  // rows in the last pods poll
	fromDisc bool // avail is absent/unavailable because of discovery (re-evaluated at every discovery refresh), not a failed poll
	avail    protocol.MetricsAvail
	msg      string
}

// usageState is one runner's usage poller. Its loop goroutine is on r.wg; step runs synchronously in it, so at
// most one poll is in flight per cluster.
type usageState struct {
	r *runner

	mu   sync.Mutex
	want usageWant
	kick chan struct{}

	pods, nodes pollState // nodes: phase 1b (the node poller); nothing polls it yet
	seq         uint32    // usage.VerBase and only increasing (see usage.VerBase)
	buf         []store.Item
}

func newUsageState(r *runner) *usageState {
	return &usageState{r: r, kick: make(chan struct{}, 1), seq: usage.VerBase}
}

func (u *usageState) kickNow() {
	if u == nil {
		return
	}
	select {
	case u.kick <- struct{}{}:
	default:
	}
}

func (u *usageState) setWant(w usageWant) {
	u.mu.Lock()
	old := u.want
	u.want = w
	if w.pods && !old.pods && u.pods.avail == protocol.MetricsForbidden {
		u.pods.next = time.Time{} // demand restarting from nothing retries a refused list at once
	}
	u.mu.Unlock()
	u.kickNow()
}

// avail returns the pods availability and message for ClusterState.
func (u *usageState) avail() (protocol.MetricsAvail, string) {
	if u == nil {
		return "", ""
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.pods.avail, u.pods.msg
}

// republish hands the current pods snapshot to the engine again: a reselect within the deselect grace keeps
// the store but detach cleared the engine's copy. Never called with r.mu held (invariant 5).
func (u *usageState) republish() {
	if u == nil {
		return
	}
	u.mu.Lock()
	sn := u.pods.snap
	u.mu.Unlock()
	if sn != nil {
		u.r.m.eng.SetUsage(u.r.id, ingest.PodsGVR, sn)
	}
}

func (u *usageState) loop() {
	r := u.r
	defer r.wg.Done()
	t := time.NewTimer(time.Hour)
	defer t.Stop()
	for {
		wait := u.step(time.Now())
		t.Reset(wait)
		select {
		case <-r.ctx.Done():
			return
		case <-u.kick:
		case <-t.C:
		}
	}
}

const idleWait = time.Hour // nothing to do until kicked

// step does whatever is due and returns how long to sleep before the next look.
func (u *usageState) step(now time.Time) time.Duration {
	r, m := u.r, u.r.m
	u.mu.Lock()
	want := u.want
	u.mu.Unlock()

	p := &u.pods // only the loop goroutine writes the poll state; ClusterState reads under mu
	wake := idleWait
	soonest := func(d time.Duration) { wake = min(wake, max(d, time.Millisecond)) }

	// 1. availability, from discovery. It is judged whether or not anything wants usage, so the UI learns that
	// metrics-server is missing (and later that it appeared) without a poll.
	d := r.disc.Load()
	if d == nil {
		if !want.pods {
			return u.idle(now, soonest, &wake)
		}
		return idleWait // discoverLoop kicks once it has the list
	}
	res := d.byG[metricsPodsGVR]
	mr, hasReader := any(r.be).(kube.MetricsReader)
	switch {
	case res == nil && hasReader && metricsListed(d):
		// the group-version is listed but nothing answers: installed and down, not absent
		u.setDiscAvail(protocol.MetricsUnavailable, "metrics-server is not responding")
		u.dropPods()
		return idleWait
	case res == nil || !hasReader:
		u.setDiscAvail(protocol.MetricsAbsent, "metrics-server is not installed (no metrics.k8s.io in discovery)")
		u.dropPods()
		return idleWait // a discovery refresh kicks
	case res.Stale:
		u.setDiscAvail(protocol.MetricsUnavailable, "the metrics API is registered but its server is not answering")
		u.dropPods()
		return idleWait
	default:
		u.mu.Lock()
		was := p.fromDisc
		p.fromDisc = false
		u.mu.Unlock()
		if was {
			u.setAvail("", "") // it appeared (or recovered): unknown until a poll says otherwise
		}
	}

	if !want.pods {
		return u.idle(now, soonest, &wake)
	}
	u.mu.Lock()
	p.unwanted = time.Time{}
	u.mu.Unlock()

	interval := u.interval(want.podScope)
	// stale data is not kept past twice the interval while polls fail
	if p.snap != nil && p.fails > 0 && now.Sub(p.at) > 2*interval {
		u.dropPods()
	}

	due := p.snap == nil || !p.snap.Covers(want.podScope) || now.Sub(p.at) >= interval
	if due {
		ready := u.nextAt()
		if g := p.tried.Add(m.UsageMinGap); g.After(ready) {
			ready = g
		}
		if now.Before(ready) {
			soonest(ready.Sub(now))
			return wake
		}
		u.pollPods(now, want, mr, interval)
		now = time.Now()
		if p.snap != nil && p.snap.Covers(want.podScope) && p.fails == 0 {
			soonest(max(interval-now.Sub(p.at), m.UsageMinGap))
		} else {
			soonest(max(u.nextAt().Sub(now), m.UsageMinGap))
		}
		return wake
	}
	soonest(interval - now.Sub(p.at))
	return wake
}

func (u *usageState) setAvail(a protocol.MetricsAvail, msg string) {
	u.mu.Lock()
	changed := u.pods.avail != a || u.pods.msg != msg
	u.pods.avail, u.pods.msg = a, msg
	u.mu.Unlock()
	if changed {
		u.r.m.kick() // ClusterStates is re-sent
	}
}

// dropPods clears the pods snapshot and the engine's copy.
func (u *usageState) dropPods() {
	u.mu.Lock()
	had := u.pods.snap != nil
	u.pods.snap = nil
	u.mu.Unlock()
	if had {
		u.r.m.eng.SetUsage(u.r.id, ingest.PodsGVR, nil)
	}
}

func (u *usageState) nodeAlloc() map[string]usage.NodeAlloc {
	u.r.mu.Lock()
	w := u.r.watches["v1/nodes"]
	u.r.mu.Unlock()
	if w == nil || w.st == nil {
		return nil
	}
	out := map[string]usage.NodeAlloc{}
	w.st.Scan(func(row *ingest.Row) bool {
		c, mem := row.Allocatable()
		out[row.Name()] = usage.NodeAlloc{CPU: c, MemMi: uint32(min(mem>>20, 1<<32-1))}
		return true
	})
	return out
}

func (u *usageState) pollPods(now time.Time, want usageWant, mr kube.MetricsReader, interval time.Duration) {
	r, m := u.r, u.r.m
	p := &u.pods
	r.mu.Lock()
	w := r.watches[ingest.PodsGVR]
	r.mu.Unlock()
	if w == nil || w.st == nil {
		return
	}
	p.tried = now

	var samples []usage.PodSample
	var err error
	if want.podScope == nil {
		samples, err = mr.PodMetrics(r.ctx, "")
	} else {
		for _, ns := range want.podScope {
			var s []usage.PodSample
			if s, err = mr.PodMetrics(r.ctx, ns); err != nil {
				break
			}
			samples = append(samples, s...)
		}
	}
	if r.ctx.Err() != nil {
		return
	}
	if err != nil {
		u.pollFailed(now, err, interval)
		return
	}

	// The store snapshot is taken after the list returns so the join sees every pod the samples can name.
	u.buf = w.st.Snapshot(u.buf, want.podScope)
	nodes := u.nodeAlloc()
	u.mu.Lock()
	u.seq++
	seq := u.seq
	prev := p.snap
	u.mu.Unlock()
	sn := usage.BuildPods(w.st, u.buf, want.podScope, samples, nodes, prev, seq)
	clear(u.buf)
	u.buf = u.buf[:0]

	u.mu.Lock()
	p.snap, p.at, p.fails, p.next, p.items = sn, time.Now(), 0, time.Time{}, len(sn.Slots)
	u.mu.Unlock()
	u.setAvail(protocol.MetricsOK, "")
	m.eng.SetUsage(r.id, ingest.PodsGVR, sn)
}

// pollFailed maps a failed list to the availability states of docs/performance.md §5. None of them are toasts or
// log spam (debug level only).
func (u *usageState) pollFailed(now time.Time, err error, interval time.Duration) {
	r, m := u.r, u.r.m
	p := &u.pods
	r.m.log.Debug("metrics poll failed", "cluster", r.id, "err", err.Error())
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		u.dropPods()
		u.mu.Lock()
		p.fails = 0
		p.next = now.Add(m.ForbiddenRetry)
		u.mu.Unlock()
		u.setAvail(protocol.MetricsForbidden, "you are not allowed to read metrics.k8s.io")
		return
	}
	u.mu.Lock()
	p.fails++
	fails := p.fails
	delay := interval
	for i := 1; i < fails && delay < m.UsageMaxBackoff; i++ {
		delay *= 2 // 30 → 60 → 120 s …
	}
	p.next = now.Add(min(delay, m.UsageMaxBackoff))
	u.mu.Unlock()
	if fails >= 2 {
		u.setAvail(protocol.MetricsUnavailable, "metrics-server is not answering")
	}
}

func (u *usageState) nextAt() time.Time {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.pods.next
}

// idle handles a cluster nothing wants usage of: polling has stopped, the last snapshot is kept for UsageKeep.
func (u *usageState) idle(now time.Time, soonest func(time.Duration), wake *time.Duration) time.Duration {
	p, m := &u.pods, u.r.m
	u.mu.Lock()
	if p.unwanted.IsZero() {
		p.unwanted = now
	}
	un := p.unwanted
	u.mu.Unlock()
	if p.snap != nil {
		if now.Sub(un) >= m.UsageKeep {
			u.dropPods()
		} else {
			soonest(m.UsageKeep - now.Sub(un))
		}
	}
	return *wake
}

func (u *usageState) setDiscAvail(a protocol.MetricsAvail, msg string) {
	u.mu.Lock()
	u.pods.fromDisc = true
	u.mu.Unlock()
	u.setAvail(a, msg)
}

// interval is the poll interval: the long one for a cluster-wide pod list over BigPods items.
func (u *usageState) interval(scope []string) time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	if scope == nil && u.pods.items > BigPods {
		return u.r.m.UsageBigInterval
	}
	return u.r.m.UsageInterval
}

// metricsListed reports whether discovery lists any metrics.k8s.io group-version, even one that is not answering.
func metricsListed(d *discState) bool {
	for i := range d.list {
		if d.list[i].Group == "metrics.k8s.io" {
			return true
		}
	}
	return false
}
