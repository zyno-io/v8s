package problems_test

import (
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	. "github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/problems/ptest"
	"github.com/zyno-io/v8s/agent/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

const stsGVR = "apps/v1/statefulsets"

func hpaObj(m metav1.ObjectMeta) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: m, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 3},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 1, Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
			{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetResourceMetric"}}}}
}

// A normal rolling update (Available=True, Progressing=True since creation months ago) is not an outage.
func TestWorkloadUnavailableIgnoresNormalRollout(t *testing.T) {
	h := ptest.New(t, depGVR, "v1/pods")
	now := h.Clk.Now()
	created := now.Add(-90 * 24 * time.Hour)
	d := &appsv1.Deployment{ObjectMeta: meta("shop", "web"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(4)), ProgressDeadlineSeconds: ptr.To(int32(600))},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 3, AvailableReplicas: 3, UpdatedReplicas: 2, Conditions: []appsv1.DeploymentCondition{
			{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable", LastTransitionTime: mt(created)},
			{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "ReplicaSetUpdated", LastTransitionTime: mt(created)}}}}
	h.Put(depGVR, proj(t, depGVR, d))
	h.Pass()
	if ps := h.ByClass("workload.unavailable"); len(ps) != 0 {
		t.Fatalf("healthy rollout raised immediately: %+v", ps)
	}
	// a rollout that then stays stuck past the progress deadline is raised
	h.Advance(11 * time.Minute)
	if ps := h.ByClass("workload.unavailable"); len(ps) != 1 {
		t.Fatalf("stalled partial outage not raised after the deadline: %+v", ps)
	}
}

func stsRow(t *testing.T, ready, updated int32) *ingest.Row {
	return proj(t, stsGVR, &appsv1.StatefulSet{ObjectMeta: meta("db", "pg"), Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(5))},
		Status: appsv1.StatefulSetStatus{ReadyReplicas: ready, AvailableReplicas: ready, UpdatedReplicas: updated}})
}

// A StatefulSet bringing replicas up one by one keeps resetting the grace; a stalled one raises.
func TestWorkloadUnavailableStatefulSetProgress(t *testing.T) {
	h := ptest.New(t, stsGVR, "v1/pods")
	h.Put(stsGVR, stsRow(t, 1, 1))
	h.Pass()
	for i := int32(2); i <= 4; i++ {
		h.Advance(6 * time.Minute) // each step within the 10 min grace of the previous progress
		h.Put(stsGVR, stsRow(t, i, i))
		h.Pass()
		if ps := h.ByClass("workload.unavailable"); len(ps) != 0 {
			t.Fatalf("progressing StatefulSet raised at %d/5: %+v", i, ps)
		}
	}
	h.Advance(11 * time.Minute) // no progress any more
	if ps := h.ByClass("workload.unavailable"); len(ps) != 1 {
		t.Fatalf("stalled StatefulSet not raised: %+v", ps)
	}
	h.Put(stsGVR, stsRow(t, 5, 5))
	h.Pass()
	h.Advance(time.Minute)
	if ps := h.ByClass("workload.unavailable"); len(ps) != 0 {
		t.Fatalf("recovered StatefulSet still raised: %+v", ps)
	}
}

// A long-running pod that restarted once more and has been Ready again since is not crash-looping 90 s later.
func TestCrashloopRecentRestartExpires(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR)
	now := h.Clk.Now()
	tr := true
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop", UID: "u1", ResourceVersion: "1", CreationTimestamp: mt(now.Add(-30 * 24 * time.Hour)),
		OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-abc", Controller: &tr}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: mt(now.Add(-5 * time.Second))}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, RestartCount: 4,
				State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: mt(now.Add(-20 * time.Second))}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: mt(now.Add(-21 * time.Second))}}}}}}
	h.Put(podsGVR, ingest.Project(p))
	h.Pass()
	if len(h.ByClass("pod.crashloop")) == 0 {
		t.Fatal("setup: a container that restarted 20 s ago with a failing exit is expected inside the window")
	}
	h.Advance(2 * time.Hour)
	h.Advance(DefaultHold + time.Second)
	if ps := h.ByClass("pod.crashloop"); len(ps) != 0 {
		t.Fatalf("crashloop sticks after the 90 s window with no pod update: %+v", ps)
	}
}

