package ingest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/watch"
)

func i32p(v int32) *int32 { return &v }

func cellStrings(m *Module, r *Row) string {
	var out []string
	for _, c := range m.Cols {
		if c.Src != SrcCell {
			continue
		}
		cell := r.Cell(c.Idx)
		switch c.Type {
		case "int", "time":
			out = append(out, fmt.Sprint(cell.N))
		default:
			out = append(out, cell.S.Value())
		}
	}
	return strings.Join(out, "|")
}

func om(name, ns string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: ns, UID: "uid-" + "x", ResourceVersion: "5",
		CreationTimestamp: metav1.NewTime(time.Unix(1700000000, 0))}
}

func TestCuratedProjections(t *testing.T) {
	ctrl := true
	cases := []struct {
		gvr  string
		obj  runtime.Object
		want string
		tone uint8
	}{
		{"apps/v1/deployments", &appsv1.Deployment{ObjectMeta: om("d", "ns"),
			Spec:   appsv1.DeploymentSpec{Replicas: i32p(3), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "a:1"}, {Image: "b:2"}, {Image: "a:1"}}}}},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 2, UpdatedReplicas: 3, AvailableReplicas: 2}}, "2/3|3|2|a:1,b:2", ToneWarning},
		{"apps/v1/deployments", &appsv1.Deployment{ObjectMeta: om("d", "ns"), Spec: appsv1.DeploymentSpec{Replicas: i32p(2)}}, "0/2|0|0|", ToneDanger},
		{"apps/v1/statefulsets", &appsv1.StatefulSet{ObjectMeta: om("s", "ns"), Spec: appsv1.StatefulSetSpec{Replicas: i32p(2)},
			Status: appsv1.StatefulSetStatus{ReadyReplicas: 2}}, "2/2|", ToneSuccess},
		{"apps/v1/daemonsets", &appsv1.DaemonSet{ObjectMeta: om("ds", "ns"), Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 5, CurrentNumberScheduled: 5,
			NumberReady: 4, UpdatedNumberScheduled: 5, NumberAvailable: 4}}, "5|5|4|5|4|", ToneWarning},
		{"apps/v1/replicasets", &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "rs", Namespace: "ns", UID: "u", ResourceVersion: "1",
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "d", Controller: &ctrl}}},
			Spec: appsv1.ReplicaSetSpec{Replicas: i32p(2)}, Status: appsv1.ReplicaSetStatus{Replicas: 2, ReadyReplicas: 2}}, "2|2|2", ToneSuccess},
		{"batch/v1/jobs", &batchv1.Job{ObjectMeta: om("j", "ns"), Spec: batchv1.JobSpec{Completions: i32p(1)},
			Status: batchv1.JobStatus{Succeeded: 1, StartTime: &metav1.Time{Time: time.Unix(100, 0)}, CompletionTime: &metav1.Time{Time: time.Unix(142, 0)},
				Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}}, "1/1|42s|Complete", ToneSuccess},
		{"batch/v1/jobs", &batchv1.Job{ObjectMeta: om("j", "ns"), Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}}}, "0/1||Failed", ToneDanger},
		{"batch/v1/cronjobs", &batchv1.CronJob{ObjectMeta: om("c", "ns"), Spec: batchv1.CronJobSpec{Schedule: "*/5 * * * *", Suspend: func() *bool { b := true; return &b }()},
			Status: batchv1.CronJobStatus{Active: []corev1.ObjectReference{{}}, LastScheduleTime: &metav1.Time{Time: time.UnixMilli(12345000)}}}, "*/5 * * * *|true|1|12345000", ToneWarning},
		{"v1/services", &corev1.Service{ObjectMeta: om("s", "ns"), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, ClusterIP: "10.0.0.1",
			Selector: map[string]string{"b": "2", "a": "1"}, Ports: []corev1.ServicePort{{Port: 80, NodePort: 30080, Protocol: "TCP"}}}}, "LoadBalancer|10.0.0.1||80:30080/TCP|a=1,b=2", ToneWarning},
		{"networking.k8s.io/v1/ingresses", &networkingv1.Ingress{ObjectMeta: om("i", "ns"), Spec: networkingv1.IngressSpec{IngressClassName: ptr("nginx"),
			Rules: []networkingv1.IngressRule{{Host: "a.example.com"}}, TLS: []networkingv1.IngressTLS{{}}},
			Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "1.2.3.4"}}}}},
			"nginx|a.example.com|1.2.3.4|80, 443", ToneNeutral},
		{"discovery.k8s.io/v1/endpointslices", &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: "ns", UID: "u", ResourceVersion: "1",
			Labels: map[string]string{discoveryv1.LabelServiceName: "web"}}, AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints: []discoveryv1.Endpoint{{}, {Conditions: discoveryv1.EndpointConditions{Ready: func() *bool { b := false; return &b }()}}}}, "web|IPv4|1|2", ToneWarning},
		{"networking.k8s.io/v1/networkpolicies", &networkingv1.NetworkPolicy{ObjectMeta: om("n", "ns"), Spec: networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{"Ingress", "Egress"}, Ingress: make([]networkingv1.NetworkPolicyIngressRule, 2)}}, "<all pods>|Ingress,Egress|2|0", ToneNeutral},
		{"v1/nodes", &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", UID: "u", ResourceVersion: "1", Labels: map[string]string{"node-role.kubernetes.io/worker": "", "node-role.kubernetes.io/gpu": ""}},
			Spec: corev1.NodeSpec{Unschedulable: true, Taints: []corev1.Taint{{}}},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
				Addresses:   []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.1.1.1"}},
				NodeInfo:    corev1.NodeSystemInfo{KubeletVersion: "v1.31.0", OSImage: "Ubuntu", KernelVersion: "6.8", ContainerRuntimeVersion: "containerd://1.7"},
				Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi"), corev1.ResourcePods: resource.MustParse("110")}}},
			"Ready,SchedulingDisabled|gpu,worker|v1.31.0|10.1.1.1|Ubuntu|6.8|containerd://1.7|110|1", ToneWarning},
		{"v1/namespaces", &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns", UID: "u", ResourceVersion: "1"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating}}, "Terminating", ToneWarning},
		{"v1/persistentvolumeclaims", &corev1.PersistentVolumeClaim{ObjectMeta: om("p", "ns"), Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-1", StorageClassName: ptr("fast")},
			Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce, corev1.ReadWriteMany}}}, "Bound|pv-1|10Gi|RWO,RWX|fast", ToneSuccess},
		{"v1/persistentvolumes", &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv", UID: "u", ResourceVersion: "1"},
			Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{Namespace: "ns", Name: "c"}, PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
				Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}, StorageClassName: "std"},
			Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased}}, "Released|ns/c|1Gi||Retain|std", ToneWarning},
		{"v1/events", &corev1.Event{ObjectMeta: om("e", "ns"), Type: "Warning", Reason: "BackOff", Message: "boom", Count: 4,
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p"}, LastTimestamp: metav1.Time{Time: time.UnixMilli(5000)}}, "Warning|BackOff|Pod/p|boom|4|5000|", ToneWarning},
		{"v1/serviceaccounts", &corev1.ServiceAccount{ObjectMeta: om("sa", "ns"), Secrets: []corev1.ObjectReference{{}, {}}}, "2", ToneNeutral},
		{"autoscaling/v2/horizontalpodautoscalers", &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: om("h", "ns"),
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "api"}, MinReplicas: i32p(2), MaxReplicas: 5,
				Metrics: []autoscalingv2.MetricSpec{{Type: "Resource", Resource: &autoscalingv2.ResourceMetricSource{Name: "cpu", Target: autoscalingv2.MetricTarget{AverageUtilization: i32p(70)}}}}},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 5, CurrentMetrics: []autoscalingv2.MetricStatus{{Type: "Resource",
				Resource: &autoscalingv2.ResourceMetricStatus{Name: "cpu", Current: autoscalingv2.MetricValueStatus{AverageUtilization: i32p(90)}}}}}},
			"Deployment/api|cpu: 90%/70%|2|5|5", ToneWarning},
		{"policy/v1/poddisruptionbudgets", &policyv1.PodDisruptionBudget{ObjectMeta: om("pdb", "ns"), Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: &intstr.IntOrString{Type: intstr.String, StrVal: "50%"}},
			Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 0, CurrentHealthy: 2, ExpectedPods: 2}}, "50%||0|2|2", ToneWarning},
	}
	for _, c := range cases {
		m := Lookup(c.gvr)
		if m == nil || !m.Curated {
			t.Fatalf("%s not registered", c.gvr)
		}
		r := m.ProjectObj(c.obj)
		if r == nil {
			t.Fatalf("%s: projection returned nil", c.gvr)
		}
		if got := cellStrings(m, r); got != c.want || r.Tone() != c.tone {
			t.Errorf("%s: cells %q tone %d, want %q tone %d", c.gvr, got, r.Tone(), c.want, c.tone)
		}
		if len(r.cells) != m.NCells {
			t.Errorf("%s: row has %d cells, module %d", c.gvr, len(r.cells), m.NCells)
		}
		if r.Name() == "" {
			t.Errorf("%s: empty name", c.gvr)
		}
	}
	// ReplicaSet owner column
	rs := Lookup("apps/v1/replicasets")
	if r := rs.ProjectObj(cases[4].obj); r.OwnerName().Value() != "d" {
		t.Errorf("owner = %q", r.OwnerName().Value())
	}
	// Events are Warning-only through the list options.
	opts := metav1.ListOptions{}
	Lookup("v1/events").Tweak(&opts)
	if opts.FieldSelector != "type=Warning" {
		t.Errorf("events selector %q", opts.FieldSelector)
	}
	if n := len(Tier0Modules()); n != 14 || Tier0Modules()[0].GVR != PodsGVR {
		t.Errorf("tier0 modules: %d", n)
	}
}

