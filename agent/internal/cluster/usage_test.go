package cluster

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/disco"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/usage"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// meter counts the metrics.k8s.io lists a cluster makes, and can make them fail.
type meter struct {
	mu    sync.Mutex
	calls []string // the namespace of each pod list ("" = cluster-wide)
	times []time.Time
	nodes atomic.Int64
	err   error
	nerr  error // node list failure
	stale atomic.Bool
	down  atomic.Bool // metrics.k8s.io is listed by discovery but lists no resources (its server is down)
}

func (m *meter) setErr(err error) { m.mu.Lock(); m.err = err; m.mu.Unlock() }
func (m *meter) count() int       { m.mu.Lock(); defer m.mu.Unlock(); return len(m.calls) }
func (m *meter) namespaces() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

type meterBackend struct {
	kube.Backend
	m *meter
}

func (b meterBackend) PodMetrics(ctx context.Context, ns string) ([]usage.PodSample, error) {
	b.m.mu.Lock()
	b.m.calls = append(b.m.calls, ns)
	b.m.times = append(b.m.times, time.Now())
	err := b.m.err
	b.m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return b.Backend.(kube.MetricsReader).PodMetrics(ctx, ns)
}

func (b meterBackend) NodeMetrics(ctx context.Context) ([]usage.NodeSample, error) {
	b.m.nodes.Add(1)
	b.m.mu.Lock()
	err := b.m.nerr
	b.m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return b.Backend.(kube.MetricsReader).NodeMetrics(ctx)
}

func (b meterBackend) Discover(ctx context.Context) ([]disco.Resource, error) {
	res, err := b.Backend.Discover(ctx)
	if b.m.down.Load() {
		res = slices.DeleteFunc(res, func(r disco.Resource) bool { return r.Group == "metrics.k8s.io" })
		res = append(res, disco.Resource{Group: "metrics.k8s.io", Version: "v1beta1", Stale: true})
	}
	if b.m.stale.Load() {
		for i := range res {
			if res[i].Group == "metrics.k8s.io" {
				res[i].Stale = true
			}
		}
	}
	return res, err
}

type meterSource struct {
	*fake.Source
	m *meter
}

func (s meterSource) Backend(id string) (Backend, error) {
	b, err := s.Source.Backend(id)
	if err != nil {
		return nil, err
	}
	return meterBackend{b, s.m}, nil
}

// usageManager selects prod-us (300 pods in 37 namespaces) over a metering backend with short intervals.
func usageManager(t *testing.T, cfg fake.Config, opts ...func(*Manager)) (*Manager, *view.Engine, *meter) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := view.NewEngine()
	go eng.Run(ctx)
	mt := &meter{}
	cfg.Clusters, cfg.Pods = 1, 300
	m := NewManager(ctx, meterSource{fake.NewSource(cfg), mt}, eng, nil)
	m.UsageInterval, m.UsageBigInterval, m.UsageKeep, m.UsageMinGap, m.UsageMaxBackoff = 150*time.Millisecond, 300*time.Millisecond, 600*time.Millisecond, 20*time.Millisecond, 600*time.Millisecond
	m.ForbiddenRetry = 400 * time.Millisecond
	for _, o := range opts { // before Select: the poller reads these without a lock
		o(m)
	}
	t.Cleanup(m.Close)
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	readyAndDiscovered(t, m, "prod-us")
	return m, eng, mt
}

func metricsState(m *Manager) (protocol.MetricsAvail, string) {
	st := m.States().States
	if len(st) == 0 {
		return "", ""
	}
	return st[0].Metrics, st[0].MetricsMessage
}

func (m *Manager) podsSnap(id string) *usage.Snap {
	r := m.runnerForTest(id)
	r.usage.mu.Lock()
	defer r.usage.mu.Unlock()
	return r.usage.pods.snap
}

func steady(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if !cond() {
			t.Fatalf("%s changed within %v", what, d)
		}
	}
}

func TestNoDemandNoCalls(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{})
	steady(t, "call count", 400*time.Millisecond, func() bool { return mt.count() == 0 })
	// A demand without the usage flag polls nothing either.
	d := m.OpenUsage(ingest.PodsGVR, nil, nil, false)
	defer d.Close()
	steady(t, "call count", 300*time.Millisecond, func() bool { return mt.count() == 0 })
}

