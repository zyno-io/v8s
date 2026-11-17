package problems_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	. "github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/problems/ptest"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

func proj(t *testing.T, gvr string, o runtime.Object) *ingest.Row {
	t.Helper()
	r := ingest.Lookup(gvr).ProjectObj(o)
	if r == nil {
		t.Fatalf("no projection for %s", gvr)
	}
	return r
}

func meta(ns, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + ns + "-" + name), ResourceVersion: "1"}
}

func mt(tm time.Time) metav1.Time { return metav1.NewTime(tm) }

var (
	nodeGVR  = "v1/nodes"
	depGVR   = "apps/v1/deployments"
	jobGVR   = "batch/v1/jobs"
	cronGVR  = "batch/v1/cronjobs"
	svcGVR   = "v1/services"
	sliceGVR = "discovery.k8s.io/v1/endpointslices"
	ingGVR   = "networking.k8s.io/v1/ingresses"
	pvcGVR   = "v1/persistentvolumeclaims"
	evGVR    = "v1/events"
	hpaGVR   = "autoscaling/v2/horizontalpodautoscalers"
)

func hasClass(h *ptest.Harness, class string) bool { return len(h.ByClass(class)) > 0 }

func TestPodDetectors(t *testing.T) {
	now := ptest.NewClock().Now()
	tests := []struct {
		name  string
		pod   func(p *corev1.Pod)
		class string
		grace time.Duration // 0: raised at once
	}{
		{"crashloop", func(p *corev1.Pod) {
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 3, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}
		}, "pod.crashloop", 0},
		{"crashloop between back-offs (terminated, not waiting)", func(p *corev1.Pod) {
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 5,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: mt(now)}}}}
		}, "pod.crashloop", 0},
		{"oom kill reported as Error exit 137 with a memory limit", func(p *corev1.Pod) {
			p.Spec.Containers = []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")}}}}
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 2,
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 137, FinishedAt: mt(now.Add(-time.Minute))}}}}
		}, "pod.oomkilled", 0},
		{"image pull", func(p *corev1.Pod) {
			p.Status.Phase = corev1.PodPending
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: "manifest unknown"}}}}
		}, "pod.image-pull", 0},
		{"config error", func(p *corev1.Pod) {
			p.Status.Phase = corev1.PodPending
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError", Message: `secret "x" not found`}}}}
		}, "pod.config-error", 0},
		{"oomkilled", func(p *corev1.Pod) {
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 2, Ready: true,
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: mt(now.Add(-20 * time.Minute))}}}}
		}, "pod.oomkilled", 0},
		{"pending", func(p *corev1.Pod) {
			p.Status.Phase = corev1.PodPending
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "insufficient memory", LastTransitionTime: mt(now)}}
		}, "pod.pending", 5 * time.Minute},
		{"not ready", func(p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: mt(now)}}
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Ready: false, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: mt(now)}}}}
		}, "pod.not-ready", 5 * time.Minute},
		{"stuck terminating", func(p *corev1.Pod) { p.DeletionTimestamp = ptr.To(mt(now)) }, "pod.stuck-terminating", 10 * time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := ptest.New(t, "v1/pods", "apps/v1/replicasets")
			p := &corev1.Pod{ObjectMeta: meta("shop", "web-1"), Status: corev1.PodStatus{Phase: corev1.PodRunning}}
			tc.pod(p)
			h.Put("v1/pods", ingest.Project(p))
			h.Pass()
			if tc.grace == 0 {
				if !hasClass(h, tc.class) {
					t.Fatalf("%s not raised", tc.class)
				}
				return
			}
			if hasClass(h, tc.class) {
				t.Fatal("raised before its grace period")
			}
			h.Advance(tc.grace - time.Second)
			if hasClass(h, tc.class) {
				t.Fatal("raised one second early")
			}
			h.Advance(2 * time.Second)
			if !hasClass(h, tc.class) {
				t.Fatalf("not raised after the grace period (deadline heap)")
			}
		})
	}
}

func TestOOMKilledExpiresAfterAnHour(t *testing.T) {
	h := ptest.New(t, "v1/pods", "apps/v1/replicasets")
	at := h.Clk.Now().Add(-50 * time.Minute)
	p := &corev1.Pod{ObjectMeta: meta("shop", "web-1"), Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", RestartCount: 1, Ready: true,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", FinishedAt: mt(at)}}}}}}
	h.Put("v1/pods", ingest.Project(p))
	h.Pass()
	if !hasClass(h, "pod.oomkilled") {
		t.Fatal("not raised")
	}
	h.Advance(11 * time.Minute) // TTL elapsed: the deadline removes it, then the hold keeps it for 30 s
	h.Advance(31 * time.Second)
	if hasClass(h, "pod.oomkilled") {
		t.Fatal("still present more than an hour after the kill")
	}
}

