//go:build e2e

package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	_ "github.com/zyno-io/v8s/agent/internal/flux"
	_ "github.com/zyno-io/v8s/agent/internal/helm"
	"github.com/zyno-io/v8s/agent/internal/kube"
	_ "github.com/zyno-io/v8s/agent/internal/platform"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// TestPlatformProblems runs the Flux, Helm and platform detectors against the e2e cluster: Flux has a failing
// GitRepository `platform` and not-ready Kustomizations, every controller is up, every webhook has endpoints
// and the Helm releases are deployed.
func TestPlatformProblems(t *testing.T) {
	path := os.Getenv("V8S_E2E_KUBECONFIG")
	if path == "" {
		t.Skip("V8S_E2E_KUBECONFIG not set")
	}
	_ = os.Setenv("KUBE_FEATURE_WatchListClient", "true")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	src := &cluster.KubeSource{Loader: kube.Loader{Path: path}}
	cs := src.Clusters()
	if len(cs) == 0 {
		t.Fatal("no contexts in the kubeconfig")
	}
	id := cs[0].ID
	eng := view.NewEngine()
	go eng.Run(ctx)
	hub := problems.NewHub(problems.HubConfig{})
	m := cluster.NewManager(ctx, src, eng, nil)
	m.SetProblems(hub)
	defer m.Close()
	if _, err := m.Select([]string{id}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cluster ready", 60*time.Second, func() bool {
		st := m.States().States
		return len(st) == 1 && st[0].Phase == protocol.PhaseReady
	})
	find := func(class, kind, name string) bool {
		e := hub.Engine(id)
		if e == nil {
			return false
		}
		for _, p := range e.Problems() {
			if p.Class == class && p.Subject.Kind == kind && p.Subject.Name == name {
				return true
			}
		}
		return false
	}
	// Fresh clusters (CI) need minutes for Flux to reconcile and fail; a settled one answers at once.
	waitFor(t, "flux.source-failed GitRepository platform", 5*time.Minute, func() bool { return find("flux.source-failed", "GitRepository", "platform") })
	waitFor(t, "flux.not-ready Kustomization platform", 5*time.Minute, func() bool { return find("flux.not-ready", "Kustomization", "platform") })

	hasWatch := false
	for _, w := range m.WatchInfo(id) {
		hasWatch = hasWatch || (w.GVR == protocol.HelmGVR && w.Tier0)
	}
	if !hasWatch {
		t.Error("the Helm release watch is not part of the baseline")
	}
	// no false positives: controllers are up, webhooks have endpoints, no APIService is down, releases are deployed
	for _, p := range hub.Engine(id).Problems() {
		switch p.Class {
		case "flux.controller-down", "webhook.failing", "apiservice.unavailable", "helm.failed", "helm.stuck-pending":
			t.Errorf("unexpected %s: %s (%s)", p.Class, p.Title, p.Message)
		}
	}
}