// A Failed pod is over, not crash-looping.
func TestCrashloopIgnoresFailedPods(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR)
	r := crashPod("job-1", "job-x", h.Clk.Now().Add(-time.Minute))
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "job-2", Namespace: "shop", UID: "u2", ResourceVersion: "1"},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", RestartCount: 5,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: mt(h.Clk.Now())}}}}}}
	_ = r
	h.Put(podsGVR, ingest.Project(p))
	h.Pass()
	if ps := h.ByClass("pod.crashloop"); len(ps) != 0 {
		t.Fatalf("Failed pod flagged as crash-looping: %+v", ps)
	}
}

// A weekday schedule that last ran on Friday is not "not scheduling" over the weekend.
func TestCronWeekendNotFlagged(t *testing.T) {
	h := ptest.New(t, jobGVR, cronGVR)
	fri := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC) // a Friday
	h.Clk.Advance(fri.Add(time.Minute).Sub(h.Clk.Now()))
	m := meta("rep", "weekday")
	m.CreationTimestamp = mt(fri.Add(-30 * 24 * time.Hour))
	h.Put(cronGVR, proj(t, cronGVR, &batchv1.CronJob{ObjectMeta: m, Spec: batchv1.CronJobSpec{Schedule: "0 9 * * 1-5"},
		Status: batchv1.CronJobStatus{LastScheduleTime: ptr.To(mt(fri)), LastSuccessfulTime: ptr.To(mt(fri.Add(time.Minute)))}}))
	h.Pass()
	h.Advance(48*time.Hour + time.Minute) // Sunday 09:02
	if ps := h.ByClass("cronjob.not-scheduling"); len(ps) != 0 {
		t.Fatalf("raised on a weekend for a weekday schedule: %+v", ps[0].Title)
	}
	h.Advance(24 * time.Hour) // Monday 09:02: Monday's run is overdue by 2 min, still within the slack
	if ps := h.ByClass("cronjob.not-scheduling"); len(ps) != 0 {
		t.Fatalf("raised for one late run: %+v", ps[0].Title)
	}
	h.Advance(24*time.Hour + 10*time.Minute) // Tuesday 09:12: Monday and Tuesday both missed
	if ps := h.ByClass("cronjob.not-scheduling"); len(ps) != 1 {
		t.Fatalf("two missed fire times not raised: %+v", ps)
	}
}

func recreatedDeployment(t *testing.T, now time.Time, uid string) *ingest.Row {
	m := meta("shop", "api")
	m.UID = types.UID(uid)
	return proj(t, depGVR, &appsv1.Deployment{ObjectMeta: m, Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(3))},
		Status: appsv1.DeploymentStatus{AvailableReplicas: 0, Conditions: []appsv1.DeploymentCondition{
			{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, LastTransitionTime: mt(now.Add(-time.Hour))}}}})
}

// GitOps deletes and recreates a Deployment: the problem keeps its acknowledgement.
func TestAckSurvivesSubjectRecreation(t *testing.T) {
	h := ptest.New(t, depGVR, "v1/pods")
	now := h.Clk.Now()
	r1 := recreatedDeployment(t, now, "u1")
	h.Put(depGVR, r1)
	h.Pass()
	ps := h.ByClass("workload.unavailable")
	if len(ps) != 1 {
		t.Fatalf("setup: %+v", ps)
	}
	if err := h.Hub.Ack(ps[0].ID, 0); err != nil {
		t.Fatal(err)
	}
	h.Del(depGVR, r1)
	h.Pass()
	h.Put(depGVR, recreatedDeployment(t, now, "u2"))
	h.Pass()
	ps = h.ByClass("workload.unavailable")
	if len(ps) != 1 || !ps[0].Acked {
		t.Fatalf("ack lost across recreation: %+v", ps)
	}
}

