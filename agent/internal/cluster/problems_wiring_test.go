package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
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

func TestUnavailableReason(t *testing.T) {
	if r := unavailableReason("apps/v1/statefulsets", forbiddenAPIErr()); r != "no permission to list statefulsets" {
		t.Fatalf("%q", r)
	}
	if r := unavailableReason("apps/v1/statefulsets", context.DeadlineExceeded); r != "" {
		t.Fatalf("a timeout is not 'unavailable': %q", r)
	}
}