func ptr[T any](v T) *T { return &v }

func TestPodNativeSidecarsAndGating(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	started := true
	// Running pod with a started, ready sidecar and one regular container: Running 2/2.
	p := &corev1.Pod{ObjectMeta: om("p", "ns"), Status: corev1.PodStatus{Phase: corev1.PodRunning,
		Conditions: []corev1.PodCondition{{Type: corev1.PodInitialized, Status: corev1.ConditionTrue}}}}
	p.Spec.InitContainers = []corev1.Container{{Name: "proxy", RestartPolicy: &always}}
	p.Spec.Containers = []corev1.Container{{Name: "app"}}
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "proxy", Ready: true, Started: &started, RestartCount: 2,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Ready: true, RestartCount: 1, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	reason, restarts, ready, total := PodStatus(p)
	if reason != "Running" || ready != 2 || total != 2 || restarts != 3 {
		t.Fatalf("sidecar pod: %s %d/%d restarts=%d", reason, ready, total, restarts)
	}
	if StatusTone(reason, p.Status.Phase, ready, total) != ToneSuccess {
		t.Fatal("tone")
	}
	// A regular init container still running: Init:0/2 (sidecar skipped only when started).
	q := p.DeepCopy()
	q.Spec.InitContainers = append([]corev1.Container{{Name: "migrate"}}, q.Spec.InitContainers...)
	q.Status.Conditions = nil
	q.Status.InitContainerStatuses = append([]corev1.ContainerStatus{{Name: "migrate", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}, q.Status.InitContainerStatuses...)
	if r, _, _, _ := PodStatus(q); r != "Init:0/2" {
		t.Fatalf("initializing pod: %s", r)
	}
	// Terminating applies only to non-terminal phases.
	now := metav1.Now()
	d := p.DeepCopy()
	d.DeletionTimestamp = &now
	if r, _, _, _ := PodStatus(d); r != "Terminating" {
		t.Fatalf("deleting running pod: %s", r)
	}
	d.Status.Phase = corev1.PodSucceeded
	d.Status.ContainerStatuses[0].Ready = false
	d.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}
	if r, _, _, _ := PodStatus(d); r != "Completed" {
		t.Fatalf("deleting succeeded pod: %s", r)
	}
	// SchedulingGated.
	g := &corev1.Pod{ObjectMeta: om("g", "ns"), Status: corev1.PodStatus{Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonSchedulingGated}}}}
	g.Spec.Containers = []corev1.Container{{Name: "c"}}
	r, _, _, tt := PodStatus(g)
	if r != "SchedulingGated" || StatusTone(r, g.Status.Phase, 0, tt) != ToneWarning {
		t.Fatalf("gated: %s", r)
	}
}