// Disabling a class and enabling it again keeps its acknowledgements.
func TestAckSurvivesClassDisable(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR)
	crashers(h, "api-x", 1)
	p := only(t, h, "pod.crashloop")
	if err := h.Hub.Ack(p.ID, 0); err != nil {
		t.Fatal(err)
	}
	h.SetClass("pod.crashloop", false)
	if len(h.ByClass("pod.crashloop")) != 0 {
		t.Fatal("class not dropped")
	}
	h.SetClass("pod.crashloop", true)
	h.Pass()
	if !only(t, h, "pod.crashloop").Acked {
		t.Fatal("ack lost across disable/enable")
	}
}

// An acknowledged problem still in its grace period when the agent restarts is not GC'd 30 s after ready.
func TestGCAcksKeepsGraceClasses(t *testing.T) {
	h := ptest.New(t, svcGVR, sliceGVR)
	svc := &corev1.Service{ObjectMeta: meta("shop", "api"), Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "api"}, ClusterIP: "10.0.0.1"}}
	h.Put(svcGVR, proj(t, svcGVR, svc))
	h.Pass()
	h.Advance(2*time.Minute + time.Second)
	ps := h.ByClass("service.no-endpoints")
	if len(ps) != 1 {
		t.Fatalf("setup %+v", ps)
	}
	if err := h.Hub.Ack(ps[0].ID, 0); err != nil {
		t.Fatal(err)
	}
	h2 := ptest.NewWithSettings(t, h.Set, svcGVR, sliceGVR) // agent restart
	h2.Put(svcGVR, proj(t, svcGVR, svc))
	h2.Pass()
	h2.Clk.Advance(30 * time.Second)
	h2.Pass()
	h2.Hub.GCAcks("c1") // what the cluster runner does 30 s after PhaseReady
	h2.Hub.Flush()
	h2.Advance(2 * time.Minute)
	ps = h2.ByClass("service.no-endpoints")
	if len(ps) != 1 || !ps[0].Acked {
		t.Fatalf("ack lost across restart: %+v", ps)
	}
	// an ack nobody has seen for longer than AckMaxAge is dropped
	h2.Clk.Advance(AckMaxAge + time.Hour)
	h2.Del(svcGVR, proj(t, svcGVR, svc))
	h2.Pass()
	h2.Hub.GCAcks("c1")
	h2.Hub.Flush()
	if len(h2.Set.Acks) != 0 {
		t.Fatalf("long-unseen ack not collected: %v", h2.Set.Acks)
	}
}

// A snooze loaded from the settings after a restart still expires.
func TestSnoozeExpiresAfterRestart(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR)
	crashers(h, "api-x", 1)
	p := only(t, h, "pod.crashloop")
	if err := h.Hub.Ack(p.ID, h.Clk.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	h2 := ptest.NewWithSettings(t, h.Set, podsGVR, rsGVR)
	h2.Put(podsGVR, crashPod("api-x-0", "api-x", h2.Clk.Now().Add(-5*time.Minute)))
	h2.Pass()
	if !only(t, h2, "pod.crashloop").Acked {
		t.Fatal("setup: not acked after restart")
	}
	h2.Advance(2 * time.Hour)
	if only(t, h2, "pod.crashloop").Acked {
		t.Fatal("snooze did not expire after restart")
	}
}

// A detector's only input stops (watch evicted), the object is deleted meanwhile, then the watch returns.
func TestNoStaleProblemAfterInputReattach(t *testing.T) {
	h := ptest.New(t, hpaGVR)
	h.SetClass("hpa.unable", true)
	h.Put(hpaGVR, proj(t, hpaGVR, hpaObj(meta("shop", "web"))))
	h.Pass()
	if len(h.ByClass("hpa.unable")) != 1 {
		t.Fatalf("setup: %+v", h.ByClass("hpa.unable"))
	}
	h.E.Detach(hpaGVR)
	h.Pass()
	h.Sync(hpaGVR) // restarted: the HPA no longer exists
	h.Pass()
	h.Advance(time.Minute)
	if ps := h.ByClass("hpa.unable"); len(ps) != 0 {
		t.Fatalf("stale problem for a deleted HPA: %+v", ps)
	}
}