func TestDemandPollsImmediatelyThenEveryIntervalAndCloseStops(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{})
	d := m.OpenUsage(ingest.PodsGVR, nil, nil, true)
	waitFor(t, "the immediate first poll", func() bool { return mt.count() >= 1 })
	if mt.namespaces()[0] != "" {
		t.Fatalf("all namespaces is one cluster-wide list, got %v", mt.namespaces())
	}
	waitFor(t, "polls at the interval", func() bool { return mt.count() >= 4 })
	if a, _ := metricsState(m); a != protocol.MetricsOK {
		t.Fatalf("metrics state = %q", a)
	}
	if sn := m.podsSnap("prod-us"); sn == nil || sn.Scope != nil || len(sn.Slots) == 0 {
		t.Fatalf("snapshot = %+v", sn)
	}
	d.Close()
	time.Sleep(60 * time.Millisecond) // a poll in flight finishes
	n := mt.count()
	steady(t, "call count after close", 500*time.Millisecond, func() bool { return mt.count() == n })
	d.Close() // closing twice is harmless
}

func TestReopenWhileFreshDoesNotPollStaleDoes(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{}, func(m *Manager) { m.UsageInterval = 500 * time.Millisecond })
	d := m.OpenUsage(ingest.PodsGVR, nil, nil, true)
	waitFor(t, "first poll", func() bool { return mt.count() == 1 })
	d.Close()
	d = m.OpenUsage(ingest.PodsGVR, nil, nil, true) // the snapshot is younger than the interval
	steady(t, "call count on a fresh reopen", 250*time.Millisecond, func() bool { return mt.count() == 1 })
	d.Close()
	time.Sleep(600 * time.Millisecond) // older than the interval (and still kept: UsageKeep is 600 ms)
	d = m.OpenUsage(ingest.PodsGVR, nil, nil, true)
	defer d.Close()
	waitFor(t, "an immediate poll when stale", func() bool { return mt.count() >= 2 })
}

func TestScopeIsPerNamespaceUpToFiveThenClusterWide(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{})
	d := m.OpenUsage(ingest.PodsGVR, nil, []string{"team-1", "team-2"}, true)
	waitFor(t, "two namespace lists", func() bool { return mt.count() >= 2 })
	if got := mt.namespaces()[:2]; !slices.Equal(got, []string{"team-1", "team-2"}) {
		t.Fatalf("per-namespace lists = %v", got)
	}
	waitFor(t, "a snapshot of both namespaces", func() bool {
		sn := m.podsSnap("prod-us")
		return sn != nil && slices.Equal(sn.Scope, []string{"team-1", "team-2"})
	})
	// Another view widens the union: the new demand opens before the old closes, and the snapshot is rebuilt for the union.
	d2 := m.OpenUsage(ingest.PodsGVR, nil, []string{"team-3"}, true)
	d.Close()
	waitFor(t, "a snapshot covering team-3", func() bool {
		sn := m.podsSnap("prod-us")
		return sn != nil && sn.Covers([]string{"team-3"})
	})
	d2.Close()

	// More than five namespaces is one cluster-wide list; any all-namespaces demand wins as well.
	w := usageWant{}
	m.umu.Lock()
	m.demands = nil
	m.umu.Unlock()
	var ds []*UsageDemand
	for _, ns := range [][]string{{"a", "b", "c"}, {"d", "e", "f"}} {
		ds = append(ds, m.OpenUsage(ingest.PodsGVR, nil, ns, true))
	}
	if w = m.wantFor("prod-us"); !w.pods || w.podScope != nil {
		t.Fatalf("six namespaces must be cluster-wide: %+v", w)
	}
	for _, d := range ds {
		d.Close()
	}
	ds = []*UsageDemand{m.OpenUsage(ingest.PodsGVR, nil, []string{"a"}, true), m.OpenUsage(ingest.PodsGVR, nil, nil, true)}
	if w = m.wantFor("prod-us"); !w.pods || w.podScope != nil {
		t.Fatalf("an all-namespaces demand must win: %+v", w)
	}
	for _, d := range ds {
		d.Close()
	}
	// A demand for another cluster does not concern this one.
	d3 := m.OpenUsage(ingest.PodsGVR, []string{"elsewhere"}, nil, true)
	defer d3.Close()
	if w = m.wantFor("prod-us"); w.pods {
		t.Fatalf("a demand for another cluster leaked: %+v", w)
	}
	if w = m.wantFor("elsewhere"); !w.pods {
		t.Fatalf("the demand lost its own cluster: %+v", w)
	}
	// Nodes and capacity demands feed the node poller wants (phase 1b/1c).
	n := m.OpenUsage("v1/nodes", nil, nil, true)
	c := m.OpenUsage(protocol.CapacityGVR, nil, nil, true)
	if w = m.wantFor("prod-us"); !w.nodes || !w.capacity || w.pods {
		t.Fatalf("nodes/capacity wants = %+v", w)
	}
	n.Close()
	c.Close()
}

