package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSelectDeselectLifecycle(t *testing.T) {
	old := DeselectGrace
	DeselectGrace = 300 * time.Millisecond
	defer func() { DeselectGrace = old }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := NewManager(ctx, fake.NewSource(fake.Config{Clusters: 2, Pods: 2000}), eng, nil)
	defer m.Close()

	if l := m.List(); len(l.Clusters) != 2 || len(l.Selected) != 2 {
		t.Fatalf("list: %+v", l)
	}
	if _, err := m.Select([]string{"prod-us", "prod-eu"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both ready", func() bool {
		st := m.States().States
		return len(st) == 2 && st[0].Phase == protocol.PhaseReady && st[1].Phase == protocol.PhaseReady
	})
	sub, _ := eng.Subscribe(protocol.ViewQuery{}, 1, protocol.Window{Limit: 10})
	waitFor(t, "2000 rows", func() bool { eng.Pump(); return sub.Total() == 2000 })

	// Deselect one: it leaves the views at once but keeps running during the grace period.
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	if sub.Total() != 1000 || len(m.States().States) != 1 || !m.Running("prod-eu") {
		t.Fatalf("after deselect: total=%d states=%d running=%v", sub.Total(), len(m.States().States), m.Running("prod-eu"))
	}
	// Reselect inside the grace period: no restart, rows come back.
	if _, err := m.Select([]string{"prod-us", "prod-eu"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if !m.Running("prod-eu") || sub.Total() != 2000 {
		t.Fatalf("reselect during grace lost the cluster: total=%d", sub.Total())
	}
	// Deselect and wait out the grace: ingest stops and memory is released.
	_, _ = m.Select([]string{"prod-us"})
	waitFor(t, "grace stop", func() bool { return !m.Running("prod-eu") })
	if _, err := m.Select([]string{"nope"}); err == nil {
		t.Fatal("unknown cluster must be rejected")
	}
}

func TestClassifyErrors(t *testing.T) {
	gr := schema.GroupResource{Resource: "pods"}
	cases := []struct {
		err   error
		phase protocol.ClusterPhase
	}{
		{apierrors.NewUnauthorized("no"), protocol.PhaseAuthRequired},
		{apierrors.NewForbidden(gr, "x", errors.New("no")), protocol.PhaseAuthRequired},
		{errors.New("Get https://x: dial tcp 10.0.0.1:443: connect: connection refused"), protocol.PhaseUnreachable},
		{errors.New("getting credentials: exec: executable aws failed"), protocol.PhaseAuthRequired},
		{errors.New("something odd"), protocol.PhaseDegraded},
	}
	for _, c := range cases {
		if p, msg := classify(c.err); p != c.phase || msg == "" {
			t.Errorf("%v: phase %s msg %q, want %s", c.err, p, msg, c.phase)
		}
	}
}