func TestDisabledByDefaultClasses(t *testing.T) {
	h := ptest.New(t, "v1/pods", "apps/v1/replicasets", nodeGVR)
	p := &corev1.Pod{ObjectMeta: meta("shop", "ev"), Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}}
	h.Put("v1/pods", ingest.Project(p))
	h.Put(nodeGVR, proj(t, nodeGVR, &corev1.Node{ObjectMeta: meta("", "n1"), Spec: corev1.NodeSpec{Unschedulable: true},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}))
	h.Pass()
	if hasClass(h, "pod.evicted") || hasClass(h, "node.cordoned") {
		t.Fatal("off-by-default classes produced problems")
	}
	h.Set.ProblemPrefs = map[string]any{"classes": map[string]any{"pod.evicted": true}, "clusters": map[string]any{"c1": map[string]any{"node.cordoned": true}}}
	h.Hub.SettingsChanged(h.Set)
	h.Pass()
	if !hasClass(h, "pod.evicted") || !hasClass(h, "node.cordoned") {
		t.Fatalf("enabling by preference (global and per-cluster) did not raise: %v", h.E.Counts())
	}
}

func TestWorkloadClasses(t *testing.T) {
	h := ptest.New(t, depGVR, "v1/pods")
	now := h.Clk.Now()
	mkDep := func(name string, availSince time.Time, progReason string, progSince time.Time) *ingest.Row {
		d := &appsv1.Deployment{ObjectMeta: meta("shop", name), Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(3)), ProgressDeadlineSeconds: ptr.To(int32(600))},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1, AvailableReplicas: 1, Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, LastTransitionTime: mt(availSince)},
				{Type: appsv1.DeploymentProgressing, Status: map[bool]corev1.ConditionStatus{true: corev1.ConditionFalse, false: corev1.ConditionTrue}[progReason != ""], Reason: progReason, LastTransitionTime: mt(progSince)}}}}
		return proj(t, depGVR, d)
	}
	h.Put(depGVR, mkDep("old", now.Add(-20*time.Minute), "ProgressDeadlineExceeded", now.Add(-5*time.Minute)))
	h.Put(depGVR, mkDep("fresh", now.Add(-1*time.Minute), "", now.Add(-1*time.Minute)))
	h.Pass()
	un := h.ByClass("workload.unavailable")
	if len(un) != 1 || un[0].Subject.Name != "old" || un[0].Title != "Deployment old: 1/3 available" {
		t.Fatalf("unavailable beyond the progress deadline: %+v", un)
	}
	if st := h.ByClass("workload.rollout-stuck"); len(st) != 1 || st[0].Subject.Name != "old" {
		t.Fatalf("rollout-stuck: %+v", st)
	}
	h.Advance(9*time.Minute + 5*time.Second) // "fresh" crosses its progress deadline
	if n := len(h.ByClass("workload.unavailable")); n != 2 {
		t.Fatalf("fresh not raised after its deadline: %d", n)
	}
}