func TestAbsentMetricsServerNeverPolls(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{NoMetrics: true})
	d := m.OpenUsage(ingest.PodsGVR, nil, nil, true)
	defer d.Close()
	waitFor(t, "absent", func() bool { a, _ := metricsState(m); return a == protocol.MetricsAbsent })
	steady(t, "call count", 300*time.Millisecond, func() bool { return mt.count() == 0 })
	if _, msg := metricsState(m); msg == "" {
		t.Error("an absent state explains itself")
	}
}

func TestAbsentIsJudgedWithoutDemandAndRecovers(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{})
	if a, _ := metricsState(m); a != "" {
		t.Fatalf("metrics state before anything asked = %q", a)
	}
	// A stale aggregated API is unavailable, and learned from discovery alone (no demand, no call).
	mt.stale.Store(true)
	m.runnerForTest("prod-us").invalidate()
	waitFor(t, "unavailable from discovery", func() bool { a, _ := metricsState(m); return a == protocol.MetricsUnavailable })
	if mt.count() != 0 {
		t.Fatal("no demand: no call")
	}
	mt.stale.Store(false)
	m.runnerForTest("prod-us").invalidate()
	waitFor(t, "unknown again once it recovers", func() bool { a, _ := metricsState(m); return a == "" })
	d := m.OpenUsage(ingest.PodsGVR, nil, nil, true)
	defer d.Close()
	waitFor(t, "ok", func() bool { a, _ := metricsState(m); return a == protocol.MetricsOK })
}

func forbidden() error {
	return apierrors.NewForbidden(schema.GroupResource{Group: "metrics.k8s.io", Resource: "pods"}, "", nil)
}

func TestForbiddenRetriesAfterForbiddenRetry(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{})
	mt.setErr(forbidden())
	d := m.OpenUsage(ingest.PodsGVR, nil, nil, true)
	defer d.Close()
	waitFor(t, "forbidden", func() bool { a, _ := metricsState(m); return a == protocol.MetricsForbidden })
	if m.podsSnap("prod-us") != nil {
		t.Fatal("a refused list clears the snapshot")
	}
	if n := mt.count(); n != 1 {
		t.Fatalf("calls = %d, want one", n)
	}
	steady(t, "call count while refused", 250*time.Millisecond, func() bool { return mt.count() == 1 })
	waitFor(t, "a retry after ForbiddenRetry", func() bool { return mt.count() > 1 })
	mt.setErr(nil)
	waitFor(t, "ok once allowed", func() bool { a, _ := metricsState(m); return a == protocol.MetricsOK })
}

func TestForbiddenRetriesWhenDemandRestartsFromNothing(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{}, func(m *Manager) { m.ForbiddenRetry = time.Hour })
	mt.setErr(forbidden())
	d := m.OpenUsage(ingest.PodsGVR, nil, nil, true)
	waitFor(t, "forbidden", func() bool { a, _ := metricsState(m); return a == protocol.MetricsForbidden })
	steady(t, "call count while parked for an hour", 200*time.Millisecond, func() bool { return mt.count() == 1 })
	d.Close()
	time.Sleep(50 * time.Millisecond)
	mt.setErr(nil)
	d = m.OpenUsage(ingest.PodsGVR, nil, nil, true)
	defer d.Close()
	waitFor(t, "an immediate retry, ok", func() bool { a, _ := metricsState(m); return a == protocol.MetricsOK })
}

