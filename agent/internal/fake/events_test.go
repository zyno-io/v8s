package fake

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
)

func eventsAPI(t *testing.T) (ingest.API, *Backend) {
	t.Helper()
	src := NewSource(Config{Clusters: 1, Pods: 2000})
	t.Cleanup(func() { src.Release("prod-us") })
	be, err := src.Backend("prod-us")
	if err != nil {
		t.Fatal(err)
	}
	api, err := be.API(ingest.EventsGVR, ingest.Lookup(ingest.EventsGVR))
	if err != nil {
		t.Fatal(err)
	}
	return api, be.(*Backend)
}

func listEvents(t *testing.T, api ingest.API, opts metav1.ListOptions) []corev1.Event {
	t.Helper()
	obj, err := api.List(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return obj.(*corev1.EventList).Items
}

func TestFakeEventsHonorFieldSelectors(t *testing.T) {
	api, _ := eventsAPI(t)
	all := listEvents(t, api, metav1.ListOptions{})
	warn := listEvents(t, api, metav1.ListOptions{FieldSelector: "type=Warning"})
	if len(warn) == 0 || len(warn) >= len(all) {
		t.Fatalf("type=Warning: %d of %d", len(warn), len(all))
	}
	for _, e := range warn {
		if e.Type != "Warning" {
			t.Fatalf("a %s event passed type=Warning", e.Type)
		}
	}
	// a node's events live in default and are matched by kind, name and an empty involvedObject.namespace
	sel := "involvedObject.kind=Node,involvedObject.name=node-5,involvedObject.namespace="
	ns := api.(ingest.Namespacer)
	got := listEvents(t, ns.InNamespace("default"), metav1.ListOptions{FieldSelector: sel})
	reasons := map[string]string{}
	for _, e := range got {
		reasons[e.Reason] = string(e.UID)
		if e.Namespace != "default" || e.InvolvedObject.Name != "node-5" {
			t.Errorf("unexpected event %s/%s about %s", e.Namespace, e.Name, e.InvolvedObject.Name)
		}
	}
	if len(got) != 3 || reasons["Starting"] == "" || reasons["RegisteredNode"] == "" || reasons["NodeNotReady"] == "" {
		t.Fatalf("node events: %v", reasons)
	}
	if other := listEvents(t, ns.InNamespace("team-0"), metav1.ListOptions{FieldSelector: sel}); len(other) != 0 {
		t.Fatalf("namespace scoping ignored: %d events", len(other))
	}
	if _, err := api.List(context.Background(), metav1.ListOptions{FieldSelector: "a=b=c,"}); err == nil {
		t.Error("a malformed selector was accepted")
	}
}

func nextEvent(t *testing.T, w watch.Interface, what string) watch.Event {
	t.Helper()
	select {
	case ev := <-w.ResultChan():
		return ev
	case <-time.After(5 * time.Second):
		t.Fatalf("no watch event: %s", what)
	}
	return watch.Event{}
}

func TestFakeEventsWatchSelectorAndScaleEmitsScalingReplicaSet(t *testing.T) {
	api, be := eventsAPI(t)
	ns := api.(ingest.Namespacer).InNamespace("team-2")
	initial := true
	w, err := ns.Watch(context.Background(), metav1.ListOptions{
		FieldSelector: "involvedObject.kind=Deployment,involvedObject.name=svc-2,involvedObject.namespace=team-2", SendInitialEvents: &initial})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	// the initial events are the generated event of the deployment, and only that one, then the bookmark
	ev := nextEvent(t, w, "initial event")
	if e, ok := ev.Object.(*corev1.Event); !ok || e.Reason != "ScalingReplicaSet" || e.Source.Component != "deployment-controller" {
		t.Fatalf("initial %v %+v", ev.Type, ev.Object)
	}
	if ev = nextEvent(t, w, "bookmark"); ev.Type != watch.Bookmark {
		t.Fatalf("want the initial-events bookmark, got %v", ev.Type)
	}
	dep, ok := be.World().Get("apps/v1/deployments", "team-2", "svc-2")
	if !ok {
		t.Fatal("deployment missing")
	}
	cur, _, _ := unstructured.NestedInt64(dep.Object, "spec", "replicas")
	dyn := be.Dynamic().Resource(schemaGVR("apps/v1/deployments")).Namespace("team-2")
	if _, err := dyn.Patch(context.Background(), "svc-2", types.MergePatchType, []byte(`{"spec":{"replicas":`+itoa(cur+3)+`}}`), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	ev = nextEvent(t, w, "scale event")
	e, ok := ev.Object.(*corev1.Event)
	if !ok || ev.Type != watch.Added || e.Reason != "ScalingReplicaSet" || e.Type != "Normal" || e.InvolvedObject.Name != "svc-2" {
		t.Fatalf("scale produced %v %+v", ev.Type, ev.Object)
	}
	if want := "to " + itoa(cur+3); len(e.Message) < len(want) || e.Message[len(e.Message)-len(want):] != want {
		t.Errorf("message %q", e.Message)
	}
	// a different deployment's events never reach this watch
	if _, err := be.Dynamic().Resource(schemaGVR("apps/v1/deployments")).Namespace("team-3").Patch(context.Background(), "svc-3", types.MergePatchType,
		[]byte(`{"spec":{"replicas":9}}`), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-w.ResultChan():
		t.Fatalf("an unrelated event passed the selector: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestFakeDynamicNameWatchStillWorks(t *testing.T) {
	_, be := eventsAPI(t)
	dyn := be.Dynamic().Resource(schemaGVR("v1/configmaps")).Namespace("team-0")
	w, err := dyn.Watch(context.Background(), metav1.ListOptions{FieldSelector: "metadata.name=config-0"})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	cm, ok := be.World().Get("v1/configmaps", "team-0", "config-0")
	if !ok {
		t.Skip("config-0 is not in team-0")
	}
	cm.Object["data"] = map[string]any{"a": "b"}
	if _, err := dyn.Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	ev := nextEvent(t, w, "named configmap")
	if u, ok := ev.Object.(runtime.Unstructured); !ok || u.UnstructuredContent()["metadata"].(map[string]any)["name"] != "config-0" {
		t.Fatalf("event %+v", ev)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func schemaGVR(gvr string) schema.GroupVersionResource {
	g, v, r, _ := ingest.ParseGVR(gvr)
	return schema.GroupVersionResource{Group: g, Version: v, Resource: r}
}
