package cluster

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

type evSummary struct{ typ, reason, object, source string }

// objectEventRows lists the rows of the cluster's shared object-events store that are about kind/name.
func objectEventRows(m *Manager, cl, object string) []evSummary {
	m.mu.Lock()
	r := m.runners[cl]
	m.mu.Unlock()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	st := r.oev
	r.mu.Unlock()
	if st == nil {
		return nil
	}
	mod := ingest.Lookup(ingest.ObjectEventsGVR)
	var out []evSummary
	st.Scan(func(row *ingest.Row) bool {
		s := evSummary{
			typ: row.Cell(mod.Col("type").Idx).S.Value(), reason: row.Cell(mod.Col("reason").Idx).S.Value(),
			object: row.Cell(mod.Col("object").Idx).S.Value(), source: row.Cell(mod.Col("source").Idx).S.Value(),
		}
		if s.object == object {
			out = append(out, s)
		}
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].reason < out[j].reason })
	return out
}

func reasons(rows []evSummary) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.reason)
	}
	return out
}

func startEventsManager(t *testing.T) *Manager {
	t.Helper()
	m, _ := newFakeManager(t, fake.Config{Clusters: 1, Pods: 3000})
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	readyAndDiscovered(t, m, "prod-us")
	return m
}

func watchKeyInfo(m *Manager, key string) (WatchInfo, bool) { return watchOf(m, "prod-us", key) }

// failingPod finds a pod with a Warning event other than FailedScheduling (which never gets a Scheduled event).
func failingPod(t *testing.T, m *Manager) protocol.ObjectRef {
	t.Helper()
	mod := ingest.Lookup(ingest.EventsGVR)
	var ref protocol.ObjectRef
	waitFor(t, "tier-0 events", func() bool {
		m.ScanKind("prod-us", ingest.EventsGVR, func(r *ingest.Row) bool {
			obj := r.Cell(mod.Col("object").Idx).S.Value()
			if name, ok := strings.CutPrefix(obj, "Pod/"); ok && r.Cell(mod.Col("reason").Idx).S.Value() != "FailedScheduling" {
				ref = protocol.ObjectRef{Cluster: "prod-us", GVR: ingest.PodsGVR, Namespace: r.NS().Value(), Name: name}
				return false
			}
			return true
		})
		return ref.Name != ""
	})
	return ref
}

