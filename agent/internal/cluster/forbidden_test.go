package cluster

import (
	"context"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/view"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

// toggleDeny refuses List and Watch of one kind while on is set, counting the calls.
type toggleDeny struct {
	gvr    string
	on     atomic.Bool
	unauth atomic.Bool // answer 401 instead (a credential that is being refreshed)
	calls  atomic.Int64
}

type toggleAPI struct {
	ingest.API
	d *toggleDeny
}

func (a toggleAPI) List(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err // a call on a cancelled (parked) run is not a request to the server
	}
	a.d.calls.Add(1)
	if a.d.unauth.Load() {
		return nil, apierrors.NewUnauthorized("token expired")
	}
	if a.d.on.Load() {
		return forbiddenAPI{}.List(ctx, o)
	}
	return a.API.List(ctx, o)
}

func (a toggleAPI) Watch(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.d.calls.Add(1)
	if a.d.unauth.Load() {
		return nil, apierrors.NewUnauthorized("token expired")
	}
	if a.d.on.Load() {
		return forbiddenAPI{}.Watch(ctx, o)
	}
	return a.API.Watch(ctx, o)
}

type toggleBackend struct {
	kube.Backend
	d *toggleDeny
}

func (b toggleBackend) API(gvr string, m *ingest.Module) (ingest.API, error) {
	api, err := b.Backend.API(gvr, m)
	if err != nil || gvr != b.d.gvr {
		return api, err
	}
	return toggleAPI{api, b.d}, nil
}

type toggleSource struct {
	*fake.Source
	d *toggleDeny
}

func (s toggleSource) Backend(id string) (Backend, error) {
	b, err := s.Source.Backend(id)
	if err != nil {
		return nil, err
	}
	return toggleBackend{b, s.d}, nil
}

const denyGVR = "apps/v1/daemonsets" // a tier-0 kind

func newDenyManager(t *testing.T, retry time.Duration) (*Manager, *toggleDeny) {
	t.Helper()
	m, d := newToggleManager(t, retry, func(d *toggleDeny) { d.on.Store(true) })
	readyAndDiscovered(t, m, "prod-us")
	return m, d
}

// newToggleManager selects prod-us with one kind's API wrapped by a toggleDeny; it does not wait for readiness.
func newToggleManager(t *testing.T, retry time.Duration, setup func(*toggleDeny)) (*Manager, *toggleDeny) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := view.NewEngine()
	go eng.Run(ctx)
	d := &toggleDeny{gvr: denyGVR}
	setup(d)
	m := NewManager(ctx, toggleSource{fake.NewSource(fake.Config{Clusters: 1, Pods: 50}), d}, eng, nil)
	m.ForbiddenRetry = retry
	t.Cleanup(m.Close)
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	return m, d
}

func parked(m *Manager) bool {
	w, ok := watchOf(m, "prod-us", denyGVR)
	return ok && w.Parked
}

func (m *Manager) runnerForTest(id string) *runner {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runners[id]
}

func (r *runner) watchErr(gvr string) error {
	r.mu.Lock()
	w := r.watches[gvr]
	r.mu.Unlock()
	if w == nil {
		return nil
	}
	return w.lastErr()
}

func TestForbiddenBaselineWatchStopsRetrying(t *testing.T) {
	m, d := newDenyManager(t, time.Hour)
	waitFor(t, "baseline watch parked", func() bool { return parked(m) })
	n := d.calls.Load()
	if n == 0 || n > 2 {
		t.Fatalf("want one list (plus at most a watch-list probe), got %d calls", n)
	}
	// client-go's default backoff would have retried within 800 ms and again after ~1.6 s.
	time.Sleep(2500 * time.Millisecond)
	if got := d.calls.Load(); got != n {
		t.Fatalf("parked watch kept hitting the API: %d -> %d calls", n, got)
	}
	w, _ := watchOf(m, "prod-us", denyGVR)
	if !w.Tier0 || w.Refs != 0 {
		t.Fatalf("watch should stay registered as baseline: %+v", w)
	}
	if m.runnerForTest("prod-us").watchErr(denyGVR) == nil {
		t.Fatal("parked watch lost its error")
	}
	if m.KindSynced("prod-us", denyGVR) || m.KindSynced("prod-us", "example.com/v1/unwatched") || m.KindSynced("nope", denyGVR) {
		t.Fatal("a parked, unwatched or unknown-cluster kind is not synced")
	}
}

func TestForbiddenBaselineWatchRetriesAndRecovers(t *testing.T) {
	m, d := newDenyManager(t, 300*time.Millisecond)
	waitFor(t, "parked", func() bool { return parked(m) })
	first := d.calls.Load()
	waitFor(t, "retried after ForbiddenRetry", func() bool { return d.calls.Load() > first })
	d.on.Store(false)
	waitFor(t, "recovered", func() bool {
		w, ok := watchOf(m, "prod-us", denyGVR)
		return ok && !w.Parked && m.runnerForTest("prod-us").watchErr(denyGVR) == nil
	})
	if !m.ScanKind("prod-us", denyGVR, func(*ingest.Row) bool { return false }) {
		t.Fatal("kind not watched after recovery")
	}
	waitFor(t, "synced after recovery", func() bool { return m.KindSynced("prod-us", denyGVR) })
}

func TestForbiddenParkedWatchRetriedWhenViewAcquires(t *testing.T) {
	m, d := newDenyManager(t, time.Hour)
	waitFor(t, "parked", func() bool { return parked(m) })
	d.on.Store(false)
	rel, err := m.runnerForTest("prod-us").acquire(denyGVR)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	waitFor(t, "recovered through acquire", func() bool {
		w, _ := watchOf(m, "prod-us", denyGVR)
		return !w.Parked && m.runnerForTest("prod-us").watchErr(denyGVR) == nil
	})
}

func TestForbiddenViewHeldWatchKeepsRetrying(t *testing.T) {
	m, d := newDenyManager(t, time.Hour)
	// Park first, then let a view take it: the view's reference must keep it retrying (no re-park).
	waitFor(t, "parked", func() bool { return parked(m) })
	rel, err := m.runnerForTest("prod-us").acquire(denyGVR)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	n := d.calls.Load()
	waitFor(t, "retries while held", func() bool { return d.calls.Load() >= n+3 })
	if parked(m) {
		t.Fatal("a watch held by a view must not be parked")
	}
}

// A 401 is often transient (an exec credential being refreshed) and cluster-wide: it must not park a watch, which
// would freeze the kind for ForbiddenRetry after the credential recovers.
func TestUnauthorizedDoesNotPark(t *testing.T) {
	m, d := newToggleManager(t, time.Hour, func(d *toggleDeny) { d.unauth.Store(true) })
	waitFor(t, "the 401 seen", func() bool { return d.calls.Load() > 0 })
	time.Sleep(300 * time.Millisecond)
	if parked(m) {
		t.Fatal("a 401 parked the watch")
	}
	d.unauth.Store(false)
	waitFor(t, "the watch recovers on its own", func() bool { return m.runnerForTest("prod-us").watchErr(denyGVR) == nil && d.calls.Load() > 1 })
	if parked(m) {
		t.Fatal("parked after recovery")
	}
}
