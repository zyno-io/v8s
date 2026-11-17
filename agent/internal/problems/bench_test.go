package problems_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems/ptest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// benchPod builds pod i of a 50k-pod cluster: 400 apps, 1 in 100 pods unhealthy in a varying way.
func benchPod(i, gen int, crash bool, now time.Time) *ingest.Row {
	app := i % 400
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("svc-%d-%d", app, i), Namespace: fmt.Sprintf("team-%d", app%37), UID: types.UID(fmt.Sprintf("u-%d", i)),
		ResourceVersion: fmt.Sprint(gen), OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: fmt.Sprintf("svc-%d-abc", app), Controller: ptr.To(true)}}},
		Spec:   corev1.PodSpec{NodeName: "node-1"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now)}}}}}}
	if crash {
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(now.Add(-time.Minute))}}
		p.Status.ContainerStatuses[0] = corev1.ContainerStatus{Name: "app", RestartCount: int32(3 + gen%5),
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}
	}
	return ingest.Project(p)
}

// BenchmarkProblemsPass measures one evaluation pass over a 50k-pod cluster in which 1% of the pods change.
// The cost is proportional to the changed keys, not the cluster size (docs/problems.md).
func BenchmarkProblemsPass(b *testing.B) {
	h := ptest.New(&testing.T{}, "v1/pods", "apps/v1/replicasets")
	now := h.Clk.Now()
	rows := make([]any, 50000)
	for i := range rows {
		rows[i] = benchPod(i, 0, i%100 == 0, now)
	}
	if err := h.Stores["v1/pods"].Replace(rows, "2"); err != nil {
		b.Fatal(err)
	}
	h.Pass() // the initial full evaluation
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		b.StopTimer()
		for k := 0; k < 500; k++ {
			i := k*100 + 1
			_ = h.Stores["v1/pods"].Update(benchPod(i, n+1, (n+1)%2 == 1, now))
		}
		b.StartTimer()
		h.Pass()
	}
	b.ReportMetric(float64(h.E.LastPass.Dirty), "dirty/pass")
}

// TestPassCostIsProportionalToChurn asserts the pass cost of 1% churn on 50k pods stays far below a full
// evaluation and reports the numbers.
func TestPassCostIsProportionalToChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("50k pods")
	}
	h := ptest.New(t, "v1/pods", "apps/v1/replicasets")
	now := h.Clk.Now()
	rows := make([]any, 50000)
	for i := range rows {
		rows[i] = benchPod(i, 0, i%100 == 0, now)
	}
	_ = h.Stores["v1/pods"].Replace(rows, "2")
	start := time.Now()
	h.Pass()
	full := time.Since(start)
	stats := h.E.LastPass
	var worst time.Duration
	for n := 1; n <= 5; n++ {
		for k := 0; k < 500; k++ {
			_ = h.Stores["v1/pods"].Update(benchPod(k*100+1, n, n%2 == 1, now))
		}
		s := time.Now()
		h.Pass()
		worst = max(worst, time.Since(s))
	}
	t.Logf("full evaluation of 50000 pods: %v (%d evals, %d events); 1%% churn pass: worst %v (%d events, %d dirty, %d evals)",
		full, stats.Evals, stats.Events, worst, h.E.LastPass.Events, h.E.LastPass.Dirty, h.E.LastPass.Evals)
	if h.E.LastPass.Dirty > 1000 {
		t.Fatalf("a 1%% churn pass marked %d keys dirty", h.E.LastPass.Dirty)
	}
	if worst > 400*time.Millisecond && worst*3 > full { // generous: the test runs next to other packages
		t.Fatalf("1%% churn pass took %v", worst)
	}
}