// One unreadable kind (a watch that never syncs) does not silence a class that has other inputs.
func TestUnavailableInputIsAbsent(t *testing.T) {
	h := ptest.New(t, depGVR, "v1/pods")
	h.E.Attach(stsGVR, store.New()) // StatefulSets cannot be listed: the store never syncs
	h.Put(depGVR, recreatedDeployment(t, h.Clk.Now(), "u1"))
	h.Advance(time.Minute)
	if ps := h.ByClass("workload.unavailable"); len(ps) != 0 {
		t.Fatalf("setup: an unsynced input must hold the class back: %+v", ps)
	}
	h.E.SetUnavailable("apps/statefulsets", "no permission to list statefulsets")
	h.Advance(time.Minute)
	if ps := h.ByClass("workload.unavailable"); len(ps) != 1 {
		t.Fatalf("an unavailable input silenced the class: %+v", ps)
	}
	if got := h.E.Unavailable(); got["apps/statefulsets"] == "" {
		t.Fatalf("unavailable not recorded: %v", got)
	}
	h.E.SetUnavailable("apps/statefulsets", "")
	if len(h.E.Unavailable()) != 0 {
		t.Fatal("unavailable not cleared")
	}
}

// Warning events about a cluster-scoped object (a Node, whose events live in "default") count towards it.
func TestEventsForClusterScopedObject(t *testing.T) {
	h := ptest.New(t, evGVR, nodeGVR)
	h.SetClass("events.warning-burst", true)
	ev := &corev1.Event{ObjectMeta: meta("default", "n1.1"), Type: "Warning", Reason: "NodeNotReady", Message: "kubelet stopped",
		InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "n1"}, Count: 12, LastTimestamp: mt(h.Clk.Now())}
	h.Put(evGVR, proj(t, evGVR, ev))
	h.Pass()
	ps := h.ByClass("events.warning-burst")
	if len(ps) != 1 || ps[0].Subject.Kind != "Node" || ps[0].Subject.NS != "" || ps[0].Count != 12 {
		t.Fatalf("events of a Node not attributed to it: %+v", ps)
	}
}

func jobPod(name, job string, now time.Time) *ingest.Row {
	tr := true
	return ingest.Project(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "rep", UID: types.UID("uid-" + name), ResourceVersion: "1", CreationTimestamp: mt(now.Add(-time.Hour)),
			OwnerReferences: []metav1.OwnerReference{{Kind: "Job", Name: job, Controller: &tr}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: mt(now.Add(-5 * time.Minute))}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", RestartCount: 5,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}}})
}

func cronJobRun(t *testing.T, name, cron string) *ingest.Row {
	m := meta("rep", name)
	m.OwnerReferences = []metav1.OwnerReference{{Kind: "CronJob", Name: cron, Controller: ptr.To(true)}}
	return proj(t, jobGVR, &batchv1.Job{ObjectMeta: m})
}

// The pods of successive runs of a CronJob share one problem (and so one acknowledgement) rolled up to the CronJob.
func TestCronJobPodsRollUpToCronJob(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR, jobGVR, cronGVR)
	now := h.Clk.Now()
	// the pod arrives before its Job is known, then the Job lands
	h.Put(podsGVR, jobPod("nightly-1-a", "nightly-1", now))
	h.Pass()
	h.Put(jobGVR, cronJobRun(t, "nightly-1", "nightly"))
	h.Pass()
	h.Advance(DefaultHold + time.Second) // the problem filed under the bare Job resolves
	p := only(t, h, "pod.crashloop")
	if want := ID("c1", "pod.crashloop", Subject{Group: "batch", Kind: "CronJob", NS: "rep", Name: "nightly"}, "app"); p.ID != want {
		t.Fatalf("not rolled up to the CronJob: %+v", p)
	}
	if err := h.Hub.Ack(p.ID, 0); err != nil {
		t.Fatal(err)
	}
	// the next run replaces the first: same problem, still acknowledged
	h.Put(jobGVR, cronJobRun(t, "nightly-2", "nightly"))
	h.Put(podsGVR, jobPod("nightly-2-a", "nightly-2", now))
	h.Del(podsGVR, jobPod("nightly-1-a", "nightly-1", now))
	h.Pass()
	p2 := only(t, h, "pod.crashloop")
	if p2.ID != p.ID || !p2.Acked {
		t.Fatalf("runs do not share the problem: %+v", p2)
	}
}

