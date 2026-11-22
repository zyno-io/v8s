package cluster

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/disco"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// swapDisc is a source whose backends answer Discover from a swappable list (the fake's list plus extra, minus drop).
type swapDisc struct {
	*fake.Source
	mu    sync.Mutex
	extra []disco.Resource
	drop  func(disco.Resource) bool
}

func (s *swapDisc) set(extra []disco.Resource) {
	s.mu.Lock()
	s.extra = extra
	s.mu.Unlock()
}

func (s *swapDisc) Backend(id string) (Backend, error) {
	b, err := s.Source.Backend(id)
	if err != nil {
		return nil, err
	}
	return swapDiscBackend{b, s}, nil
}

type swapDiscBackend struct {
	kube.Backend
	s *swapDisc
}

func (b swapDiscBackend) Discover(ctx context.Context) ([]disco.Resource, error) {
	res, err := b.Backend.Discover(ctx)
	if err != nil {
		return nil, err
	}
	b.s.mu.Lock()
	defer b.s.mu.Unlock()
	out := make([]disco.Resource, 0, len(res)+len(b.s.extra))
	for _, x := range res {
		if b.s.drop == nil || !b.s.drop(x) {
			out = append(out, x)
		}
	}
	return append(out, b.s.extra...), nil
}

func newSwapManager(t *testing.T, s *swapDisc) *Manager {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := NewManager(ctx, s, eng, nil)
	t.Cleanup(m.Close)
	return m
}

func kindsGen(m *Manager) uint64 {
	if st := m.States().States; len(st) > 0 {
		return st[0].Kinds
	}
	return 0
}

func hasKind(m *Manager, gvr string) bool {
	return slices.ContainsFunc(m.Kinds(), func(k protocol.Kind) bool { return k.GVR == gvr })
}

func TestDiscoveryGenBumpsOnlyOnChange(t *testing.T) {
	s := &swapDisc{Source: fake.NewSource(fake.Config{Clusters: 1, Pods: 50})}
	m := newSwapManager(t, s)
	m.DiscoveryFollowUp = 50 * time.Millisecond
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Kinds==1", func() bool { return kindsGen(m) == 1 })
	waitFor(t, "CRD watch synced", func() bool {
		m.mu.Lock()
		r := m.runners["prod-us"]
		m.mu.Unlock()
		r.mu.Lock()
		defer r.mu.Unlock()
		w := r.watches[crdGVR]
		return w != nil && w.invSynced.Load()
	})

	m.mu.Lock()
	r := m.runners["prod-us"]
	m.mu.Unlock()

	// Same set again: refreshed (the follow-up fires), gen stays.
	r.invalidate()
	time.Sleep(1500 * time.Millisecond)
	if g := kindsGen(m); g != 1 {
		t.Fatalf("identical rediscovery bumped the gen: %d", g)
	}

	s.set([]disco.Resource{{Group: "example.io", Version: "v1", Resource: "widgets", Kind: "Widget", Namespaced: true, Verbs: []string{"list", "watch", "get"}}})
	if hasKind(m, "example.io/v1/widgets") {
		t.Fatal("widgets known before discovery refreshed")
	}
	r.invalidate()
	waitFor(t, "Kinds==2", func() bool { return kindsGen(m) == 2 })
	if !hasKind(m, "example.io/v1/widgets") {
		t.Fatal("new CRD missing from Kinds()")
	}
}

func TestDiscoveryFallbackPollWithoutCRDWatch(t *testing.T) {
	s := &swapDisc{Source: fake.NewSource(fake.Config{Clusters: 1, Pods: 50}),
		drop: func(r disco.Resource) bool { return r.Resource == "customresourcedefinitions" }}
	m := newSwapManager(t, s)
	m.DiscoveryEvery = time.Hour
	m.DiscoveryFallbackEvery = 50 * time.Millisecond
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Kinds==1", func() bool { return kindsGen(m) == 1 })
	s.set([]disco.Resource{{Group: "example.io", Version: "v1", Resource: "widgets", Kind: "Widget", Verbs: []string{"list", "watch"}}})
	waitFor(t, "Kinds==2 via fallback poll", func() bool { return kindsGen(m) == 2 })
	if !hasKind(m, "example.io/v1/widgets") {
		t.Fatal("new CRD missing from Kinds()")
	}
}

// The follow-up refresh alone (no second kick) picks up a resource that appeared after the debounced refresh.
func TestDiscoveryFollowUpCatchesLaggedCRD(t *testing.T) {
	s := &swapDisc{Source: fake.NewSource(fake.Config{Clusters: 1, Pods: 50})}
	m := newSwapManager(t, s)
	m.DiscoveryFollowUp = 1500 * time.Millisecond
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Kinds==1", func() bool { return kindsGen(m) == 1 })
	m.mu.Lock()
	r := m.runners["prod-us"]
	m.mu.Unlock()
	waitFor(t, "CRD watch synced", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		w := r.watches[crdGVR]
		return w != nil && w.invSynced.Load()
	})
	r.invalidate()
	time.Sleep(1300 * time.Millisecond) // past the 1 s debounce refresh, before the follow-up
	if g := kindsGen(m); g != 1 {
		t.Fatalf("gen %d", g)
	}
	s.set([]disco.Resource{{Group: "example.io", Version: "v1", Resource: "widgets", Kind: "Widget", Namespaced: true, Verbs: []string{"list", "watch"}}})
	waitFor(t, "Kinds==2 via follow-up", func() bool { return kindsGen(m) == 2 })
	if !hasKind(m, "example.io/v1/widgets") {
		t.Fatal("kinds.list misses the lagged CRD")
	}
	// the CRD watch is usable, so the period is back to the normal one
	if p := r.discoveryPeriod(); p != m.DiscoveryEvery {
		t.Fatalf("period %v", p)
	}
}
