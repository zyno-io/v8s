package drainer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/jobs"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func owned(ns, name, kind string, lbl map[string]string) corev1.Pod {
	t := true
	p := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: lbl}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if kind != "" {
		p.OwnerReferences = []metav1.OwnerReference{{Kind: kind, Name: kind + "-x", Controller: &t}}
	}
	return p
}

func names(l []protocol.DrainPod) string {
	var s []string
	for _, p := range l {
		s = append(s, p.Namespace+"/"+p.Name)
	}
	return strings.Join(s, ",")
}

func TestClassify(t *testing.T) {
	web := map[string]string{"app": "web"}
	emptyDir := owned("a", "cache", "ReplicaSet", nil)
	emptyDir.Spec.Volumes = []corev1.Volume{{Name: "s", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	mirror := owned("kube-system", "etcd", "", nil)
	mirror.Annotations = map[string]string{corev1.MirrorPodAnnotationKey: "x"}
	done := owned("a", "job-done", "Job", nil)
	done.Status.Phase = corev1.PodSucceeded
	pods := []corev1.Pod{
		owned("a", "web-1", "ReplicaSet", web), owned("a", "api-1", "ReplicaSet", map[string]string{"app": "api"}),
		owned("a", "ds-1", "DaemonSet", nil), owned("a", "lone", "", nil), emptyDir, mirror, done,
	}
	pdbs := []policyv1.PodDisruptionBudget{{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "web-pdb"},
		Spec:   policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: web}},
		Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 0}}}
	r := Classify(pods, pdbs, protocol.DrainOptions{})
	if names(r.Evict) != "a/api-1,a/cache" || names(r.Blocked) != "a/web-1" || r.Blocked[0].PDB.Name != "web-pdb" || r.Blocked[0].PDB.DisruptionsAllowed != 0 {
		t.Fatalf("evict=%s blocked=%s", names(r.Evict), names(r.Blocked))
	}
	if names(r.Unmanaged) != "a/lone" || names(r.EmptyDir) != "a/cache" || names(r.DaemonSets) != "a/ds-1" || names(r.Mirror) != "kube-system/etcd" || names(r.Finished) != "a/job-done" {
		t.Fatalf("%+v", r)
	}
	// ignoreDaemonSets off: the DaemonSet pod is evicted (kubectl would refuse; the preflight shows it plainly)
	no := false
	r = Classify(pods, pdbs, protocol.DrainOptions{IgnoreDaemonSets: &no})
	if len(r.DaemonSets) != 0 || strings.Contains(names(r.Evict), "a/ds-1") || !strings.Contains(names(r.Blocked), "a/ds-1") {
		t.Fatalf("evict=%s blocked=%s", names(r.Evict), names(r.Blocked))
	}
	// disable eviction: PDBs are not honoured, so nothing is blocked
	r = Classify(pods, pdbs, protocol.DrainOptions{DisableEviction: true})
	if len(r.Blocked) != 0 || !strings.Contains(names(r.Evict), "a/web-1") {
		t.Fatalf("%+v", r)
	}
	// a PDB that still allows a disruption does not block
	pdbs[0].Status.DisruptionsAllowed = 1
	if r = Classify(pods, pdbs, protocol.DrainOptions{}); len(r.Blocked) != 0 {
		t.Fatal("blocked by a PDB with room")
	}
	// pod selector narrows the drain
	if r = Classify(pods, pdbs, protocol.DrainOptions{PodSelector: "app=api"}); names(r.Evict) != "a/api-1" || len(r.Unmanaged) != 0 {
		t.Fatalf("%+v", r)
	}
}

type fakeRouter struct{ be kube.Conn }

func (f fakeRouter) Route(context.Context, string, string, string, string, string, string) (*ops.Routed, error) {
	return &ops.Routed{ContextID: "prod-us", Conn: f.be}, nil
}

func newSvc(t *testing.T) (*Service, kube.Conn) {
	t.Helper()
	src := fake.NewSource(fake.Config{Clusters: 1, Pods: 100})
	t.Cleanup(func() { src.Release("prod-us") })
	be, err := src.Backend("prod-us")
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Router: fakeRouter{be}, Conn: func(string) (kube.Conn, error) { return be, nil }, EvictRetryDelay: 10 * time.Millisecond}), be
}

