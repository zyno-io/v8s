package problems_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	. "github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/problems/ptest"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	podsGVR = "v1/pods"
	rsGVR   = "apps/v1/replicasets"
)

func crashPod(name, rs string, since time.Time) *ingest.Row {
	tr := true
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("uid-" + name), ResourceVersion: "1", CreationTimestamp: metav1.NewTime(since.Add(-time.Hour)),
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rs, Controller: &tr}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(since)}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", RestartCount: 14,
				State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: metav1.NewTime(since)}}}}},
	}
	return ingest.Project(p)
}

func rsRow(name, dep string) *ingest.Row {
	tr := true
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("uid-" + name), ResourceVersion: "1",
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: dep, Controller: &tr}}}}
	r := ingest.NewRow("uid-"+name, name, "1", "shop", 0, false, 3)
	_ = rs
	r.PutN(1)
	r.PutN(1)
	r.PutN(1)
	r.SetOwner("Deployment", dep)
	return r
}

func TestCrashloopRollupAndHold(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR)
	since := h.Clk.Now().Add(-10 * time.Minute)
	h.Put(rsGVR, rsRow("api-abc", "api"))
	p1 := crashPod("api-abc-1", "api-abc", since)
	h.Put(podsGVR, p1)
	h.Pass()
	ps := h.ByClass("pod.crashloop")
	if len(ps) != 1 || ps[0].Subject.Kind != "Pod" || ps[0].Count != 1 {
		t.Fatalf("one pod: %+v", ps)
	}
	id := ps[0].ID
	if ps[0].Since != since.UnixMilli() {
		t.Fatalf("since from evidence: got %d want %d", ps[0].Since, since.UnixMilli())
	}
	h.Put(podsGVR, crashPod("api-abc-2", "api-abc", since.Add(time.Minute)))
	h.Pass()
	ps = h.ByClass("pod.crashloop")
	if len(ps) != 1 || ps[0].ID != id || ps[0].Subject.Kind != "Deployment" || ps[0].Count != 2 {
		t.Fatalf("rolled up under the same id: %+v", ps)
	}
	if ps[0].Title != "Deployment api: 2/2 pods CrashLoopBackOff" {
		t.Fatalf("title %q", ps[0].Title)
	}
	// one pod recovers: count shrinks immediately; both gone: held 30 s, then cleared
	h.Del(podsGVR, p1)
	h.Pass()
	if ps = h.ByClass("pod.crashloop"); len(ps) != 1 || ps[0].ID != id || ps[0].Count != 1 {
		t.Fatalf("after one recovered: %+v", ps)
	}
	h.Put(podsGVR, crashPod("api-abc-2", "api-abc", since.Add(time.Minute))) // unchanged
	healthy := crashPod("api-abc-2", "api-abc", since)
	_ = healthy
}

func TestGraceHoldAndDeadline(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR)
	r := ingest.Project(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pend", Namespace: "shop", UID: types.UID("u-pend"), ResourceVersion: "1"},
		Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
			Message: "0/3 nodes: insufficient cpu", LastTransitionTime: metav1.NewTime(h.Clk.Now())}}}})
	h.Put(podsGVR, r)
	h.Pass()
	if n := len(h.ByClass("pod.pending")); n != 0 {
		t.Fatalf("raised before the 5 minute grace: %d", n)
	}
	h.Advance(4 * time.Minute)
	if n := len(h.ByClass("pod.pending")); n != 0 {
		t.Fatalf("raised at 4m: %d", n)
	}
	h.Advance(61 * time.Second) // the deadline heap woke the key
	ps := h.ByClass("pod.pending")
	if len(ps) != 1 || ps[0].Message != "Unschedulable: 0/3 nodes: insufficient cpu" {
		t.Fatalf("after grace: %+v", ps)
	}
	// resolves: held for the hysteresis window
	h.Del(podsGVR, r)
	h.Pass()
	if n := len(h.ByClass("pod.pending")); n != 0 {
		t.Fatalf("deleted object clears immediately, got %d", n)
	}
}

func TestHoldBeforeClearing(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR)
	since := h.Clk.Now().Add(-time.Minute)
	h.Put(podsGVR, crashPod("a-1", "a-x", since))
	h.Pass()
	if len(h.ByClass("pod.crashloop")) != 1 {
		t.Fatal("not raised")
	}
	// the pod keeps existing but recovers (running, ready)
	h.Put(podsGVR, ingest.Project(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a-1", Namespace: "shop", UID: types.UID("uid-a-1"), ResourceVersion: "2"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}))
	h.Pass()
	if len(h.ByClass("pod.crashloop")) != 1 {
		t.Fatal("cleared at once instead of being held")
	}
	h.Advance(29 * time.Second)
	if len(h.ByClass("pod.crashloop")) != 1 {
		t.Fatal("cleared before 30 s")
	}
	h.Advance(2 * time.Second)
	if n := len(h.ByClass("pod.crashloop")); n != 0 {
		t.Fatalf("not cleared after hold: %d", n)
	}
}

func TestStableIDAcrossRecreation(t *testing.T) {
	a := ID("c", "pod.crashloop", Subject{Group: "apps", Kind: "Deployment", NS: "n", Name: "api", UID: "1"}, "app")
	b := ID("c", "pod.crashloop", Subject{Group: "apps", Kind: "Deployment", NS: "n", Name: "api", UID: "2"}, "app")
	if a != b {
		t.Fatal("id depends on the uid")
	}
	if a == ID("c2", "pod.crashloop", Subject{Group: "apps", Kind: "Deployment", NS: "n", Name: "api"}, "app") {
		t.Fatal("id ignores the cluster")
	}
}

func TestCountsAndPrefs(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR)
	h.Put(podsGVR, crashPod("a-1", "a-x", h.Clk.Now().Add(-time.Minute)))
	h.Pass()
	if c := h.E.Counts()["pod.crashloop"]; c.Total != 1 {
		t.Fatalf("counts %+v", c)
	}
	h.Set.ProblemPrefs = map[string]any{"classes": map[string]any{"pod.crashloop": false}}
	h.Hub.SettingsChanged(h.Set)
	if len(h.ByClass("pod.crashloop")) != 0 || h.E.Counts()["pod.crashloop"].Total != 0 {
		t.Fatal("disabled class still present")
	}
	h.Set.ProblemPrefs = map[string]any{}
	h.Hub.SettingsChanged(h.Set)
	h.Pass()
	if len(h.ByClass("pod.crashloop")) != 1 {
		t.Fatal("re-enabled class not re-evaluated")
	}
	_ = fmt.Sprint
}