func TestObjectEventsPodShowsNormalAndWarningWhileTier0StaysWarningOnly(t *testing.T) {
	m := startEventsManager(t)
	ref := failingPod(t, m)
	mod := ingest.Lookup(ingest.EventsGVR)
	m.ScanKind("prod-us", ingest.EventsGVR, func(r *ingest.Row) bool {
		if typ := r.Cell(mod.Col("type").Idx).S.Value(); typ != "Warning" {
			t.Fatalf("tier-0 events hold a %s event", typ)
		}
		return true
	})
	s, oe, err := m.OpenObjectEvents(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows := objectEventRows(m, "prod-us", "Pod/"+ref.Name)
	var normal, warning bool
	for _, r := range rows {
		normal = normal || (r.typ == "Normal" && r.reason == "Scheduled" && r.source == "default-scheduler")
		warning = warning || r.typ == "Warning"
	}
	if !normal || !warning {
		t.Fatalf("events of %s: %+v (key %s)", ref.Name, rows, oe.Key())
	}
	m.ScanKind("prod-us", ingest.EventsGVR, func(r *ingest.Row) bool { // still Warning-only: the rows never reach tier-0
		if typ := r.Cell(mod.Col("type").Idx).S.Value(); typ != "Warning" {
			t.Fatalf("tier-0 events now hold a %s event", typ)
		}
		return true
	})
}

func TestObjectEventsNodeListsFromDefault(t *testing.T) {
	m := startEventsManager(t)
	s, oe, err := m.OpenObjectEvents(context.Background(), protocol.ObjectRef{Cluster: "prod-us", GVR: "v1/nodes", Name: "node-5"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if oe.EventNS != "default" || oe.InvolvedNS != "" {
		t.Errorf("namespaces %q / %q", oe.EventNS, oe.InvolvedNS)
	}
	if got := reasons(objectEventRows(m, "prod-us", "Node/node-5")); !slices.Equal(got, []string{"NodeNotReady", "RegisteredNode", "Starting"}) {
		t.Fatalf("node events: %v", got)
	}
	if got := reasons(objectEventRows(m, "prod-us", "Node/node-6")); len(got) != 0 {
		t.Fatalf("another node's events were listed: %v", got)
	}
}

func TestObjectEventsRefsAndLinger(t *testing.T) {
	m := startEventsManager(t)
	m.EventsLinger = 300 * time.Millisecond
	ref := protocol.ObjectRef{Cluster: "prod-us", GVR: "v1/nodes", Name: "node-7"}
	s1, oe, err := m.OpenObjectEvents(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	s2, _, err := m.OpenObjectEvents(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := watchKeyInfo(m, oe.Key()); !ok || w.Refs != 2 || w.Tier0 {
		t.Fatalf("two opens: %+v %v", w, ok)
	}
	s1.Close()
	if w, _ := watchKeyInfo(m, oe.Key()); w.Refs != 1 || w.Lingering {
		t.Fatalf("after one close: %+v", w)
	}
	s2.Close()
	if w, ok := watchKeyInfo(m, oe.Key()); !ok || w.Refs != 0 || !w.Lingering {
		t.Fatalf("after both closed it should linger: %+v %v", w, ok)
	}
	if len(objectEventRows(m, "prod-us", "Node/node-7")) == 0 {
		t.Fatal("rows vanished while lingering")
	}
	waitFor(t, "linger expiry", func() bool { _, ok := watchKeyInfo(m, oe.Key()); return !ok })
	if got := objectEventRows(m, "prod-us", "Node/node-7"); len(got) != 0 {
		t.Fatalf("rows left after expiry: %v", got)
	}
}

// Guards invariant 3: a watch that expires must never delete the rows of the watch that replaced it.
func TestObjectEventsCloseReopenLoopKeepsRows(t *testing.T) {
	m := startEventsManager(t)
	m.EventsLinger = time.Millisecond
	ref := protocol.ObjectRef{Cluster: "prod-us", GVR: "v1/nodes", Name: "node-8"}
	for i := 0; i < 60; i++ {
		s, _, err := m.OpenObjectEvents(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		if got := objectEventRows(m, "prod-us", "Node/node-8"); len(got) != 2 {
			t.Fatalf("iteration %d: %v", i, got)
		}
		s.Close()
		time.Sleep(time.Duration(i%4) * time.Millisecond)
	}
}

func TestObjectEventsBudget(t *testing.T) {
	m := startEventsManager(t)
	m.Budget = len(m.WatchInfo("prod-us")) + 1
	open := func(node string) (*Scope, *ObjectEvents, error) {
		return m.OpenObjectEvents(context.Background(), protocol.ObjectRef{Cluster: "prod-us", GVR: "v1/nodes", Name: node})
	}
	a, oa, err := open("node-1")
	if err != nil {
		t.Fatal(err)
	}
	a.Close() // lingering: evictable
	b, ob, err := open("node-2")
	if err != nil {
		t.Fatalf("a lingering watch should have been evicted: %v", err)
	}
	defer b.Close()
	if _, ok := watchKeyInfo(m, oa.Key()); ok {
		t.Error("the lingering watch was not evicted")
	}
	if _, ok := watchKeyInfo(m, ob.Key()); !ok {
		t.Error("the new watch is missing")
	}
	if _, _, err := open("node-3"); !errors.Is(err, ErrBudget) {
		t.Fatalf("want ErrBudget with nothing evictable, got %v", err)
	}
}

type evForbiddenAPI struct{ forbiddenAPI }

func (a evForbiddenAPI) InNamespace(string) ingest.API { return a }

type evBlockAPI struct{}

func (evBlockAPI) List(ctx context.Context, _ metav1.ListOptions) (runtime.Object, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (evBlockAPI) Watch(ctx context.Context, _ metav1.ListOptions) (watch.Interface, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (a evBlockAPI) InNamespace(string) ingest.API { return a }

type evBackend struct {
	kube.Backend
	api ingest.API
}

func (b evBackend) API(gvr string, m *ingest.Module) (ingest.API, error) {
	if gvr == ingest.EventsGVR {
		return b.api, nil
	}
	return b.Backend.API(gvr, m)
}

type evSource struct {
	*fake.Source
	api ingest.API
}

func (s evSource) Backend(id string) (Backend, error) {
	b, err := s.Source.Backend(id)
	if err != nil {
		return nil, err
	}
	return evBackend{b, s.api}, nil
}

func startWithEventsAPI(t *testing.T, api ingest.API) *Manager {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := NewManager(ctx, evSource{fake.NewSource(fake.Config{Clusters: 1, Pods: 100}), api}, eng, nil)
	t.Cleanup(m.Close)
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	readyAndDiscovered(t, m, "prod-us")
	return m
}

func TestObjectEventsForbiddenFailsFastAndIsDiscarded(t *testing.T) {
	m := startWithEventsAPI(t, evForbiddenAPI{})
	start := time.Now()
	_, _, err := m.OpenObjectEvents(context.Background(), protocol.ObjectRef{Cluster: "prod-us", GVR: "v1/nodes", Name: "node-1"})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("want forbidden, got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	for _, w := range m.WatchInfo("prod-us") {
		if strings.HasPrefix(w.GVR, ingest.ObjectEventsGVR) {
			t.Fatalf("the failed watch was kept: %+v", w)
		}
	}
}

func TestObjectEventsTimeout(t *testing.T) {
	old := ObjectEventsWait
	ObjectEventsWait = 300 * time.Millisecond
	defer func() { ObjectEventsWait = old }()
	m := startWithEventsAPI(t, evBlockAPI{})
	start := time.Now()
	_, _, err := m.OpenObjectEvents(context.Background(), protocol.ObjectRef{Cluster: "prod-us", GVR: "v1/nodes", Name: "node-1"})
	if !errors.Is(err, ErrEventsTimeout) {
		t.Fatalf("want ErrEventsTimeout, got %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
	for _, w := range m.WatchInfo("prod-us") {
		if strings.HasPrefix(w.GVR, ingest.ObjectEventsGVR) {
			t.Fatalf("the timed-out watch was kept: %+v", w)
		}
	}
}

func TestObjectEventsRejections(t *testing.T) {
	m := startEventsManager(t)
	var bq *view.BadQueryError
	if _, _, err := m.OpenObjectEvents(context.Background(), protocol.ObjectRef{Cluster: "nope", GVR: "v1/nodes", Name: "n"}); !errors.Is(err, ErrClusterNotSelected) {
		t.Errorf("unselected cluster: %v", err)
	}
	if _, _, err := m.OpenObjectEvents(context.Background(), protocol.ObjectRef{Cluster: "prod-us", GVR: protocol.HelmGVR, Namespace: "ns", Name: "r"}); !errors.As(err, &bq) {
		t.Errorf("helm release: %v", err)
	}
	if _, _, err := m.OpenObjectEvents(context.Background(), protocol.ObjectRef{Cluster: "prod-us", GVR: "v1/nodes"}); !errors.As(err, &bq) {
		t.Errorf("missing name: %v", err)
	}
	if _, _, err := m.OpenObjectEvents(context.Background(), protocol.ObjectRef{Cluster: "prod-us", GVR: ingest.PodsGVR, Name: "p"}); !errors.As(err, &bq) {
		t.Errorf("namespaced kind without a namespace: %v", err)
	}
	if _, _, err := m.OpenObjectEvents(context.Background(), protocol.ObjectRef{Cluster: "prod-us", GVR: "nope.example.com/v1/things", Name: "p"}); !apierrors.IsNotFound(err) {
		t.Errorf("unknown kind: %v", err)
	}
}
