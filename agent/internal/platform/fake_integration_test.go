package platform_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/fake"
	_ "github.com/zyno-io/v8s/agent/internal/flux"
	_ "github.com/zyno-io/v8s/agent/internal/helm"
	_ "github.com/zyno-io/v8s/agent/internal/platform"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// TestFakeClustersShowPlatformProblems selects a fake cluster with the real manager, watches and problems
// engine and waits for every class this package, flux and helm add to appear in the problems view source.
func TestFakeClustersShowPlatformProblems(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := view.NewEngine()
	go eng.Run(ctx)
	var skew atomic.Int64 // seconds the engine clock runs ahead: graces elapse without waiting
	hub := problems.NewHub(problems.HubConfig{Now: func() time.Time { return time.Now().Add(time.Duration(skew.Load()) * time.Second) }, Interval: 50 * time.Millisecond})
	m := cluster.NewManager(ctx, fake.NewSource(fake.Config{Clusters: 2, Pods: 400}), eng, nil)
	m.SetProblems(hub)
	defer m.Close()
	if _, err := m.Select([]string{"prod-us"}); err != nil { // cluster index 0: metrics APIService down, no flux-system down
		t.Fatal(err)
	}
	want := []string{"apiservice.unavailable", "webhook.failing", "cert.expiring", "gateway.route-unaccepted", "flux.source-failed", "flux.not-ready",
		"flux.stalled", "flux.stale", "flux.dependency-blocked", "helm.failed", "helm.stuck-pending"}
	deadline := time.Now().Add(40 * time.Second)
	var missing []string
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if time.Since(deadline.Add(-40*time.Second)) > 3*time.Second {
			skew.Store(3600)
		}
		e := hub.Engine("prod-us")
		if e == nil {
			continue
		}
		have := map[string]bool{}
		for _, p := range e.Problems() {
			have[p.Class] = true
		}
		missing = missing[:0]
		for _, c := range want {
			if !have[c] {
				missing = append(missing, c)
			}
		}
		if len(missing) == 0 {
			break
		}
	}
	if len(missing) > 0 {
		t.Fatalf("classes missing after 40 s: %v", missing)
	}
	// the second fake cluster has the flux source-controller down instead of the APIService
	if _, err := m.Select([]string{"prod-us", "prod-eu"}); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if e := hub.Engine("prod-eu"); e != nil {
			for _, p := range e.Problems() {
				if p.Class == "flux.controller-down" && p.Subject.Name == "source-controller" {
					return
				}
			}
		}
	}
	t.Fatal("flux.controller-down not raised in prod-eu")
	_ = protocol.PhaseReady
}
