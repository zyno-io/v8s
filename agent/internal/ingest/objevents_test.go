package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type lwRecAPI struct {
	mu   sync.Mutex
	list []metav1.ListOptions
}

func (a *lwRecAPI) List(_ context.Context, o metav1.ListOptions) (runtime.Object, error) {
	a.mu.Lock()
	a.list = append(a.list, o)
	a.mu.Unlock()
	return &corev1.EventList{}, nil
}
func (a *lwRecAPI) Watch(context.Context, metav1.ListOptions) (watch.Interface, error) {
	return watch.NewEmptyWatch(), nil
}

func TestListerWatcherTweakOverridesModuleTweak(t *testing.T) {
	api := &lwRecAPI{}
	lw := &ListerWatcher{API: api, Mod: Lookup(EventsGVR)}
	if _, err := lw.ListWithContext(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := api.list[0].FieldSelector; got != "type=Warning" {
		t.Errorf("module tweak: selector %q", got)
	}
	lw = &ListerWatcher{API: api, Mod: Lookup(EventsGVR), Tweak: func(o *metav1.ListOptions) { o.FieldSelector = "involvedObject.name=x" }}
	if _, err := lw.ListWithContext(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := api.list[1].FieldSelector; got != "involvedObject.name=x" {
		t.Errorf("override: selector %q", got)
	}
}

func TestObjectEventsModule(t *testing.T) {
	ev, oe := Lookup(EventsGVR), Lookup(ObjectEventsGVR)
	if ev == nil || oe == nil {
		t.Fatal("modules not registered")
	}
	if c := ev.Col("lastSeen"); c == nil || c.Idx != eventLastSeenCell {
		t.Errorf("eventLastSeenCell %d, lastSeen col %+v", eventLastSeenCell, c)
	}
	keys := func(m *Module) []string {
		var k []string
		for _, c := range m.Cols {
			k = append(k, c.Key)
		}
		return k
	}
	if !slices.Equal(keys(ev), keys(oe)) {
		t.Errorf("columns differ: %v vs %v", keys(ev), keys(oe))
	}
	if len(oe.DefaultSort) != 1 || oe.DefaultSort[0].Col != "lastSeen" || !oe.DefaultSort[0].Desc {
		t.Errorf("default sort %+v", oe.DefaultSort)
	}
	if oe.Curated || oe.Tier0 || oe.Virtual || oe.Synthetic || oe.Tweak != nil {
		t.Errorf("object-events module must be view-only: %+v", oe)
	}
	for _, m := range CuratedModules() {
		if m.GVR == ObjectEventsGVR {
			t.Error("object-events module is curated")
		}
	}
	if n := len(Tier0Modules()); n != 14 {
		t.Errorf("tier0 modules: %d", n)
	}
	if oe.NCells != 7 || ev.NCells != 7 {
		t.Errorf("NCells %d / %d", ev.NCells, oe.NCells)
	}
}

func TestEventSourceColumn(t *testing.T) {
	m := Lookup(EventsGVR)
	e := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: "ns", UID: "u1", ResourceVersion: "1"}, Type: "Normal", Reason: "Scheduled",
		Source: corev1.EventSource{Component: "kubelet"}, ReportingController: "ignored", LastTimestamp: metav1.Time{Time: time.UnixMilli(7000)}}
	r := m.ProjectObj(e)
	if got := r.Cell(m.Col("source").Idx).S.Value(); got != "kubelet" {
		t.Errorf("source = %q", got)
	}
	if got := EventLastSeen(r); got != 7000 {
		t.Errorf("EventLastSeen = %d", got)
	}
	e.Source.Component = ""
	e.ReportingController = "default-scheduler"
	r = m.ProjectObj(e)
	if got := r.Cell(m.Col("source").Idx).S.Value(); got != "default-scheduler" {
		t.Errorf("fallback source = %q", got)
	}
	if len(r.cells) != m.NCells || m.NCells != 7 {
		t.Errorf("cells %d, NCells %d", len(r.cells), m.NCells)
	}
}

func TestTypedAPIInNamespace(t *testing.T) {
	var mu sync.Mutex
	var path, sel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		path, sel = r.URL.Path, r.URL.Query().Get("fieldSelector")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "EventList", "metadata": map[string]any{"resourceVersion": "5"}, "items": []any{}})
	}))
	defer srv.Close()
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	m := Lookup(EventsGVR)
	api, err := APIFor(cs, m, "", "v1", "events")
	if err != nil {
		t.Fatal(err)
	}
	nsr, ok := api.(Namespacer)
	if !ok {
		t.Fatal("typed API is not a Namespacer")
	}
	if _, err := nsr.InNamespace("team-a").List(context.Background(), metav1.ListOptions{FieldSelector: "involvedObject.name=x"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if path != "/api/v1/namespaces/team-a/events" || sel != "involvedObject.name=x" {
		t.Errorf("request %s ? %s", path, sel)
	}
	mu.Unlock()
	if _, err := api.List(context.Background(), metav1.ListOptions{}); err != nil { // the original stays cluster-wide
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if path != "/api/v1/events" {
		t.Errorf("unscoped request %s", path)
	}
}
