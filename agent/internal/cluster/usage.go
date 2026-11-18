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

	metricsPodsGVR  = "metrics.k8s.io/v1beta1/pods"
	metricsNodesGVR = "metrics.k8s.io/v1beta1/nodes"
	nodesGVR        = "v1/nodes"
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
	nodes    bool     // the node metrics poll (a nodes view with usage, or a capacity card)
	capacity bool     // the Overview capacity aggregate
}

func (m *Manager) wantFor(id string) usageWant {
	m.umu.Lock()
	defer m.umu.Unlock()
	var w usageWant
	all := false
	set := map[string]struct{}{}
	for d := range m.demands {
		if len(d.clusters) > 0 && !slices.Contains(d.clusters, id) {
			continue
		}
		if d.gvr == protocol.CapacityGVR {
			// the capacity row (requests, limits, allocatable) is computed for any open card; only usage needs node metrics
			w.capacity = true
			w.nodes = w.nodes || d.usage
			continue
		}
		if !d.usage {
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
		case nodesGVR:
			w.nodes = true
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

// capState is the throttled Overview capacity aggregate (docs/performance.md): a full scan of the pod and node
// stores at most every UsageMinGap, only while a capacity card is open and only when something changed.
type capState struct {
	unwanted        time.Time
	at              time.Time // last scan
	podSeq, nodeSeq uint64    // Store.Seq at the last scan
	nodeSnap        *usage.Snap
	last            *ingest.Row // the published row (nil: none)
	rv              uint64
	buf             []store.Item
}

// usageState is one runner's usage poller. Its loop goroutine is on r.wg; step runs synchronously in it, so at
// most one poll is in flight per cluster.
type usageState struct {
	r *runner

	mu   sync.Mutex
	want usageWant
	kick chan struct{}

	pods, nodes pollState
	seq         uint32 // usage.VerBase and only increasing (see usage.VerBase)
	buf         []store.Item
	cap         capState
	repub       bool // republish snapshots to the engine on the next step
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
	// demand restarting from nothing retries a refused list at once
	if w.pods && !old.pods && u.pods.avail == protocol.MetricsForbidden {
		u.pods.next = time.Time{}
	}
	if w.nodes && !old.nodes && u.nodes.avail == protocol.MetricsForbidden {
		u.nodes.next = time.Time{}
	}
	u.mu.Unlock()
	u.kickNow()
}

// avail returns the availability and message for ClusterState. Sources something currently wants speak first (a
// nodes page must not show the pods poll's stale verdict, nor the reverse); among them the first non-ok verdict wins,
// then ok, then unknown. With both or neither wanted, both are consulted (discovery judges them without a demand).
func (u *usageState) avail() (protocol.MetricsAvail, string) {
	if u == nil {
		return "", ""
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	c := []*pollState{&u.pods, &u.nodes}
	switch {
	case u.want.pods && !u.want.nodes:
		c = c[:1]
	case u.want.nodes && !u.want.pods:
		c = c[1:]
	}
	var ok *pollState
	for _, p := range c {
		if p.avail == protocol.MetricsOK {
			ok = p
		} else if p.avail != "" {
			return p.avail, p.msg
		}
	}
	if ok != nil {
		return ok.avail, ok.msg
	}
	return "", ""
}

// republish asks the loop to hand the current snapshots to the engine again: a reselect within the deselect grace
// keeps the stores but detach cleared the engine's copies. It runs on the loop, the only goroutine that publishes or
// drops a snapshot, so it cannot interleave with a poll and leave an older snapshot behind. Never called with r.mu
// held (invariant 5).
func (u *usageState) republish() {
	if u == nil {
		return
	}
	u.mu.Lock()
	u.repub = true
	u.mu.Unlock()
	u.kickNow()
}

func (u *usageState) republishIfDue() {
	u.mu.Lock()
	due := u.repub
	u.repub = false
	ps, ns := u.pods.snap, u.nodes.snap
	u.mu.Unlock()
	if !due {
		return
	}
	if ps != nil {
		u.r.m.eng.SetUsage(u.r.id, ingest.PodsGVR, ps)
	}
	if ns != nil {
		u.r.m.eng.SetUsage(u.r.id, nodesGVR, ns)
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

// source is what one poll source (pods or nodes) needs from a step.
type source struct {
	ps     *pollState
	gvr    string // the kind whose rows the snapshot joins
	disc   string // the metrics.k8s.io discovery key
	wanted bool
	scope  []string // pods: namespaces, nil = all
	nodes  bool
}

// step does whatever is due and returns how long to sleep before the next look.
func (u *usageState) step(now time.Time) time.Duration {
	r := u.r
	u.mu.Lock()
	want := u.want
	u.mu.Unlock()

	u.republishIfDue()

	// Availability is judged from discovery whether or not anything wants usage, so the UI learns that
	// metrics-server is missing (and later that it appeared) without a poll.
	d := r.disc.Load()
	mr, _ := any(r.be).(kube.MetricsReader)
	wake := idleWait
	for _, sp := range []*source{
		{ps: &u.pods, gvr: ingest.PodsGVR, disc: metricsPodsGVR, wanted: want.pods, scope: want.podScope},
		{ps: &u.nodes, gvr: nodesGVR, disc: metricsNodesGVR, wanted: want.nodes, nodes: true},
	} {
		wake = min(wake, u.stepSource(now, sp, d, mr))
	}
	// After the sources: the first capacity row must not be published before the first node poll, or the card flips from
	// "requested" to "used" and briefly claims no node reports metrics.
	return min(wake, u.stepCapacity(time.Now(), want))
}

func (u *usageState) stepSource(now time.Time, sp *source, d *discState, mr kube.MetricsReader) time.Duration {
	m, p := u.r.m, sp.ps // only the loop goroutine writes the poll state; ClusterState reads under mu
	wake := idleWait
	soonest := func(d time.Duration) { wake = min(wake, max(d, time.Millisecond)) }

	if d == nil {
		if !sp.wanted {
			return u.idle(now, sp)
		}
		return idleWait // discoverLoop kicks once it has the list
	}
	res := d.byG[sp.disc]
	switch {
	case res == nil && mr != nil && metricsListed(d):
		// the group-version is listed but nothing answers: installed and down, not absent
		u.setDiscAvail(sp, protocol.MetricsUnavailable, "metrics-server is not responding")
		u.dropSnap(sp)
		return idleWait
	case res == nil || mr == nil:
		u.setDiscAvail(sp, protocol.MetricsAbsent, "metrics-server is not installed (no metrics.k8s.io in discovery)")
		u.dropSnap(sp)
		return idleWait // a discovery refresh kicks
	case res.Stale:
		u.setDiscAvail(sp, protocol.MetricsUnavailable, "the metrics API is registered but its server is not answering")
		u.dropSnap(sp)
		return idleWait
	default:
		u.mu.Lock()
		was := p.fromDisc
		p.fromDisc = false
		u.mu.Unlock()
		if was {
			u.setAvail(p, "", "") // it appeared (or recovered): unknown until a poll says otherwise
		}
	}

	if !sp.wanted {
		return u.idle(now, sp)
	}
	u.mu.Lock()
	p.unwanted = time.Time{}
	u.mu.Unlock()

	interval := m.UsageInterval
	if !sp.nodes {
		interval = u.interval(sp.scope)
	}
	// stale data is not kept past twice the interval while polls fail
	if p.snap != nil && p.fails > 0 && now.Sub(p.at) > 2*interval {
		u.dropSnap(sp)
	}

	due := p.snap == nil || !p.snap.Covers(sp.scope) || now.Sub(p.at) >= interval
	if due {
		ready := u.nextAt(p)
		if g := p.tried.Add(m.UsageMinGap); g.After(ready) {
			ready = g
		}
		if now.Before(ready) {
			soonest(ready.Sub(now))
			return wake
		}
		if sp.nodes {
			u.pollNodes(now, mr, interval)
		} else {
			u.pollPods(now, sp.scope, mr, interval)
		}
		now = time.Now()
		if p.snap != nil && p.snap.Covers(sp.scope) && p.fails == 0 {
			soonest(max(interval-now.Sub(p.at), m.UsageMinGap))
		} else {
			soonest(max(u.nextAt(p).Sub(now), m.UsageMinGap))
		}
		return wake
	}
	soonest(interval - now.Sub(p.at))
	return wake
}

func (u *usageState) setAvail(p *pollState, a protocol.MetricsAvail, msg string) {
	u.mu.Lock()
	changed := p.avail != a || p.msg != msg
	p.avail, p.msg = a, msg
	u.mu.Unlock()
	if changed {
		u.r.m.kick() // ClusterStates is re-sent
	}
}

// dropSnap clears a source's snapshot and the engine's copy.
func (u *usageState) dropSnap(sp *source) {
	u.mu.Lock()
	had := sp.ps.snap != nil
	sp.ps.snap = nil
	u.mu.Unlock()
	if had {
		u.r.m.eng.SetUsage(u.r.id, sp.gvr, nil)
	}
}

func (u *usageState) store(gvr string) *store.Store {
	u.r.mu.Lock()
	w := u.r.watches[gvr]
	u.r.mu.Unlock()
	if w == nil {
		return nil
	}
	return w.st
}

func (u *usageState) nodeAlloc() map[string]usage.NodeAlloc {
	st := u.store(nodesGVR)
	if st == nil {
		return nil
	}
	out := map[string]usage.NodeAlloc{}
	st.Scan(func(row *ingest.Row) bool {
		c, mem := row.Allocatable()
		out[row.Name()] = usage.NodeAlloc{CPU: c, MemMi: uint32(min(mem>>20, 1<<32-1))}
		return true
	})
	return out
}

func (u *usageState) pollPods(now time.Time, scope []string, mr kube.MetricsReader, interval time.Duration) {
	r, m := u.r, u.r.m
	p := &u.pods
	st := u.store(ingest.PodsGVR)
	if st == nil || !st.Synced() {
		return // joined before the first list landed it would be an empty snapshot for a whole interval; retried at UsageMinGap
	}
	p.tried = now

	var samples []usage.PodSample
	var err error
	if scope == nil {
		samples, err = mr.PodMetrics(r.ctx, "")
	} else {
		for _, ns := range scope {
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
		u.pollFailed(now, p, err, interval)
		return
	}

	// The store snapshot is taken after the list returns so the join sees every pod the samples can name.
	u.buf = st.Snapshot(u.buf, scope)
	nodes := u.nodeAlloc()
	u.mu.Lock()
	u.seq++
	seq := u.seq
	prev := p.snap
	u.mu.Unlock()
	sn := usage.BuildPods(st, u.buf, scope, samples, nodes, prev, seq)
	clear(u.buf)
	u.buf = u.buf[:0]

	u.mu.Lock()
	p.snap, p.at, p.fails, p.next, p.items = sn, time.Now(), 0, time.Time{}, len(sn.Slots)
	u.mu.Unlock()
	u.setAvail(p, protocol.MetricsOK, "")
	m.eng.SetUsage(r.id, ingest.PodsGVR, sn)
}

// pollNodes is the node poll: one small cluster-wide NodeMetrics list (one item per node).
func (u *usageState) pollNodes(now time.Time, mr kube.MetricsReader, interval time.Duration) {
	r, m := u.r, u.r.m
	p := &u.nodes
	st := u.store(nodesGVR)
	if st == nil || !st.Synced() {
		return
	}
	p.tried = now
	samples, err := mr.NodeMetrics(r.ctx)
	if r.ctx.Err() != nil {
		return
	}
	if err != nil {
		u.pollFailed(now, p, err, interval)
		return
	}
	u.buf = st.Snapshot(u.buf, nil)
	u.mu.Lock()
	u.seq++
	seq := u.seq
	prev := p.snap
	u.mu.Unlock()
	sn := usage.BuildNodes(st, u.buf, samples, prev, seq)
	clear(u.buf)
	u.buf = u.buf[:0]

	u.mu.Lock()
	p.snap, p.at, p.fails, p.next = sn, time.Now(), 0, time.Time{}
	u.mu.Unlock()
	u.setAvail(p, protocol.MetricsOK, "")
	m.eng.SetUsage(r.id, nodesGVR, sn)
}

// pollFailed maps a failed list to the availability states of docs/performance.md §5. None of them are toasts or
// log spam (debug level only).
func (u *usageState) pollFailed(now time.Time, p *pollState, err error, interval time.Duration) {
	r, m := u.r, u.r.m
	sp := &source{ps: p, gvr: ingest.PodsGVR}
	if p == &u.nodes {
		sp.gvr = nodesGVR
	}
	r.m.log.Debug("metrics poll failed", "cluster", r.id, "gvr", sp.gvr, "err", err.Error())
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		u.dropSnap(sp)
		u.mu.Lock()
		p.fails = 0
		p.next = now.Add(m.ForbiddenRetry)
		u.mu.Unlock()
		u.setAvail(p, protocol.MetricsForbidden, "you are not allowed to read metrics.k8s.io")
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
		u.setAvail(p, protocol.MetricsUnavailable, "metrics-server is not answering")
	}
}

func (u *usageState) nextAt(p *pollState) time.Time {
	u.mu.Lock()
	defer u.mu.Unlock()
	return p.next
}

// idle handles a source nothing wants usage of: polling has stopped, the last snapshot is kept for UsageKeep.
func (u *usageState) idle(now time.Time, sp *source) time.Duration {
	p, m := sp.ps, u.r.m
	u.mu.Lock()
	if p.unwanted.IsZero() {
		p.unwanted = now
	}
	un := p.unwanted
	u.mu.Unlock()
	if p.snap == nil {
		return idleWait
	}
	if now.Sub(un) >= m.UsageKeep {
		u.dropSnap(sp)
		return idleWait
	}
	return max(m.UsageKeep-now.Sub(un), time.Millisecond)
}

func (u *usageState) setDiscAvail(sp *source, a protocol.MetricsAvail, msg string) {
	u.mu.Lock()
	sp.ps.fromDisc = true
	u.mu.Unlock()
	u.setAvail(sp.ps, a, msg)
}

// interval is the pods poll interval: the long one for a cluster-wide list over BigPods items.
func (u *usageState) interval(scope []string) time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	if scope == nil && u.pods.items > BigPods {
		return u.r.m.UsageBigInterval
	}
	return u.r.m.UsageInterval
}

// stepCapacity recomputes the cluster's capacity row. While a capacity card is open it looks every UsageMinGap
// (two atomic loads when nothing changed) and rescans only when the pod store, the node store or the node usage
// snapshot changed; the scan itself is a few milliseconds at 50k pods. Once nothing wants it for UsageKeep the
// row is removed.
func (u *usageState) stepCapacity(now time.Time, want usageWant) time.Duration {
	r, m, c := u.r, u.r.m, &u.cap
	if r.capStore == nil {
		return idleWait
	}
	if !want.capacity {
		if c.last == nil {
			return idleWait
		}
		if c.unwanted.IsZero() {
			c.unwanted = now
		}
		if now.Sub(c.unwanted) >= m.UsageKeep {
			r.capStore.Apply(nil, []string{c.last.UID()})
			*c = capState{}
			return idleWait
		}
		return max(m.UsageKeep-now.Sub(c.unwanted), time.Millisecond)
	}
	c.unwanted = time.Time{}
	pods, nodes := u.store(ingest.PodsGVR), u.store(nodesGVR)
	if pods == nil || nodes == nil || !pods.Synced() || !nodes.Synced() {
		return m.UsageMinGap // not everything is in yet: a partial total would mislead
	}
	u.mu.Lock()
	ns := u.nodes.snap
	u.mu.Unlock()
	ps, nq := pods.Seq(), nodes.Seq()
	if c.last != nil && ps == c.podSeq && nq == c.nodeSeq && ns == c.nodeSnap {
		return m.UsageMinGap
	}
	if c.last != nil && now.Sub(c.at) < m.UsageMinGap {
		return max(m.UsageMinGap-now.Sub(c.at), time.Millisecond)
	}
	// Seq is read before the scan: a change during it just triggers another one.
	cp := usage.ScanCapacity(pods, nodes, ns, &c.buf)
	c.rv++
	row := cp.Row(r.id, c.rv)
	u.mu.Lock()
	c.at, c.podSeq, c.nodeSeq, c.nodeSnap = now, ps, nq, ns
	u.mu.Unlock()
	if c.last == nil || !c.last.CellsEqual(row) {
		c.last = row
		r.capStore.Apply([]*ingest.Row{row}, nil)
	}
	return m.UsageMinGap
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
