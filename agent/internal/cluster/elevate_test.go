package cluster

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/settings"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// newElevateManager: prod-us (no link), prod-eu (read-only rbac context) linked to prod-eu-admin, both selected.
func newElevateManager(t *testing.T) (*Manager, *settings.Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := NewManager(ctx, fake.NewSource(fake.Config{Clusters: 2, Pods: 100, RBAC: true}), eng, nil)
	t.Cleanup(m.Close)
	set, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.SetSettings(set)
	if _, err := set.Patch(map[string]any{"links": map[string]any{"prod-eu": "prod-eu-admin"}}); err != nil {
		t.Fatal(err)
	}
	hub := problems.NewHub(problems.HubConfig{Settings: set.Get})
	m.SetProblems(hub)
	m.Linger = time.Minute
	if _, err := m.Select([]string{"prod-us", "prod-eu"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"prod-us", "prod-eu"} {
		waitFor(t, id+" ready", func() bool {
			m.mu.Lock()
			r := m.runners[id]
			m.mu.Unlock()
			return r != nil && r.phase.Load() == protocol.PhaseReady && r.disc.Load() != nil
		})
	}
	return m, set
}

func runnerOf(m *Manager, id string) *runner {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runners[id]
}

func stateOf(m *Manager, id string) protocol.ClusterState {
	for _, s := range m.States().States {
		if s.ID == id {
			return s
		}
	}
	return protocol.ClusterState{}
}

func TestElevateRestartsOneCluster(t *testing.T) {
	m, _ := newElevateManager(t)
	usBefore, euBefore := runnerOf(m, "prod-us"), runnerOf(m, "prod-eu")
	usStore := usBefore.watches["v1/pods"].st
	peBefore := euBefore.pe

	const secrets = "v1/secrets"
	sc := m.OpenScope(secrets, []string{"prod-eu"})
	defer sc.Close()
	waitFor(t, "secrets watch forbidden on the read context", func() bool {
		r := runnerOf(m, "prod-eu")
		r.mu.Lock()
		w := r.watches[secrets]
		r.mu.Unlock()
		return w != nil && apierrors.IsForbidden(w.lastErr())
	})
	if m.KindSynced("prod-eu", secrets) {
		t.Fatal("secrets synced on the read context")
	}

	st, err := m.Elevate("prod-eu", true, "prod-eu-admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.States) != 2 || !stateOf(m, "prod-eu").Elevated || stateOf(m, "prod-us").Elevated {
		t.Fatalf("states: %+v", st)
	}
	if m.Elevated("prod-eu") != "prod-eu-admin" || m.Effective("prod-eu") != "prod-eu-admin" || m.Effective("prod-us") != "prod-us" {
		t.Fatal("Elevated/Effective")
	}
	eu := runnerOf(m, "prod-eu")
	if eu == euBefore || eu.ctxID != "prod-eu-admin" {
		t.Fatal("prod-eu runner not replaced")
	}
	if runnerOf(m, "prod-us") != usBefore || usBefore.watches["v1/pods"].st != usStore {
		t.Fatal("prod-us was touched")
	}
	if eu.pe == nil || eu.pe == peBefore {
		t.Fatal("problems engine not replaced")
	}
	waitFor(t, "secrets synced as the write context, scope held on the new runner", func() bool {
		w, ok := watchOf(m, "prod-eu", secrets)
		return ok && w.Refs == 1 && m.KindSynced("prod-eu", secrets)
	})
	waitFor(t, "elevated cluster ready", func() bool { return stateOf(m, "prod-eu").Phase == protocol.PhaseReady })

	// Idempotent.
	if _, err := m.Elevate("prod-eu", true, "prod-eu-admin"); err != nil || runnerOf(m, "prod-eu") != eu {
		t.Fatalf("second elevate: %v", err)
	}
	// Revert.
	if _, err := m.Elevate("prod-eu", false, ""); err != nil {
		t.Fatal(err)
	}
	back := runnerOf(m, "prod-eu")
	if back == eu || back.ctxID != "prod-eu" || m.Elevated("prod-eu") != "" || stateOf(m, "prod-eu").Elevated {
		t.Fatal("revert")
	}
	waitFor(t, "scope still held after revert", func() bool {
		w, ok := watchOf(m, "prod-eu", secrets)
		return ok && w.Refs == 1
	})
}

func TestElevateErrors(t *testing.T) {
	old := DeselectGrace
	DeselectGrace = time.Minute
	defer func() { DeselectGrace = old }()
	m, _ := newElevateManager(t)
	if _, err := m.Elevate("prod-us", true, ""); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("unlinked: %v", err)
	}
	if _, err := m.Elevate("nope", true, ""); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := m.Select([]string{"prod-us"}); err != nil { // prod-eu now in its grace period
		t.Fatal(err)
	}
	if _, err := m.Elevate("prod-eu", true, "prod-eu-admin"); !errors.Is(err, ErrClusterNotSelected) {
		t.Fatalf("grace: %v", err)
	}
	if _, err := m.Select([]string{"prod-us", "prod-eu"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Elevate("prod-eu", true, "prod-eu-admin"); err != nil {
		t.Fatalf("reselected: %v", err)
	}
}

func TestElevateSurvivesGraceReselectButNotStop(t *testing.T) {
	old := DeselectGrace
	DeselectGrace = 300 * time.Millisecond
	defer func() { DeselectGrace = old }()
	m, _ := newElevateManager(t)
	if _, err := m.Elevate("prod-eu", true, "prod-eu-admin"); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Select([]string{"prod-us"})
	_, _ = m.Select([]string{"prod-us", "prod-eu"})
	time.Sleep(500 * time.Millisecond)
	if m.Elevated("prod-eu") != "prod-eu-admin" {
		t.Fatal("elevation lost on a reselect within the grace period")
	}
	_, _ = m.Select([]string{"prod-us"})
	waitFor(t, "stopped after grace", func() bool { return !m.Running("prod-eu") })
	_, _ = m.Select([]string{"prod-us", "prod-eu"})
	if m.Elevated("prod-eu") != "" {
		t.Fatal("elevation survived the stop")
	}
}

func TestElevateRevertsWhenLinkRemoved(t *testing.T) {
	m, set := newElevateManager(t)
	if _, err := m.Elevate("prod-eu", true, "prod-eu-admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Patch(map[string]any{"links": map[string]any{"prod-eu": nil}}); err != nil {
		t.Fatal(err)
	}
	m.Changed()
	waitFor(t, "auto-revert", func() bool { return m.Elevated("prod-eu") == "" })
}

func TestElevateConcurrent(t *testing.T) {
	m, _ := newElevateManager(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(on bool) {
			defer wg.Done()
			if _, err := m.Elevate("prod-eu", on, "prod-eu-admin"); err != nil {
				t.Error(err)
			}
		}(i%2 == 0)
	}
	wg.Wait()
	e := m.Elevated("prod-eu")
	if e != "" && e != "prod-eu-admin" {
		t.Fatalf("elevated: %q", e)
	}
	if !m.Running("prod-eu") || len(m.States().States) != 2 {
		t.Fatal("cluster lost")
	}
	waitFor(t, "ready", func() bool { return stateOf(m, "prod-eu").Phase == protocol.PhaseReady })
}

func TestElevateGraceReselectAfterUnlink(t *testing.T) {
	old := DeselectGrace
	DeselectGrace = time.Minute
	defer func() { DeselectGrace = old }()
	m, set := newElevateManager(t)
	if _, err := m.Elevate("prod-eu", true, "prod-eu-admin"); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Select([]string{"prod-us"}) // prod-eu in its grace period
	if _, err := set.Patch(map[string]any{"links": map[string]any{"prod-eu": nil}}); err != nil {
		t.Fatal(err)
	}
	m.Changed()
	time.Sleep(100 * time.Millisecond)
	if _, err := m.Select([]string{"prod-us", "prod-eu"}); err != nil {
		t.Fatal(err)
	}
	if m.Elevated("prod-eu") != "" {
		t.Fatal("reselect re-attached an elevation whose link is gone")
	}
	waitFor(t, "prod-eu ready on the read context", func() bool { return stateOf(m, "prod-eu").Phase == protocol.PhaseReady })
}

func TestElevateRefusesChangedLink(t *testing.T) {
	m, _ := newElevateManager(t)
	if _, err := m.Elevate("prod-eu", true, "prod-us"); !errors.Is(err, ErrLinkChanged) {
		t.Fatalf("stale target: %v", err)
	}
	if m.Elevated("prod-eu") != "" {
		t.Fatal("elevated to an unverified context")
	}
}