func TestJobsAndCronJobs(t *testing.T) {
	h := ptest.New(t, jobGVR, cronGVR)
	now := h.Clk.Now()
	failed := func(ns, name string, owner string, created time.Time) *batchv1.Job {
		j := &batchv1.Job{ObjectMeta: meta(ns, name), Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", LastTransitionTime: mt(created)}}}}
		j.CreationTimestamp = mt(created)
		if owner != "" {
			j.OwnerReferences = []metav1.OwnerReference{{Kind: "CronJob", Name: owner, Controller: ptr.To(true)}}
		}
		return j
	}
	h.Put(jobGVR, proj(t, jobGVR, failed("rep", "nightly-export", "", now.Add(-time.Hour))))
	h.Put(cronGVR, proj(t, cronGVR, &batchv1.CronJob{ObjectMeta: meta("rep", "hourly"), Spec: batchv1.CronJobSpec{Schedule: "*/5 * * * *"},
		Status: batchv1.CronJobStatus{LastScheduleTime: ptr.To(mt(now.Add(-2 * time.Minute))), LastSuccessfulTime: ptr.To(mt(now.Add(-3 * time.Hour)))}}))
	h.Put(jobGVR, proj(t, jobGVR, failed("rep", "hourly-1", "hourly", now.Add(-10*time.Minute))))
	h.Pass()
	if jf := h.ByClass("job.failed"); len(jf) != 1 || jf[0].Subject.Name != "nightly-export" {
		t.Fatalf("job.failed (cron-owned jobs are reported by their CronJob): %+v", jf)
	}
	if lf := h.ByClass("cronjob.last-failed"); len(lf) != 1 || lf[0].Subject.Name != "hourly" {
		t.Fatalf("cronjob.last-failed: %+v", lf)
	}
	if hasClass(h, "cronjob.not-scheduling") {
		t.Fatal("a cronjob that scheduled 2 minutes ago is not 'not scheduling'")
	}
	// a cronjob that has not been scheduled for more than twice its interval
	h.Put(cronGVR, proj(t, cronGVR, &batchv1.CronJob{ObjectMeta: func() metav1.ObjectMeta {
		m := meta("rep", "stale")
		m.CreationTimestamp = mt(now.Add(-24 * time.Hour))
		return m
	}(),
		Spec:   batchv1.CronJobSpec{Schedule: "0 * * * *"},
		Status: batchv1.CronJobStatus{LastScheduleTime: ptr.To(mt(now.Add(-90 * time.Minute)))}}))
	h.Pass()
	if hasClass(h, "cronjob.not-scheduling") {
		t.Fatal("raised before twice the interval elapsed since the last schedule (90m of 2h)")
	}
	h.Advance(31 * time.Minute)
	found := false
	for _, p := range h.ByClass("cronjob.not-scheduling") {
		found = found || p.Subject.Name == "stale"
	}
	if !found {
		t.Fatalf("not-scheduling: %+v", h.ByClass("cronjob.not-scheduling"))
	}
}

func TestNodeClasses(t *testing.T) {
	h := ptest.New(t, nodeGVR)
	now := h.Clk.Now()
	h.Put(nodeGVR, proj(t, nodeGVR, &corev1.Node{ObjectMeta: meta("", "bad"), Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
		{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady", LastTransitionTime: mt(now)},
		{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue, Reason: "KubeletHasDiskPressure", LastTransitionTime: mt(now.Add(-time.Hour))}}}}))
	h.Pass()
	if hasClass(h, "node.not-ready") {
		t.Fatal("raised before 60 s")
	}
	if p := h.ByClass("node.pressure"); len(p) != 1 || p[0].Title != "Node bad: DiskPressure" {
		t.Fatalf("pressure: %+v", p)
	}
	h.Advance(61 * time.Second)
	nr := h.ByClass("node.not-ready")
	if len(nr) != 1 || nr[0].Severity != SevCritical {
		t.Fatalf("not-ready should be critical: %+v", nr)
	}
}

func TestNetworkJoinsUseEndpointSlices(t *testing.T) {
	h := ptest.New(t, ingGVR, svcGVR, sliceGVR)
	svc := &corev1.Service{ObjectMeta: meta("shop", "api"), Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "api"}, ClusterIP: "10.0.0.1"}}
	ing := &networkingv1.Ingress{ObjectMeta: meta("shop", "public"), Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
		Paths: []networkingv1.HTTPIngressPath{{Path: "/", Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "api"}}},
			{Path: "/x", Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "ghost"}}}}}}}}}}
	slice := func(ready bool) *discoveryv1.EndpointSlice {
		m := meta("shop", "api-abc")
		m.Labels = map[string]string{discoveryv1.LabelServiceName: "api"}
		return &discoveryv1.EndpointSlice{ObjectMeta: m, AddressType: discoveryv1.AddressTypeIPv4, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.1.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(ready)}}}}
	}
	h.Put(svcGVR, proj(t, svcGVR, svc))
	h.Put(ingGVR, proj(t, ingGVR, ing))
	h.Put(sliceGVR, proj(t, sliceGVR, slice(false)))
	h.Pass()
	nb := h.ByClass("ingress.no-backend")
	if len(nb) != 1 || nb[0].Count != 2 || nb[0].Message != "Service api has no ready endpoints; Service ghost not found" {
		t.Fatalf("ingress.no-backend: %+v", nb)
	}
	if hasClass(h, "service.no-endpoints") {
		t.Fatal("service.no-endpoints must wait its 2 minute grace")
	}
	h.Advance(2*time.Minute + time.Second)
	if !hasClass(h, "service.no-endpoints") {
		t.Fatal("service.no-endpoints not raised after 2 minutes")
	}
	// the slice turns ready: the Service and the Ingress (joined through the EndpointSlice index) re-evaluate
	h.Put(sliceGVR, proj(t, sliceGVR, slice(true)))
	h.Pass()
	if nb = h.ByClass("ingress.no-backend"); len(nb) != 1 || nb[0].Count != 1 {
		t.Fatalf("ingress should only miss ghost now: %+v", nb)
	}
	h.Advance(31 * time.Second)
	if hasClass(h, "service.no-endpoints") {
		t.Fatal("service.no-endpoints not cleared after the hold")
	}
	// ExternalName services are never flagged
	h.Put(svcGVR, proj(t, svcGVR, &corev1.Service{ObjectMeta: meta("shop", "ext"), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName}}))
	h.Advance(3 * time.Minute)
	for _, p := range h.ByClass("service.no-endpoints") {
		if p.Subject.Name == "ext" {
			t.Fatal("ExternalName service flagged")
		}
	}
}