type recAPI struct {
	opts   []metav1.ListOptions
	events []watch.Event
}

func (a *recAPI) List(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
	a.opts = append(a.opts, o)
	return &corev1.PodList{Items: []corev1.Pod{*podFixture("a"), *podFixture("b")}}, nil
}

func (a *recAPI) Watch(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
	ch := make(chan watch.Event, len(a.events))
	for _, e := range a.events {
		ch <- e
	}
	close(ch)
	return watch.NewProxyWatcher(ch), nil
}

func podFixture(name string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: om(name, "ns")}
	p.UID = "u-" + types.UID(name)
	p.Spec.Containers = []corev1.Container{{Name: "c"}}
	p.Status.Phase = corev1.PodRunning
	return p
}

func TestListMapsRVZeroAndReportsRows(t *testing.T) {
	api := &recAPI{}
	var got []*Row
	lw := &ListerWatcher{API: api, Hooks: Hooks{OnRows: func(r []*Row) { got = append(got, r...) }}}
	if _, err := lw.ListWithContext(context.Background(), metav1.ListOptions{ResourceVersion: "0", Limit: 500}); err != nil {
		t.Fatal(err)
	}
	if api.opts[0].ResourceVersion != "" || api.opts[0].Limit != 500 {
		t.Fatalf("list opts: %+v", api.opts[0])
	}
	if len(got) != 2 {
		t.Fatalf("OnRows got %d rows", len(got))
	}
}

func TestWatchErrorEventsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		status *metav1.Status
		check  func(error) bool
	}{
		{&metav1.Status{Status: "Failure", Code: 401, Reason: metav1.StatusReasonUnauthorized, Message: "no"}, apierrors.IsUnauthorized},
		{&metav1.Status{Status: "Failure", Code: 403, Reason: metav1.StatusReasonForbidden, Message: "no"}, apierrors.IsForbidden},
		{&metav1.Status{Status: "Failure", Code: 410, Reason: metav1.StatusReasonExpired, Message: "old"}, apierrors.IsResourceExpired},
	} {
		api := &recAPI{events: []watch.Event{{Type: watch.Error, Object: tc.status}}}
		var gotErr error
		lw := &ListerWatcher{API: api, Hooks: Hooks{OnError: func(e error) { gotErr = e }}}
		w, err := lw.WatchWithContext(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		<-w.ResultChan()
		for range w.ResultChan() {
		}
		if gotErr == nil || !tc.check(gotErr) {
			t.Errorf("status %d: error %v not classified", tc.status.Code, gotErr)
		}
	}
}

func TestWatchBatchesInitialRows(t *testing.T) {
	var evs []watch.Event
	for i := 0; i < 300; i++ {
		evs = append(evs, watch.Event{Type: watch.Added, Object: podFixture(fmt.Sprint("p", i))})
	}
	bm := podFixture("bm")
	bm.Annotations = map[string]string{metav1.InitialEventsAnnotationKey: "true"}
	evs = append(evs, watch.Event{Type: watch.Bookmark, Object: bm})
	var batches []int
	lw := &ListerWatcher{API: &recAPI{events: evs}, Hooks: Hooks{OnRows: func(r []*Row) { batches = append(batches, len(r)) }}}
	yes := true
	w, _ := lw.WatchWithContext(context.Background(), metav1.ListOptions{SendInitialEvents: &yes})
	n := 0
	for range w.ResultChan() {
		n++
	}
	if n != 301 || len(batches) != 2 || batches[0] != 256 || batches[1] != 44 {
		t.Fatalf("events=%d batches=%v", n, batches)
	}
}
