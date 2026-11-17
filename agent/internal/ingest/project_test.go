package ingest

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

func ctr(name string, ready bool, restarts int32, st corev1.ContainerState) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, Ready: ready, RestartCount: restarts, State: st}
}

var (
	running = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	waiting = func(r string) corev1.ContainerState {
		return corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: r}}
	}
	terminated = func(r string, code int32, sig int32) corev1.ContainerState {
		return corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: r, ExitCode: code, Signal: sig}}
	}
)

func mkPod(phase corev1.PodPhase, nContainers int, cs ...corev1.ContainerStatus) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns", UID: "u1", ResourceVersion: "7"}}
	for i := 0; i < nContainers; i++ {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "c"})
	}
	p.Status.Phase = phase
	p.Status.ContainerStatuses = cs
	return p
}

func TestStatusReasonAndTone(t *testing.T) {
	now := metav1.Now()
	deleting := mkPod(corev1.PodRunning, 1, ctr("c", true, 0, running))
	deleting.DeletionTimestamp = &now
	evicted := mkPod(corev1.PodFailed, 1)
	evicted.Status.Reason = "Evicted"
	initWait := mkPod(corev1.PodPending, 1, ctr("c", false, 0, waiting("PodInitializing")))
	initWait.Spec.InitContainers = []corev1.Container{{Name: "i1"}, {Name: "i2"}}
	initWait.Status.InitContainerStatuses = []corev1.ContainerStatus{ctr("i1", true, 0, terminated("Completed", 0, 0)), ctr("i2", false, 0, running)}
	initErr := mkPod(corev1.PodPending, 1)
	initErr.Spec.InitContainers = []corev1.Container{{Name: "i1"}}
	initErr.Status.InitContainerStatuses = []corev1.ContainerStatus{ctr("i1", false, 0, terminated("Error", 1, 0))}
	initCrash := mkPod(corev1.PodPending, 1)
	initCrash.Spec.InitContainers = []corev1.Container{{Name: "i1"}}
	initCrash.Status.InitContainerStatuses = []corev1.ContainerStatus{ctr("i1", false, 3, waiting("CrashLoopBackOff"))}
	notReadyDone := mkPod(corev1.PodRunning, 2, ctr("a", true, 0, running), ctr("b", false, 0, terminated("Completed", 0, 0)))
	notReadyDone.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	nodeLost := mkPod(corev1.PodRunning, 1, ctr("c", true, 0, running))
	nodeLost.DeletionTimestamp = &now
	nodeLost.Status.Reason = "NodeLost"

	tests := []struct {
		name   string
		pod    *corev1.Pod
		reason string
		tone   uint8
		ready  int
		total  int
		rst    int
	}{
		{"running ready", mkPod(corev1.PodRunning, 2, ctr("a", true, 1, running), ctr("b", true, 2, running)), "Running", ToneSuccess, 2, 2, 3},
		{"running not ready", mkPod(corev1.PodRunning, 2, ctr("a", true, 0, running), ctr("b", false, 0, running)), "Running", ToneWarning, 1, 2, 0},
		{"crashloop", mkPod(corev1.PodRunning, 1, ctr("c", false, 9, waiting("CrashLoopBackOff"))), "CrashLoopBackOff", ToneDanger, 0, 1, 9},
		{"imagepullbackoff", mkPod(corev1.PodPending, 1, ctr("c", false, 0, waiting("ImagePullBackOff"))), "ImagePullBackOff", ToneDanger, 0, 1, 0},
		{"errimagepull", mkPod(corev1.PodPending, 1, ctr("c", false, 0, waiting("ErrImagePull"))), "ErrImagePull", ToneDanger, 0, 1, 0},
		{"oomkilled", mkPod(corev1.PodRunning, 1, ctr("c", false, 2, terminated("OOMKilled", 137, 0))), "OOMKilled", ToneDanger, 0, 1, 2},
		{"error", mkPod(corev1.PodRunning, 1, ctr("c", false, 0, terminated("Error", 1, 0))), "Error", ToneDanger, 0, 1, 0},
		{"exit code", mkPod(corev1.PodRunning, 1, ctr("c", false, 0, terminated("", 3, 0))), "ExitCode:3", ToneDanger, 0, 1, 0},
		{"signal", mkPod(corev1.PodRunning, 1, ctr("c", false, 0, terminated("", 0, 9))), "Signal:9", ToneDanger, 0, 1, 0},
		{"container creating", mkPod(corev1.PodPending, 1, ctr("c", false, 0, waiting("ContainerCreating"))), "ContainerCreating", ToneWarning, 0, 1, 0},
		{"pending unscheduled", mkPod(corev1.PodPending, 1), "Pending", ToneWarning, 0, 1, 0},
		{"completed", mkPod(corev1.PodSucceeded, 1, ctr("c", false, 0, terminated("Completed", 0, 0))), "Completed", ToneInfo, 0, 1, 0},
		{"succeeded phase", mkPod(corev1.PodSucceeded, 1), "Succeeded", ToneInfo, 0, 1, 0},
		{"failed phase", mkPod(corev1.PodFailed, 1), "Failed", ToneDanger, 0, 1, 0},
		{"evicted", evicted, "Evicted", ToneDanger, 0, 1, 0},
		{"terminating", deleting, "Terminating", ToneWarning, 1, 1, 0},
		{"node lost", nodeLost, "Unknown", ToneWarning, 1, 1, 0},
		{"init progress", initWait, "Init:1/2", ToneWarning, 0, 1, 0},
		{"init error", initErr, "Init:Error", ToneDanger, 0, 1, 0},
		{"init crashloop", initCrash, "Init:CrashLoopBackOff", ToneDanger, 0, 1, 3},
		{"completed but one running", notReadyDone, "NotReady", ToneWarning, 1, 2, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Project(tc.pod)
			if got := r.Status().Value(); got != tc.reason {
				t.Errorf("reason = %q, want %q", got, tc.reason)
			}
			if r.Tone() != tc.tone {
				t.Errorf("tone = %d, want %d", r.Tone(), tc.tone)
			}
			if int(r.Ready()) != tc.ready || int(r.Total()) != tc.total || int(r.Restarts()) != tc.rst {
				t.Errorf("ready/total/restarts = %d/%d/%d, want %d/%d/%d", r.Ready(), r.Total(), r.Restarts(), tc.ready, tc.total, tc.rst)
			}
		})
	}
}

