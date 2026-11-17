package cluster

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/settings"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func newFakeManager(t *testing.T, cfg fake.Config) (*Manager, *view.Engine) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := NewManager(ctx, fake.NewSource(cfg), eng, nil)
	t.Cleanup(m.Close)
	return m, eng
}

func watchOf(m *Manager, cl, gvr string) (WatchInfo, bool) {
	for _, w := range m.WatchInfo(cl) {
		if w.GVR == gvr {
			return w, true
		}
	}
	return WatchInfo{}, false
}

func readyAndDiscovered(t *testing.T, m *Manager, id string) {
	t.Helper()
	waitFor(t, "ready + discovery", func() bool {
		st := m.States().States
		m.mu.Lock()
		r := m.runners[id]
		m.mu.Unlock()
		return len(st) > 0 && st[0].Phase == protocol.PhaseReady && r != nil && r.disc.Load() != nil
	})
}

func TestTier0StartsOnSelectAndRunsUntilDeselect(t *testing.T) {
	old := DeselectGrace
	DeselectGrace = 100 * time.Millisecond
	defer func() { DeselectGrace = old }()
	m, _ := newFakeManager(t, fake.Config{Clusters: 1, Pods: 500})
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	readyAndDiscovered(t, m, "prod-us")
	want := []string{"v1/pods", "v1/nodes", "v1/namespaces", "apps/v1/deployments", "apps/v1/statefulsets", "apps/v1/daemonsets", "apps/v1/replicasets",
		"batch/v1/jobs", "batch/v1/cronjobs", "v1/services", "discovery.k8s.io/v1/endpointslices", "networking.k8s.io/v1/ingresses", "v1/persistentvolumeclaims", "v1/events"}
	for _, g := range want {
		if w, ok := watchOf(m, "prod-us", g); !ok || !w.Tier0 || w.Lingering {
			t.Errorf("tier-0 watch %s missing or lingering: %+v", g, w)
		}
	}
	waitFor(t, "CRD invalidation watch", func() bool {
		_, ok := watchOf(m, "prod-us", "apiextensions.k8s.io/v1/customresourcedefinitions")
		return ok
	})
	_, _ = m.Select(nil)
	waitFor(t, "all watches stopped", func() bool { return !m.Running("prod-us") })
}

func TestRefCountLingerAndSharedWatch(t *testing.T) {
	m, _ := newFakeManager(t, fake.Config{Clusters: 2, Pods: 200})
	m.Linger = 300 * time.Millisecond
	_, _ = m.Select([]string{"prod-us", "prod-eu"})
	readyAndDiscovered(t, m, "prod-us")
	readyAndDiscovered(t, m, "prod-eu")
	const gvr = "v1/configmaps"
	mod, err := m.ResolveModule(context.Background(), gvr, nil)
	if err != nil || mod == nil || !mod.Table {
		t.Fatalf("resolve: %v %v", mod, err)
	}
	a, b := m.OpenScope(gvr, nil), m.OpenScope(gvr, []string{"prod-us"})
	waitFor(t, "watches on both clusters", func() bool {
		w1, ok1 := watchOf(m, "prod-us", gvr)
		w2, ok2 := watchOf(m, "prod-eu", gvr)
		return ok1 && ok2 && w1.Refs == 2 && w2.Refs == 1
	})
	if w, _ := watchOf(m, "prod-us", gvr); w.Refs != 2 || w.Lingering {
		t.Fatalf("two subscribers share one watch: %+v", w)
	}
	if w, _ := watchOf(m, "prod-eu", gvr); w.Refs != 1 {
		t.Fatalf("eu refs: %+v", w)
	}
	a.Close()
	b.Close()
	if w, ok := watchOf(m, "prod-us", gvr); !ok || w.Refs != 0 || !w.Lingering {
		t.Fatalf("after last release the watch lingers: %+v", w)
	}
	// Reacquire while lingering cancels the timer.
	c := m.OpenScope(gvr, []string{"prod-us"})
	time.Sleep(500 * time.Millisecond)
	if w, ok := watchOf(m, "prod-us", gvr); !ok || w.Refs != 1 {
		t.Fatalf("reacquired watch must survive the linger period: %+v", w)
	}
	c.Close()
	waitFor(t, "linger expiry", func() bool { _, ok := watchOf(m, "prod-us", gvr); return !ok })
	// Selecting another cluster after the scope opened adds its watch (scope = all selected clusters).
	_, _ = m.Select([]string{"prod-us"})
	d := m.OpenScope("v1/secrets", nil)
	defer d.Close()
	if _, ok := watchOf(m, "prod-eu", "v1/secrets"); ok {
		t.Fatal("deselected cluster must not get a watch")
	}
	_, _ = m.Select([]string{"prod-us", "prod-eu"})
	waitFor(t, "scope follows selection", func() bool { w, ok := watchOf(m, "prod-eu", "v1/secrets"); return ok && w.Refs == 1 })
}