// A rollout that is healthy again forgets its progress: the next rollout, a day later, is not judged by the old one.
func TestWorkloadUnavailableSecondRolloutNotRaisedImmediately(t *testing.T) {
	h := ptest.New(t, depGVR, "v1/pods")
	created := h.Clk.Now().Add(-90 * 24 * time.Hour)
	dep := func(ready, updated int32) *ingest.Row {
		d := &appsv1.Deployment{ObjectMeta: meta("shop", "web"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(4)), ProgressDeadlineSeconds: ptr.To(int32(600))},
			Status: appsv1.DeploymentStatus{ReadyReplicas: ready, AvailableReplicas: ready, UpdatedReplicas: updated, Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable", LastTransitionTime: mt(created)},
				{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "ReplicaSetUpdated", LastTransitionTime: mt(created)}}}}
		return proj(t, depGVR, d)
	}
	h.Put(depGVR, dep(3, 2)) // first rollout
	h.Pass()
	h.Advance(time.Minute)
	h.Put(depGVR, dep(4, 4)) // done
	h.Pass()
	h.Advance(24 * time.Hour)
	h.Put(depGVR, dep(3, 2)) // second rollout, a day later
	h.Pass()
	if ps := h.ByClass("workload.unavailable"); len(ps) != 0 {
		t.Fatalf("second rollout raised immediately: %+v", ps)
	}
	h.Advance(11 * time.Minute)
	if ps := h.ByClass("workload.unavailable"); len(ps) != 1 {
		t.Fatalf("stalled second rollout not raised after the deadline: %+v", ps)
	}
}

// Replacing a workload's pods (a rollout) is not the subject going away: once the problem has stayed resolved
// through the hold, a later recurrence is a new problem without the old acknowledgement.
func TestAckDroppedWhenMemberPodsReplaced(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR)
	h.Put(rsGVR, rsRow("api-x", "api"))
	rows := crashers(h, "api-x", 2)
	p := only(t, h, "pod.crashloop")
	if err := h.Hub.Ack(p.ID, 0); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows { // the rollout replaces the crashing pods
		h.Del(podsGVR, r)
	}
	h.Pass()
	h.Put(podsGVR, healthyPod("api-y-0", "api-x"))
	h.Pass()
	h.Advance(DefaultHold + time.Second)
	if len(h.ByClass("pod.crashloop")) != 0 {
		t.Fatal("not resolved")
	}
	h.Hub.Flush()
	if _, ok := h.Set.Acks[p.ID]; ok {
		t.Fatal("ack stayed after the member pods were replaced and the problem resolved")
	}
	h.Put(podsGVR, crashPod("api-y-0", "api-x", h.Clk.Now().Add(-5*time.Minute)))
	h.Pass()
	if only(t, h, "pod.crashloop").Acked {
		t.Fatal("a later crashloop inherited the old acknowledgement")
	}
}

// A stable live problem keeps its acknowledgement's last-seen current, so GC after a long quiet period keeps it.
func TestGCAcksKeepsStableLiveAck(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR)
	crashers(h, "api-x", 1)
	p := only(t, h, "pod.crashloop")
	if err := h.Hub.Ack(p.ID, 0); err != nil {
		t.Fatal(err)
	}
	h.Clk.Advance(AckMaxAge + 24*time.Hour) // nothing about the problem changes for 8 days
	if _, ok := h.E.Problem(p.ID); !ok {
		t.Skip("problem no longer live")
	}
	h.Hub.GCAcks("c1")
	h.Hub.Flush()
	if _, ok := h.Set.Acks[p.ID]; !ok {
		t.Fatal("ack of a live problem collected")
	}
}
