package cluster

import (
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/usage"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func (m *Manager) nodesSnap(id string) *usage.Snap {
	r := m.runnerForTest(id)
	r.usage.mu.Lock()
	defer r.usage.mu.Unlock()
	return r.usage.nodes.snap
}

// capRow returns the cluster's published capacity cells (nil: no row).
func (m *Manager) capRow(id string) map[string]int64 {
	r := m.runnerForTest(id)
	var out map[string]int64
	r.capStore.Scan(func(row *ingest.Row) bool {
		out = map[string]int64{}
		for i, c := range ingest.Lookup(protocol.CapacityGVR).Cols[2:] {
			out[c.Key] = row.Cell(i).N
		}
		return false
	})
	return out
}

func TestNodesDemandPollsNodesNotPods(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{})
	d := m.OpenUsage("v1/nodes", nil, nil, true)
	waitFor(t, "an immediate node poll", func() bool { return mt.nodes.Load() >= 1 })
	waitFor(t, "polls at the interval", func() bool { return mt.nodes.Load() >= 3 })
	if mt.count() != 0 {
		t.Fatal("a nodes demand must not list pod metrics")
	}
	sn := m.nodesSnap("prod-us")
	if sn == nil || len(sn.Slots) == 0 {
		t.Fatalf("node snapshot = %+v", sn)
	}
	if a, _ := metricsState(m); a != protocol.MetricsOK {
		t.Fatalf("metrics state = %q", a)
	}
	var has int
	for _, s := range sn.Slots {
		if s.Has && s.NodeCPU > 0 {
			has++
		}
	}
	if has == 0 {
		t.Fatal("no node slot has usage and allocatable")
	}
	d.Close()
	time.Sleep(60 * time.Millisecond)
	n := mt.nodes.Load()
	steady(t, "node poll count after close", 400*time.Millisecond, func() bool { return mt.nodes.Load() == n })
	waitFor(t, "node snapshot dropped after UsageKeep", func() bool { return m.nodesSnap("prod-us") == nil })
}