func TestUnavailableBacksOffAndClearsStaleSnapshot(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{})
	d := m.OpenUsage(ingest.PodsGVR, nil, nil, true)
	defer d.Close()
	waitFor(t, "ok", func() bool { a, _ := metricsState(m); return a == protocol.MetricsOK })
	if m.podsSnap("prod-us") == nil {
		t.Fatal("no snapshot")
	}
	mt.mu.Lock()
	from := len(mt.times)
	mt.err = apierrors.NewServiceUnavailable("metrics-server is down")
	mt.mu.Unlock()
	waitFor(t, "unavailable after repeated failures", func() bool { a, _ := metricsState(m); return a == protocol.MetricsUnavailable })
	// The snapshot is kept until it is older than twice the interval, then cleared.
	waitFor(t, "the stale snapshot dropped", func() bool { return m.podsSnap("prod-us") == nil })

	// Failures back off (interval 150 ms: 150, 300, 600, capped at 600): the gaps grow, then stop at the cap.
	waitFor(t, "four failed attempts", func() bool { mt.mu.Lock(); defer mt.mu.Unlock(); return len(mt.times)-from >= 5 })
	mt.mu.Lock()
	ts := slices.Clone(mt.times[from:])
	mt.mu.Unlock()
	var gaps []time.Duration
	for i := 1; i < len(ts); i++ {
		gaps = append(gaps, ts[i].Sub(ts[i-1]))
	}
	if gaps[1] < gaps[0]+80*time.Millisecond || gaps[2] < gaps[1]+80*time.Millisecond {
		t.Errorf("gaps did not grow: %v", gaps)
	}
	for _, g := range gaps {
		if g > m.UsageMaxBackoff+200*time.Millisecond {
			t.Errorf("gap %v exceeds the backoff cap %v (%v)", g, m.UsageMaxBackoff, gaps)
		}
	}

	// Recovery: the next successful poll is ok again.
	mt.setErr(nil)
	waitFor(t, "ok again", func() bool { a, _ := metricsState(m); return a == protocol.MetricsOK })
}

func TestSnapshotDroppedAfterUsageKeepAndPollsStopImmediately(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{})
	d := m.OpenUsage(ingest.PodsGVR, nil, nil, true)
	waitFor(t, "snapshot", func() bool { return m.podsSnap("prod-us") != nil })
	d.Close()
	time.Sleep(50 * time.Millisecond)
	n := mt.count()
	if m.podsSnap("prod-us") == nil {
		t.Fatal("the snapshot is kept for UsageKeep after the last demand closes")
	}
	steady(t, "call count (no linger of polling)", 300*time.Millisecond, func() bool { return mt.count() == n })
	waitFor(t, "dropped after UsageKeep", func() bool { return m.podsSnap("prod-us") == nil })
}

func TestSnapshotReachesViewsAndSurvivesAReselectInsideTheGrace(t *testing.T) {
	old := DeselectGrace
	DeselectGrace = 5 * time.Second
	defer func() { DeselectGrace = old }()
	m, eng, _ := usageManager(t, fake.Config{})
	sub, err := eng.Subscribe(protocol.ViewQuery{GVR: ingest.PodsGVR}, 1, protocol.Window{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	sub.SetUsage(true)
	d := m.OpenUsage(ingest.PodsGVR, nil, nil, true)
	defer d.Close()
	withUsage := func() int {
		p := sub.Prepare()
		if p == nil {
			return -1
		}
		n := 0
		for i := range p.Rows {
			if p.Rows[i].Use != nil && p.Rows[i].Use.Has {
				n++
			}
		}
		p.Commit(len(p.Rows), len(p.Removed), p.SendIDs)
		return n
	}
	waitFor(t, "rows with usage reach the subscription", func() bool { return withUsage() > 0 })

	// Deselect: the engine detaches and drops its copy; reselecting inside the grace re-publishes the kept snapshot.
	_, _ = m.Select(nil)
	_, _ = m.Select([]string{"prod-us"})
	waitFor(t, "usage after the reselect", func() bool {
		sub.MarkDirty()
		return withUsage() > 0
	})
}

func TestRunnerStopEndsTheUsageLoop(t *testing.T) {
	old := DeselectGrace
	DeselectGrace = 50 * time.Millisecond
	defer func() { DeselectGrace = old }()
	m, _, mt := usageManager(t, fake.Config{})
	d := m.OpenUsage(ingest.PodsGVR, nil, nil, true)
	defer d.Close()
	waitFor(t, "polling", func() bool { return mt.count() >= 2 })
	r := m.runnerForTest("prod-us")
	_, _ = m.Select(nil)
	waitFor(t, "runner stopped", func() bool { return !m.Running("prod-us") })
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }() // stop() already waited; this returns at once if the loop is gone
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the usage loop outlived its runner")
	}
	n := mt.count()
	steady(t, "call count after stop", 400*time.Millisecond, func() bool { return mt.count() == n })
}