func TestWatchBudgetEvictsLRULingeringOnly(t *testing.T) {
	m, _ := newFakeManager(t, fake.Config{Clusters: 1, Pods: 100})
	m.Linger = time.Hour
	m.Budget = len(ingest.Tier0Modules()) + 2 // room for two on-demand watches
	_, _ = m.Select([]string{"prod-us"})
	readyAndDiscovered(t, m, "prod-us")
	// invalidation watches count against the budget too
	for _, w := range m.WatchInfo("prod-us") {
		_ = w
	}
	m.Budget = len(m.WatchInfo("prod-us")) + 2
	open := func(gvr string) *Scope {
		s := m.OpenScope(gvr, nil)
		return s
	}
	cm := open("v1/configmaps")
	waitMod := func(gvr string) {
		if _, err := ingest.WaitModule(context.Background(), gvr); err != nil {
			t.Fatal(err)
		}
	}
	waitMod("v1/configmaps")
	sec := open("v1/secrets")
	waitMod("v1/secrets")
	cm.Close() // configmaps lingers (older release), secrets stays referenced
	time.Sleep(5 * time.Millisecond)
	sc := open("storage.k8s.io/v1/storageclasses") // budget reached: evicts the lingering configmaps
	waitMod("storage.k8s.io/v1/storageclasses")
	if _, ok := watchOf(m, "prod-us", "v1/configmaps"); ok {
		t.Fatal("LRU lingering watch should have been evicted")
	}
	for _, g := range []string{"v1/secrets", "storage.k8s.io/v1/storageclasses", "v1/pods"} {
		if _, ok := watchOf(m, "prod-us", g); !ok {
			t.Fatalf("%s must survive", g)
		}
	}
	// Everything left is referenced or tier-0: a further kind cannot start.
	n := len(m.WatchInfo("prod-us"))
	extra := open("batch/v1/jobs") // tier-0 already: no new watch
	extra.Close()
	_ = open("v1/configmaps") // needs a slot, none evictable
	if len(m.WatchInfo("prod-us")) != n {
		t.Fatalf("budget exceeded: %d watches, limit %d", len(m.WatchInfo("prod-us")), m.Budget)
	}
	if _, ok := watchOf(m, "prod-us", "v1/configmaps"); ok {
		t.Fatal("no watch may start beyond the budget")
	}
	sec.Close()
	sc.Close()
}

