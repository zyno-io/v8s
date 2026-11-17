package cluster

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

// A kind that a user opened on demand becomes baseline when a problem class needs it: it must not linger out
// from under the detector (which would leave the class with stale input).
func TestOnDemandWatchPromotedToBaseline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	set := protocol.Settings{ProblemPrefs: map[string]any{}, Acks: map[string]any{}}
	hub := problems.NewHub(problems.HubConfig{Settings: func() protocol.Settings { return set }})
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := NewManager(ctx, fake.NewSource(fake.Config{Clusters: 1, Pods: 50}), eng, nil)
	defer m.Close()
	m.SetProblems(hub)
	m.Linger = 100 * time.Millisecond
	_, _ = m.Select([]string{"prod-us"})
	readyAndDiscovered(t, m, "prod-us")
	const hpa = "autoscaling/v2/horizontalpodautoscalers"
	if _, err := m.ResolveModule(ctx, hpa, nil); err != nil {
		t.Fatal(err)
	}
	sc := m.OpenScope(hpa, nil)
	waitFor(t, "on-demand hpa watch", func() bool { w, ok := watchOf(m, "prod-us", hpa); return ok && !w.Tier0 })
	set.ProblemPrefs = map[string]any{"classes": map[string]any{"hpa.unable": true}}
	hub.SettingsChanged(set)
	m.reconcileAll()
	if w, _ := watchOf(m, "prod-us", hpa); !w.Tier0 {
		t.Fatalf("watch not promoted to baseline: %+v", w)
	}
	sc.Close()
	time.Sleep(400 * time.Millisecond) // well past the linger
	if _, ok := watchOf(m, "prod-us", hpa); !ok {
		t.Fatal("the baseline watch lingered out after the last user left")
	}
}

// A kind the read context may not list is recorded as unavailable, and a class with other inputs keeps working.
type denyStatefulSets struct{ kube.Backend }

func (b denyStatefulSets) API(gvr string, m *ingest.Module) (ingest.API, error) {
	if gvr == "apps/v1/statefulsets" {
		return forbiddenAPI{}, nil
	}
	return b.Backend.API(gvr, m)
}

type denyStsSource struct{ *fake.Source }

func (s denyStsSource) Backend(id string) (Backend, error) {
	b, err := s.Source.Backend(id)
	if err != nil {
		return nil, err
	}
	return denyStatefulSets{b}, nil
}

func TestForbiddenBaselineKindIsReportedUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := problems.NewHub(problems.HubConfig{Settings: func() protocol.Settings {
		return protocol.Settings{ProblemPrefs: map[string]any{}, Acks: map[string]any{}}
	}})
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := NewManager(ctx, denyStsSource{fake.NewSource(fake.Config{Clusters: 1, Pods: 50})}, eng, nil)
	defer m.Close()
	m.SetProblems(hub)
	_, _ = m.Select([]string{"prod-us"})
	waitFor(t, "statefulsets reported unavailable", func() bool {
		for _, u := range hub.Classes().Unavailable {
			if u.Cluster == "prod-us" && u.Kind == "apps/statefulsets" {
				return true
			}
		}
		return false
	})
}

// flakyPods is forbidden until allow is set, then the real pods API.
type flakyPodsBackend struct {
	kube.Backend
	allow *atomic.Bool
}

type flakyAPI struct {
	ingest.API
	allow *atomic.Bool
}

func (a flakyAPI) List(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
	if !a.allow.Load() {
		return forbiddenAPI{}.List(ctx, o)
	}
	return a.API.List(ctx, o)
}

func (a flakyAPI) Watch(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
	if !a.allow.Load() {
		return forbiddenAPI{}.Watch(ctx, o)
	}
	return a.API.Watch(ctx, o)
}

func (b flakyPodsBackend) API(gvr string, m *ingest.Module) (ingest.API, error) {
	a, err := b.Backend.API(gvr, m)
	if err != nil || gvr != ingest.PodsGVR {
		return a, err
	}
	return flakyAPI{a, b.allow}, nil
}

type flakyPodsSource struct {
	*fake.Source
	allow *atomic.Bool
}

func (s flakyPodsSource) Backend(id string) (Backend, error) {
	b, err := s.Source.Backend(id)
	if err != nil {
		return nil, err
	}
	return flakyPodsBackend{b, s.allow}, nil
}

// The pods watch recovering clears its unavailable warning (it has its own OnOK hook).
func TestPodsUnavailableClearsWhenWatchRecovers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := problems.NewHub(problems.HubConfig{Settings: func() protocol.Settings {
		return protocol.Settings{ProblemPrefs: map[string]any{}, Acks: map[string]any{}}
	}})
	eng := view.NewEngine()
	go eng.Run(ctx)
	allow := &atomic.Bool{}
	m := NewManager(ctx, flakyPodsSource{fake.NewSource(fake.Config{Clusters: 1, Pods: 5}), allow}, eng, nil)
	defer m.Close()
	m.SetProblems(hub)
	_, _ = m.Select([]string{"prod-us"})
	has := func() bool {
		for _, u := range hub.Classes().Unavailable {
			if u.Cluster == "prod-us" && u.Kind == "pods" {
				return true
			}
		}
		return false
	}
	waitFor(t, "pods reported unavailable", has)
	allow.Store(true)
	waitFor(t, "pods warning cleared", func() bool { return !has() })
}

func TestUnavailableReason(t *testing.T) {
	if r := unavailableReason("apps/v1/statefulsets", forbiddenAPIErr()); r != "no permission to list statefulsets" {
		t.Fatalf("%q", r)
	}
	if r := unavailableReason("apps/v1/statefulsets", context.DeadlineExceeded); r != "" {
		t.Fatalf("a timeout is not 'unavailable': %q", r)
	}
}