func TestClusterWideListOverBigPodsUsesTheBigInterval(t *testing.T) {
	m, _, _ := usageManager(t, fake.Config{}, func(m *Manager) { m.UsageInterval, m.UsageBigInterval = 100*time.Millisecond, 700*time.Millisecond })
	u := m.runnerForTest("prod-us").usage
	if got := u.interval(nil); got != 100*time.Millisecond {
		t.Fatalf("interval = %v", got)
	}
	u.mu.Lock()
	u.pods.items = BigPods + 1
	u.mu.Unlock()
	if got := u.interval(nil); got != 700*time.Millisecond {
		t.Fatalf("a cluster-wide list over %d pods: interval = %v, want the big one", BigPods, got)
	}
	if got := u.interval([]string{"team-1"}); got != 100*time.Millisecond {
		t.Fatalf("a namespace-scoped list keeps the normal interval, got %v", got)
	}
}

// Installed but down (listed in discovery, no resources) is unavailable; not listed at all is absent.
func TestListedButDownIsUnavailableNotAbsent(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{})
	mt.down.Store(true)
	m.runnerForTest("prod-us").invalidate()
	waitFor(t, "unavailable", func() bool {
		a, msg := metricsState(m)
		return a == protocol.MetricsUnavailable && msg == "metrics-server is not responding"
	})
	mt.down.Store(false)
	m.runnerForTest("prod-us").invalidate()
	waitFor(t, "unknown again", func() bool { a, _ := metricsState(m); return a == "" })
}

// Concurrent opens and closes (several tabs at once) must leave each runner with the want its open demands derive.
func TestConcurrentDemandsLeaveTheDerivedWant(t *testing.T) {
	m, _, _ := usageManager(t, fake.Config{})
	var wg sync.WaitGroup
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var open []*UsageDemand
			for i := 0; i < 300; i++ {
				ns := []string{"team-" + string(rune('a'+(g+i)%7))}
				if i%5 == 0 {
					ns = nil
				}
				open = append(open, m.OpenUsage(ingest.PodsGVR, nil, ns, true))
				if len(open) > 2 {
					open[0].Close()
					open = open[1:]
				}
			}
			if g%2 == 0 { // half leave their demands open, half close everything
				for _, d := range open {
					d.Close()
				}
			}
		}()
	}
	wg.Wait()
	u := m.runnerForTest("prod-us").usage
	u.mu.Lock()
	got := u.want
	u.mu.Unlock()
	want := m.wantFor("prod-us")
	if got.pods != want.pods || !slices.Equal(got.podScope, want.podScope) || got.nodes != want.nodes {
		t.Fatalf("applied want %+v, derived %+v", got, want)
	}
	// With everything closed, nothing is wanted.
	m.umu.Lock()
	ds := make([]*UsageDemand, 0, len(m.demands))
	for d := range m.demands {
		ds = append(ds, d)
	}
	m.umu.Unlock()
	for _, d := range ds {
		d.Close()
	}
	u.mu.Lock()
	got = u.want
	u.mu.Unlock()
	if got.pods {
		t.Fatalf("pods still wanted with no demand: %+v", got)
	}
}