func TestNodesViewReceivesUsage(t *testing.T) {
	m, eng, _ := usageManager(t, fake.Config{})
	sub, err := eng.Subscribe(protocol.ViewQuery{GVR: "v1/nodes"}, 1, protocol.Window{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	sub.SetUsage(true)
	d := m.OpenUsage("v1/nodes", nil, nil, true)
	defer d.Close()
	waitFor(t, "node rows with usage", func() bool {
		p := sub.Prepare()
		if p == nil {
			return false
		}
		n := 0
		for i := range p.Rows {
			if u := p.Rows[i].Use; u != nil && u.Has && u.NodeCPU > 0 {
				n++
			}
		}
		p.Commit(len(p.Rows), len(p.Removed), p.SendIDs)
		return n > 0
	})
}

func TestNodePollFailuresAreTheirOwnState(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{})
	mt.mu.Lock()
	mt.nerr = forbidden()
	mt.mu.Unlock()
	d := m.OpenUsage("v1/nodes", nil, nil, true)
	defer d.Close()
	waitFor(t, "forbidden", func() bool { a, _ := metricsState(m); return a == protocol.MetricsForbidden })
	if m.nodesSnap("prod-us") != nil {
		t.Fatal("a refused list clears the snapshot")
	}
	mt.mu.Lock()
	mt.nerr = nil
	mt.mu.Unlock()
	waitFor(t, "ok after the retry", func() bool { a, _ := metricsState(m); return a == protocol.MetricsOK })
}

func TestCapacityDemandPublishesTheRowAndPollsOnlyNodes(t *testing.T) {
	m, eng, mt := usageManager(t, fake.Config{})
	sub, err := eng.Subscribe(protocol.ViewQuery{GVR: protocol.CapacityGVR}, 1, protocol.Window{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if m.capRow("prod-us") != nil {
		t.Fatal("no capacity row before a card asks")
	}
	d := m.OpenUsage(protocol.CapacityGVR, nil, nil, true)
	waitFor(t, "a capacity row", func() bool { return m.capRow("prod-us") != nil })
	r := m.capRow("prod-us")
	if r["cpuUse"] < 0 || r["nodesReporting"] == 0 {
		t.Fatalf("the first capacity row must already carry node usage (no requested-to-used flip): %v", r)
	}
	if r["nodes"] != 150 || r["nodesReporting"] != 150 || r["cpuAlloc"] != 150*15890 || r["memAlloc"] != 150*63<<10 {
		t.Errorf("allocatable cells = %v", r)
	}
	if r["podsCounted"] == 0 || r["cpuReq"] == 0 || r["cpuLim"] == 0 || r["memReq"] == 0 || r["podsNoCPULim"] == 0 {
		t.Errorf("pod cells = %v", r)
	}
	if mt.count() != 0 {
		t.Error("the capacity card polls node metrics only, never pod metrics")
	}
	waitFor(t, "the engine serves the row", func() bool {
		p := sub.Prepare()
		return p != nil && len(p.Rows) == 1 && p.Total == 1
	})
	d.Close()
	// Without a card the row is dropped after UsageKeep (600 ms here) and node polling stops.
	waitFor(t, "row removed", func() bool { return m.capRow("prod-us") == nil })
}

func TestCapacityWithoutMetricsServerStillShowsRequestsAndLimits(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{NoMetrics: true})
	d := m.OpenUsage(protocol.CapacityGVR, nil, nil, true)
	defer d.Close()
	waitFor(t, "a capacity row", func() bool { return m.capRow("prod-us") != nil })
	r := m.capRow("prod-us")
	if r["cpuUse"] != -1 || r["memUse"] != -1 || r["nodesReporting"] != 0 || r["cpuReq"] == 0 || r["cpuAlloc"] == 0 {
		t.Errorf("no metrics-server: usage unknown, requests known: %v", r)
	}
	waitFor(t, "absent", func() bool { a, _ := metricsState(m); return a == protocol.MetricsAbsent })
	if mt.nodes.Load() != 0 {
		t.Error("absent metrics-server: no node polls")
	}
}

func TestCapacityRescansOnlyWhenSomethingChanged(t *testing.T) {
	m, _, _ := usageManager(t, fake.Config{}, func(m *Manager) { m.UsageMinGap, m.UsageInterval = 30*time.Millisecond, time.Hour })
	d := m.OpenUsage(protocol.CapacityGVR, nil, nil, true)
	defer d.Close()
	waitFor(t, "a capacity row", func() bool { return m.capRow("prod-us") != nil })
	u := m.runnerForTest("prod-us").usage
	waitFor(t, "node usage in the row", func() bool { return m.capRow("prod-us")["cpuUse"] >= 0 })
	// Let the pod store settle (the fake generates activity), then watch the scan clock: with nothing changing it stays put.
	time.Sleep(300 * time.Millisecond)
	var at time.Time
	steadyOrSkip := func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		at = u.cap.at
		return true
	}
	steadyOrSkip()
	first := at
	pods, nodes := u.store(ingest.PodsGVR), u.store(nodesGVR)
	ps, ns := pods.Seq(), nodes.Seq()
	time.Sleep(150 * time.Millisecond)
	steadyOrSkip()
	if pods.Seq() == ps && nodes.Seq() == ns && !first.Equal(at) {
		t.Errorf("rescanned with no change in the stores (%v -> %v)", first, at)
	}
}

func TestCapacityWithoutUsageFlagStillPublishesButPollsNoNodes(t *testing.T) {
	m, _, mt := usageManager(t, fake.Config{})
	d := m.OpenUsage(protocol.CapacityGVR, nil, nil, false)
	defer d.Close()
	waitFor(t, "a capacity row", func() bool { return m.capRow("prod-us") != nil })
	r := m.capRow("prod-us")
	if r["cpuUse"] != -1 || r["cpuReq"] == 0 || r["cpuAlloc"] == 0 {
		t.Errorf("requests and allocatable without usage: %v", r)
	}
	steady(t, "node poll count", 300*time.Millisecond, func() bool { return mt.nodes.Load() == 0 })
}

func TestAvailPrefersTheWantedSource(t *testing.T) {
	m, _, _ := usageManager(t, fake.Config{})
	u := m.runnerForTest("prod-us").usage
	u.mu.Lock()
	u.pods.avail, u.pods.msg = protocol.MetricsUnavailable, "pods down"
	u.nodes.avail = protocol.MetricsOK
	u.want = usageWant{nodes: true}
	u.mu.Unlock()
	if a, _ := u.avail(); a != protocol.MetricsOK {
		t.Errorf("a nodes view must not show the pods verdict: %q", a)
	}
	u.mu.Lock()
	u.want = usageWant{pods: true}
	u.mu.Unlock()
	if a, msg := u.avail(); a != protocol.MetricsUnavailable || msg != "pods down" {
		t.Errorf("a pods view shows the pods verdict: %q %q", a, msg)
	}
	u.mu.Lock()
	u.want = usageWant{}
	u.mu.Unlock()
	if a, _ := u.avail(); a != protocol.MetricsUnavailable {
		t.Errorf("nothing wanted: the non-ok verdict wins, got %q", a)
	}
}
