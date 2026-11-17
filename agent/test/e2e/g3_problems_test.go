//go:build e2e

package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/settings"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// e2eProblems starts the manager with a problems hub against the e2e cluster.
func e2eProblems(t *testing.T) (context.Context, *cluster.Manager, *problems.Hub, *view.Engine, string) {
	t.Helper()
	path := os.Getenv("V8S_E2E_KUBECONFIG")
	if path == "" {
		t.Skip("V8S_E2E_KUBECONFIG not set")
	}
	_ = os.Setenv("KUBE_FEATURE_WatchListClient", "true")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	src := &cluster.KubeSource{Loader: kube.Loader{Path: path}}
	id := "kind-v8s-e2e"
	found := false
	for _, c := range src.Clusters() {
		found = found || c.ID == id
	}
	if !found {
		t.Skipf("context %s not in %s", id, path)
	}
	eng := view.NewEngine()
	go eng.Run(ctx)
	set, _ := settings.Open("")
	hub := problems.NewHub(problems.HubConfig{Settings: set.Get, Patch: set.Patch,
		Fetch: func(ctx context.Context, cl, gvr, ns, name string) (*unstructured.Unstructured, error) {
			g, v, r := splitGVR(gvr)
			be, err := src.Backend(cl)
			if err != nil {
				return nil, err
			}
			ri := be.Dynamic().Resource(schema.GroupVersionResource{Group: g, Version: v, Resource: r})
			if ns != "" {
				return ri.Namespace(ns).Get(ctx, name, metav1.GetOptions{})
			}
			return ri.Get(ctx, name, metav1.GetOptions{})
		}})
	set.OnChange(hub.SettingsChanged)
	m := cluster.NewManager(ctx, src, eng, nil)
	m.SetSettings(set)
	m.SetProblems(hub)
	t.Cleanup(m.Close)
	if _, err := m.Select([]string{id}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cluster ready", 60*time.Second, func() bool {
		st := m.States().States
		return len(st) == 1 && st[0].Phase == protocol.PhaseReady
	})
	return ctx, m, hub, eng, id
}

func splitGVR(s string) (g, v, r string) {
	p := strings.Split(s, "/")
	if len(p) == 2 {
		return "", p[0], p[1]
	}
	return p[0], p[1], p[2]
}

func hasProblem(hub *problems.Hub, cl, class, subjectPrefix string) (problems.Problem, bool) {
	e := hub.Engine(cl)
	if e == nil {
		return problems.Problem{}, false
	}
	for _, p := range e.Problems() {
		if p.Class == class && strings.HasPrefix(p.Subject.Name, subjectPrefix) {
			return p, true
		}
	}
	return problems.Problem{}, false
}

// TestProblemsOnRealCluster asserts the broken fixtures of the e2e cluster show up as problems.
func TestProblemsOnRealCluster(t *testing.T) {
	_, _, hub, eng, id := e2eProblems(t)
	want := []struct{ class, subject string }{
		{"pod.crashloop", "payments-api"},
		{"pod.image-pull", "search-api"},
		{"pod.pending", "too-big"},
		{"pod.oomkilled", "memory-hog"},
		{"job.failed", "nightly-export"},
		{"ingress.no-backend", "search-public"},
		{"pvc.pending", "elastic-data-2"},
	}
	t.Cleanup(func() {
		if t.Failed() {
			for _, p := range hub.Engine(id).Problems() {
				t.Logf("problem %s %s/%s %q", p.Class, p.Subject.Kind, p.Subject.Name, p.Title)
			}
		}
	})
	waitFor(t, "problems", 60*time.Second, func() bool {
		for _, w := range want {
			if _, ok := hasProblem(hub, id, w.class, w.subject); !ok {
				return false
			}
		}
		return true
	})
	// the crash loop of the two payments-api pods is one workload row with the pods as evidence
	p, _ := hasProblem(hub, id, "pod.crashloop", "payments-api")
	if p.Subject.Kind != "Deployment" || p.Count != 2 || len(p.Evidence) != 2 {
		t.Fatalf("payments-api crash loop should roll up to its Deployment: %+v", p)
	}
	// evidence from the live object and the event cache
	ev, err := hub.Evidence(context.Background(), p.ID)
	if err != nil || len(ev.Containers) == 0 || ev.Containers[0].Reason == "" {
		t.Fatalf("evidence: %v %+v", err, ev)
	}
	t.Logf("evidence: %d conditions, %d containers, %d events, %d related", len(ev.Conditions), len(ev.Containers), len(ev.Events), len(ev.Related))
	// served through the view engine
	sub, err := eng.Subscribe(protocol.ViewQuery{GVR: protocol.ProblemsGVR, Filters: []protocol.Filter{{Col: "family", Op: "in", Value: []string{"pods", "jobs"}}}}, 1, protocol.Window{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	waitFor(t, "view rows", 10*time.Second, func() bool { return sub.Total() > 0 })
	if err := hub.Ack(p.ID, 0); err != nil {
		t.Fatal(err)
	}
	if q, _ := hub.Engine(id).Problem(p.ID); !q.Acked {
		t.Fatal("ack did not stick")
	}
	_ = hub.Unack(p.ID)
}