func TestKindsUnionAndSearch(t *testing.T) {
	m, _ := newFakeManager(t, fake.Config{Clusters: 2, Pods: 400})
	_, _ = m.Select([]string{"prod-us", "prod-eu"})
	readyAndDiscovered(t, m, "prod-us")
	readyAndDiscovered(t, m, "prod-eu")
	var dep, ks *protocol.Kind
	ks2 := m.Kinds()
	for i := range ks2 {
		switch ks2[i].GVR {
		case "apps/v1/deployments":
			dep = &ks2[i]
		case "kustomize.toolkit.fluxcd.io/v1/kustomizations":
			ks = &ks2[i]
		}
	}
	if dep == nil || !dep.Curated || !dep.Available || dep.Group != "apps" || dep.Version != "v1" || !slices.Equal(dep.Clusters, []string{"prod-eu", "prod-us"}) || dep.ShortNames[0] != "deploy" {
		t.Fatalf("deployments kind: %+v", dep)
	}
	if ks == nil || ks.Curated || ks.ShortNames[0] != "ks" || !ks.Namespaced {
		t.Fatalf("kustomizations kind: %+v", ks)
	}
	_, _ = m.Select([]string{"prod-us"})
	for _, k := range m.Kinds() {
		if !slices.Equal(k.Clusters, []string{"prod-us"}) {
			t.Fatalf("kind %s lists deselected cluster: %v", k.GVR, k.Clusters)
		}
	}
	waitFor(t, "deployments synced", func() bool { return len(m.Search("svc-7", 5)) > 0 })
	res := m.Search("svc-7", 200)
	if len(res) == 0 || len(m.Search("svc-7", 3)) != 3 {
		t.Fatalf("search: %d results", len(res))
	}
	seenPrefix := false
	for _, r := range res {
		if r.Cluster != "prod-us" || r.GVR == "" || r.Kind == "" {
			t.Fatalf("bad hit %+v", r)
		}
		hasPrefix := len(r.Name) >= 5 && r.Name[:5] == "svc-7"
		if seenPrefix && hasPrefix {
			continue
		}
		if !hasPrefix {
			seenPrefix = true // from here on only contains-matches may follow
		} else if seenPrefix {
			t.Fatalf("prefix match ranked after a contains match: %+v", r)
		}
	}
	if len(m.Search("   ", 5)) != 0 {
		t.Fatal("blank search must return nothing")
	}
}

func TestSettingsSelectionLinksAndGroups(t *testing.T) {
	m, _ := newFakeManager(t, fake.Config{Clusters: 3, Pods: 30, RBAC: true})
	set, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.SetSettings(set)
	if l := m.List(); len(l.Clusters) != 4 { // prod-us, prod-eu, staging, prod-eu-admin
		t.Fatalf("clusters: %+v", l.Clusters)
	}
	_, err = set.Patch(map[string]any{"links": map[string]any{"prod-eu": "prod-eu-admin"}, "groups": map[string]any{"prod": []any{"prod-us", "prod-eu"}}})
	if err != nil {
		t.Fatal(err)
	}
	l := m.List()
	var ids []string
	for _, c := range l.Clusters {
		ids = append(ids, c.ID)
		if c.ID == "prod-eu" && (c.WriteContext != "prod-eu-admin" || !slices.Equal(c.Groups, []string{"prod"})) {
			t.Fatalf("prod-eu: %+v", c)
		}
	}
	if slices.Contains(ids, "prod-eu-admin") {
		t.Fatalf("a linked write context must not be listed as a cluster: %v", ids)
	}
	if _, err := m.Select([]string{"staging", "prod-us"}); err != nil {
		t.Fatal(err)
	}
	if got := set.Get().Selection; !slices.Equal(got, []string{"prod-us", "staging"}) {
		t.Fatalf("remembered selection: %v", got)
	}
	// A new manager over the same settings reports the remembered selection.
	m2, _ := newFakeManager(t, fake.Config{Clusters: 3, Pods: 30, RBAC: true})
	m2.SetSettings(set)
	if got := m2.List().Selected; !slices.Equal(got, []string{"prod-us", "staging"}) {
		t.Fatalf("selected after restart: %v", got)
	}
}

func TestMaybeFree(t *testing.T) {
	called := 0
	old := freeOSMemory
	freeOSMemory = func() { called++ }
	defer func() { freeOSMemory = old }()
	if maybeFree(200<<20, 150<<20) || called != 0 {
		t.Fatal("50 MB released must not trigger FreeOSMemory")
	}
	if !maybeFree(400<<20, 100<<20) || called != 1 {
		t.Fatal("300 MB released must trigger FreeOSMemory")
	}
	if maybeFree(100<<20, 400<<20) {
		t.Fatal("growth must not trigger")
	}
}