func TestPVCPendingWithEventEvidence(t *testing.T) {
	h := ptest.New(t, pvcGVR, evGVR)
	now := h.Clk.Now()
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: meta("pay", "data"), Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}}
	pvc.CreationTimestamp = mt(now.Add(-time.Minute))
	h.Put(pvcGVR, proj(t, pvcGVR, pvc))
	h.Put(evGVR, proj(t, evGVR, &corev1.Event{ObjectMeta: meta("pay", "data.1"), Type: "Warning", Reason: "ProvisioningFailed", Message: "storageclass not found",
		InvolvedObject: corev1.ObjectReference{Kind: "PersistentVolumeClaim", Name: "data", Namespace: "pay"}, LastTimestamp: mt(now)}))
	h.Pass()
	if hasClass(h, "pvc.pending") {
		t.Fatal("raised before 5 minutes")
	}
	h.Advance(4*time.Minute + 5*time.Second)
	p := h.ByClass("pvc.pending")
	if len(p) != 1 || p[0].Message != "ProvisioningFailed: storageclass not found" {
		t.Fatalf("%+v", p)
	}
}

func TestHPAClassOffByDefault(t *testing.T) {
	h := ptest.New(t, hpaGVR)
	h.Put(hpaGVR, proj(t, hpaGVR, &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: meta("a", "h"), Spec: autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 5},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetScale"}}}}))
	h.Pass()
	if hasClass(h, "hpa.unable") {
		t.Fatal("hpa.unable is off by default")
	}
	h.Set.ProblemPrefs = map[string]any{"classes": map[string]any{"hpa.unable": true}}
	h.Hub.SettingsChanged(h.Set)
	h.Pass()
	if !hasClass(h, "hpa.unable") {
		t.Fatal("not raised once enabled")
	}
}

func TestClusterUnreachable(t *testing.T) {
	h := ptest.New(t)
	h.E.SetPhase("unreachable", "Cluster unreachable")
	if p := h.ByClass("cluster.unreachable"); len(p) != 1 || p[0].Severity != SevCritical {
		t.Fatalf("%+v", p)
	}
	h.E.SetPhase("ready", "")
	h.Advance(31 * time.Second)
	if hasClass(h, "cluster.unreachable") {
		t.Fatal("not cleared after recovery")
	}
}

func TestEveryClassIsRegisteredOnce(t *testing.T) {
	want := []string{"pod.crashloop", "pod.image-pull", "pod.oomkilled", "pod.pending", "pod.not-ready", "pod.evicted", "pod.stuck-terminating", "pod.config-error", "pod.restarting",
		"workload.unavailable", "workload.rollout-stuck", "job.failed", "cronjob.last-failed", "cronjob.not-scheduling", "hpa.unable",
		"node.not-ready", "node.pressure", "node.cordoned", "pvc.pending", "pv.failed", "ingress.no-backend", "service.no-endpoints", "cluster.unreachable", "events.warning-burst"}
	for _, c := range want {
		d := Lookup(c)
		if d == nil || d.Family == "" || d.Title == "" {
			t.Errorf("class %s missing or incomplete: %+v", c, d)
		}
	}
	off := map[string]bool{"pod.evicted": true, "pod.restarting": true, "hpa.unable": true, "node.cordoned": true, "pv.failed": true, "events.warning-burst": true}
	for c := range off {
		if Lookup(c).Default {
			t.Errorf("%s must be off by default", c)
		}
	}
	_ = fmt.Sprint
}
