package cluster

import (
	"slices"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func loadingOf(m *Manager, id string) []string { return stateOf(m, id).Loading }

// A kind an open view holds is "loading" from the moment it is opened until its first list has landed and settled;
// background scopes never count, a refused list stops counting, and the clients are signaled when it clears.
func TestLoadingHeldUnsyncedKinds(t *testing.T) {
	m, _ := newElevateManagerCfg(t, fake.Config{Clusters: 2, Pods: 100, RBAC: true, ListDelay: 400 * time.Millisecond})
	const cms, secrets = "v1/configmaps", "v1/secrets"
	ch, unlisten := m.Listen()
	defer unlisten()

	bg := m.OpenBackgroundScope(cms, []string{"prod-us"})
	defer bg.Close()
	sc := m.OpenScope(cms, []string{"prod-eu"})
	defer sc.Close()
	if got := loadingOf(m, "prod-eu"); !slices.Equal(got, []string{cms}) {
		t.Fatalf("right after OpenScope: loading %v", got)
	}
	if got := loadingOf(m, "prod-us"); len(got) != 0 {
		t.Fatalf("a background scope is loading: %v", got)
	}
	sec := m.OpenScope(secrets, []string{"prod-eu"})
	defer sec.Close()
	// The fake refuses the read context's secrets list at once, so only configmaps is certain to still be loading.
	if got := loadingOf(m, "prod-eu"); !slices.Contains(got, cms) {
		t.Fatalf("loading %v, want configmaps", got)
	}

	// Drain, then require a signal that arrives with the state change, not just polling.
	for len(ch) > 0 {
		<-ch
	}
	deadline := time.After(10 * time.Second)
	for len(loadingOf(m, "prod-eu")) > 0 { // only a signal may change what we see: no polling
		select {
		case <-ch:
		case <-deadline:
			t.Fatalf("still loading: %v (synced %v)", loadingOf(m, "prod-eu"), m.KindSynced("prod-eu", cms))
		}
	}
	if !m.KindSynced("prod-eu", cms) {
		t.Fatal("loading cleared before configmaps synced")
	}
	// The RBAC read context cannot list secrets: refused, so it stopped counting as loading (checked above: the loop ended).
	r := runnerOf(m, "prod-eu")
	r.mu.Lock()
	w := r.watches[secrets]
	r.mu.Unlock()
	if w == nil || !apierrors.IsForbidden(w.lastErr()) {
		t.Fatalf("secrets watch %v: want forbidden", w)
	}
	if got := loadingOf(m, "prod-eu"); len(got) != 0 {
		t.Fatalf("loading after settle: %v", got)
	}
	// Reopening a settled kind does not flash loading again.
	sc2 := m.OpenScope(cms, []string{"prod-eu"})
	defer sc2.Close()
	if got := loadingOf(m, "prod-eu"); len(got) != 0 {
		t.Fatalf("settled kind loading again on reopen: %v", got)
	}
}

// Until a cluster's first discovery, a kind an open view asks for that cannot be placed yet counts as loading;
// background and synthetic scopes do not.
func TestLoadingDiscoveryPending(t *testing.T) {
	m, _ := newElevateManagerRaw(t, fake.Config{Clusters: 2, Pods: 100, RBAC: true, DiscoveryDelay: 800 * time.Millisecond})
	const cms = "v1/configmaps"
	sc := m.OpenScope(cms, []string{"prod-eu"})
	defer sc.Close()
	pr := m.OpenScope(problems.GVR, nil)
	defer pr.Close()
	bg := m.OpenBackgroundScope("v1/secrets", []string{"prod-eu"})
	defer bg.Close()
	if r := runnerOf(m, "prod-eu"); r.disc.Load() != nil {
		t.Skip("discovery already done")
	}
	if got := loadingOf(m, "prod-eu"); !slices.Equal(got, []string{cms}) {
		t.Fatalf("discovery pending: loading %v, want just configmaps", got)
	}
	if got := loadingOf(m, "prod-us"); slices.Contains(got, problems.GVR) || slices.Contains(got, "v1/secrets") {
		t.Fatalf("prod-us loading %v", got)
	}
	waitFor(t, "loading cleared once discovery and the list landed", func() bool {
		return len(loadingOf(m, "prod-eu")) == 0 && m.KindSynced("prod-eu", cms) && stateOf(m, "prod-eu").Phase == protocol.PhaseReady
	})
}
