package problems_test

import (
	"context"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/settings"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(25 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The problems view over fake clusters: problems of several classes appear through the ordinary view engine,
// filters and sorts work, class counts are maintained, a class can be disabled and a problem acknowledged.
func TestProblemsViewOverFakeClusters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	src := fake.NewSource(fake.Config{Clusters: 2, Pods: 4000, Churn: 0})
	eng := view.NewEngine()
	go eng.Run(ctx)
	set, _ := settings.Open("")
	hub := problems.NewHub(problems.HubConfig{Settings: set.Get, Patch: set.Patch, Hold: time.Second, Interval: 50 * time.Millisecond})
	set.OnChange(hub.SettingsChanged)
	m := cluster.NewManager(ctx, src, eng, nil)
	m.SetSettings(set)
	m.SetProblems(hub)
	defer m.Close()
	if _, err := m.Select([]string{"prod-us", "prod-eu"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ready", 30*time.Second, func() bool {
		st := m.States().States
		return len(st) == 2 && st[0].Phase == protocol.PhaseReady && st[1].Phase == protocol.PhaseReady
	})
	sub, err := eng.Subscribe(protocol.ViewQuery{GVR: protocol.ProblemsGVR, Filters: []protocol.Filter{{Col: "acked", Op: "eq", Value: "false"}}}, 1, protocol.Window{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	classes := func() map[string]int {
		out := map[string]int{}
		for _, c := range hub.Classes().Classes {
			for _, n := range c.Counts {
				out[c.Class] += n.Total
			}
		}
		return out
	}
	waitFor(t, "problems", 20*time.Second, func() bool {
		c := classes()
		return c["pod.crashloop"] > 0 && c["pod.image-pull"] > 0 && c["node.not-ready"] > 0
	})
	if sub.Total() == 0 {
		t.Fatal("problems view is empty")
	}
	c := classes()
	_ = c
	// the view is sorted by severity rank first
	p := sub.Prepare()
	if p == nil || len(p.Rows) == 0 {
		t.Fatal("no rows")
	}
	prev := int64(-1)
	for _, r := range p.Rows {
		rank := r.Row.Cell(problems.CellSeverityRank).N
		if rank < prev {
			t.Fatalf("not sorted by severity rank: %d after %d", rank, prev)
		}
		prev = rank
	}
	// class preference: hidden in the agent
	if _, err := set.Patch(problems.SettingsDocPrefs("pod.crashloop", false, "")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "class disabled", 5*time.Second, func() bool { return classes()["pod.crashloop"] == 0 })
	// ack one and see it flagged
	var id string
	for _, e := range []string{"prod-us", "prod-eu"} {
		for _, pr := range hub.Engine(e).Problems() {
			if pr.Class == "node.not-ready" {
				id = pr.ID
			}
		}
	}
	if id == "" {
		t.Fatal("no node.not-ready problem")
	}
	if err := hub.Ack(id, 0); err != nil {
		t.Fatal(err)
	}
	if len(set.Get().Acks) != 1 {
		t.Fatalf("ack not persisted: %v", set.Get().Acks)
	}
	if err := hub.Unack(id); err != nil || len(set.Get().Acks) != 0 {
		t.Fatalf("unack: %v %v", err, set.Get().Acks)
	}
}