func TestPreflightOnFakeNode(t *testing.T) {
	s, _ := newSvc(t)
	r, err := s.Preflight(context.Background(), protocol.DrainParams{Cluster: "prod-us", Node: "node-3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Blocked) != 3 || r.Blocked[0].PDB == nil || len(r.DaemonSets) != 1 || len(r.Unmanaged) != 1 || len(r.EmptyDir) != 1 || len(r.Finished) != 1 || r.Cordoned {
		t.Fatalf("%+v", r)
	}
	if _, err := s.Preflight(context.Background(), protocol.DrainParams{Cluster: "prod-us", Node: "missing"}); err == nil {
		t.Fatal("unknown node")
	}
	if _, err := s.Preflight(context.Background(), protocol.DrainParams{Cluster: "prod-us"}); err == nil {
		t.Fatal("node required")
	}
}

func runDrain(t *testing.T, s *Service, p protocol.DrainParams) protocol.JobFrame {
	t.Helper()
	run, prep, err := s.Prepare(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.New(context.Background(), nil)
	id := m.Start("", "drain", prep.Cluster, prep.Subject, run)
	m.Wait(id)
	f, _ := m.Get(id)
	return f
}

func TestDrainWaitsOnPDBThenEvictsAndLeavesNodeCordoned(t *testing.T) {
	s, be := newSvc(t)
	yes := true
	f := runDrain(t, s, protocol.DrainParams{Cluster: "prod-us", Node: "node-3", Options: protocol.DrainOptions{Force: true, DeleteEmptyDirData: true, IgnoreDaemonSets: &yes, TimeoutSeconds: 30}})
	if f.State != protocol.JobDone {
		t.Fatalf("%s %+v", f.State, f.Error)
	}
	var waited, evicted, cordoned bool
	for _, p := range f.Progress {
		waited = waited || p.State == "waiting-pdb"
		evicted = evicted || (p.State == "evicted" && strings.Contains(p.Item, "web-5d9f7c"))
		cordoned = cordoned || p.State == "cordoned"
	}
	if !waited || !evicted || !cordoned || f.UsedContext != "prod-us" {
		t.Fatalf("progress %+v", f.Progress)
	}
	rt, _ := kube.RuntimeOf(be)
	node, _ := rt.Clientset().CoreV1().Nodes().Get(context.Background(), "node-3", metav1.GetOptions{})
	if !node.Spec.Unschedulable {
		t.Fatal("node not cordoned")
	}
	pods, _ := rt.Clientset().CoreV1().Pods("team-1").List(context.Background(), metav1.ListOptions{FieldSelector: "spec.nodeName=node-3"})
	if len(pods.Items) != 0 {
		t.Fatalf("%d pods left", len(pods.Items))
	}
	// idempotent: draining again is fine
	if f = runDrain(t, s, protocol.DrainParams{Cluster: "prod-us", Node: "node-3", Options: protocol.DrainOptions{Force: true, DeleteEmptyDirData: true}}); f.State != protocol.JobDone {
		t.Fatalf("re-run %s %+v", f.State, f.Error)
	}
}

func TestDrainRefusesUnmanagedWithoutForceAndKeepsCordon(t *testing.T) {
	s, be := newSvc(t)
	f := runDrain(t, s, protocol.DrainParams{Cluster: "prod-us", Node: "node-3"})
	if f.State != protocol.JobFailed || f.Error == nil || !strings.Contains(f.Error.Message, "cannot be drained") {
		t.Fatalf("%s %+v", f.State, f.Error)
	}
	rt, _ := kube.RuntimeOf(be)
	if node, _ := rt.Clientset().CoreV1().Nodes().Get(context.Background(), "node-3", metav1.GetOptions{}); !node.Spec.Unschedulable {
		t.Fatal("the safe state is a cordoned node")
	}
}

func TestDrainCancelLeavesNodeCordoned(t *testing.T) {
	src := fake.NewSource(fake.Config{Clusters: 1, Pods: 100})
	defer src.Release("prod-us")
	be, _ := src.Backend("prod-us")
	s := New(Config{Router: fakeRouter{be}, Conn: func(string) (kube.Conn, error) { return be, nil }, EvictRetryDelay: 200 * time.Millisecond})
	run, prep, err := s.Prepare(context.Background(), protocol.DrainParams{Cluster: "prod-us", Node: "node-3", Options: protocol.DrainOptions{Force: true, DeleteEmptyDirData: true, TimeoutSeconds: 60}})
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.New(context.Background(), nil)
	id := m.Start("", "drain", prep.Cluster, prep.Subject, run)
	time.Sleep(100 * time.Millisecond)
	_ = m.Cancel(id)
	m.Wait(id)
	f, _ := m.Get(id)
	if f.State != protocol.JobCancelled {
		t.Fatalf("%s %+v", f.State, f.Error)
	}
	rt, _ := kube.RuntimeOf(be)
	if node, _ := rt.Clientset().CoreV1().Nodes().Get(context.Background(), "node-3", metav1.GetOptions{}); !node.Spec.Unschedulable {
		t.Fatal("cancel must leave the node cordoned")
	}
}