func TestProjectIdentityAndMeta(t *testing.T) {
	p := mkPod(corev1.PodRunning, 1, ctr("c", true, 0, running))
	p.Spec.NodeName = "node-1"
	p.Annotations = map[string]string{"big": "x"}
	p.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "m"}}
	r := Project(p)
	if r.UID() != "u1" || r.Name() != "p" || r.GetNamespace() != "ns" || r.GetResourceVersion() != "7" || r.Node().Value() != "node-1" {
		t.Fatalf("bad projection: %+v", r)
	}
	if r.GetAnnotations() != nil || r.GetLabels() != nil || r.GetManagedFields() != nil {
		t.Fatal("annotations/labels/managedFields must be dropped")
	}
}

type fakeAPI struct {
	pages []*corev1.PodList
	w     *watch.FakeWatcher
}

func (f *fakeAPI) List(_ context.Context, o metav1.ListOptions) (*corev1.PodList, error) {
	i := 0
	if o.Continue != "" {
		i = int(o.Continue[0] - '0')
	}
	return f.pages[i], nil
}
func (f *fakeAPI) Watch(context.Context, metav1.ListOptions) (watch.Interface, error) {
	return f.w, nil
}

func TestListerWatcherProjectsBeforeReflector(t *testing.T) {
	a := mkPod(corev1.PodRunning, 1, ctr("c", true, 0, running))
	a.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "m"}}
	api := &fakeAPI{pages: []*corev1.PodList{{ListMeta: metav1.ListMeta{ResourceVersion: "9"}, Items: []corev1.Pod{*a}}}, w: watch.NewFake()}
	n := 0
	lw := &ListerWatcher{API: api, Hooks: Hooks{OnSyncProgress: func(c int) { n = c }}}
	obj, err := lw.List(metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	l := obj.(*PodRowList)
	if len(l.Items) != 1 || l.ResourceVersion != "9" || n != 1 {
		t.Fatalf("list: %+v n=%d", l, n)
	}
	w, err := lw.Watch(metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	go api.w.Modify(a)
	select {
	case ev := <-w.ResultChan():
		if _, ok := ev.Object.(*PodRow); !ok {
			t.Fatalf("watch event not projected: %T", ev.Object)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
}
