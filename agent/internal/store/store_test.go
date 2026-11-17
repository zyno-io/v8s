package store

import (
	"fmt"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func row(uid, ns, name string, restarts int32) *ingest.PodRow {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + uid), ResourceVersion: "1"}}
	p.Spec.Containers = []corev1.Container{{Name: "c"}}
	p.Status.Phase = corev1.PodRunning
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "c", Ready: true, RestartCount: restarts,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	return ingest.Project(p)
}

func drain(s *Store) []Event {
	ev, _, _ := s.Drain(nil, -1, nil, nil)
	return ev
}

func TestReplaceIsADiff(t *testing.T) {
	s := New()
	var list []interface{}
	for i := 0; i < 5; i++ {
		list = append(list, row(fmt.Sprint(i), "a", fmt.Sprintf("p%d", i), 0))
	}
	if err := s.Replace(list, "10"); err != nil {
		t.Fatal(err)
	}
	if ev := drain(s); len(ev) != 5 {
		t.Fatalf("initial replace: %d events, want 5", len(ev))
	}
	// relist: p0 unchanged, p1 changed (restarts), p2 removed, p3 unchanged, p4 unchanged, p9 new
	list = []interface{}{
		row("0", "a", "p0", 0), row("1", "a", "p1", 5), row("3", "a", "p3", 0), row("4", "a", "p4", 0), row("9", "b", "p9", 0),
	}
	if err := s.Replace(list, "20"); err != nil {
		t.Fatal(err)
	}
	ev := drain(s)
	var adds, updates, deletes int
	for _, e := range ev {
		switch {
		case e.Old == nil:
			adds++
		case e.New == nil:
			deletes++
		default:
			updates++
		}
	}
	if adds != 1 || updates != 1 || deletes != 1 || len(ev) != 3 {
		t.Fatalf("diff produced adds=%d updates=%d deletes=%d (%d events), want 1/1/1", adds, updates, deletes, len(ev))
	}
	if s.Len() != 5 || s.NamespaceCount("a") != 4 || s.NamespaceCount("b") != 1 {
		t.Fatalf("len=%d a=%d b=%d", s.Len(), s.NamespaceCount("a"), s.NamespaceCount("b"))
	}
	// unchanged row keeps version 1, changed bumps to 2
	if it, _, _ := s.GetByKey("uid-0"); it.(*ingest.PodRow).Ver() != 1 {
		t.Fatal("unchanged row version bumped")
	}
	if it, _, _ := s.GetByKey("uid-1"); it.(*ingest.PodRow).Ver() != 2 {
		t.Fatal("changed row version not bumped")
	}
	if s.LastStoreSyncResourceVersion() != "20" {
		t.Fatal("rv not tracked")
	}
}

func TestHandleReuseAndGeneration(t *testing.T) {
	s := New()
	_ = s.Add(row("a", "ns", "x", 0))
	ev := drain(s)
	h, gen := ev[0].H, ev[0].Gen
	_ = s.Delete(row("a", "ns", "x", 0))
	_ = s.Add(row("b", "ns", "x", 0)) // same name, new uid: must reuse the freed handle
	ev = drain(s)
	if len(ev) != 2 || ev[1].H != h {
		t.Fatalf("handle not reused: %+v", ev)
	}
	if ev[1].Gen == gen {
		t.Fatal("generation must change when a handle is reused")
	}
	if _, ok := s.Row(h, gen); ok {
		t.Fatal("stale generation must not resolve")
	}
	if _, ok := s.Row(h, ev[1].Gen); !ok {
		t.Fatal("current generation must resolve")
	}
}

func TestNamespaceIndexSwapRemove(t *testing.T) {
	s := New()
	for i := 0; i < 10; i++ {
		_ = s.Add(row(fmt.Sprint(i), "n", fmt.Sprint("p", i), 0))
	}
	for _, i := range []int{0, 5, 9, 3} {
		_ = s.Delete(row(fmt.Sprint(i), "n", fmt.Sprint("p", i), 0))
	}
	items := s.Snapshot(nil, []string{"n"})
	if len(items) != 6 || s.NamespaceCount("n") != 6 {
		t.Fatalf("ns index has %d items", len(items))
	}
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.Row.Name()] = true
	}
	if seen["p0"] || seen["p5"] || !seen["p1"] || !seen["p8"] {
		t.Fatalf("wrong survivors: %v", seen)
	}
}
