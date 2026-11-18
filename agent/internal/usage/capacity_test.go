package usage

import (
	"fmt"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

func capNode(name, cpu, mem string) *ingest.Row {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: k8stypes.UID("n-" + name), ResourceVersion: "1"},
		Status: corev1.NodeStatus{Allocatable: usageList(cpu, mem)}}
	return ingest.Lookup("v1/nodes").ProjectObj(n)
}

func capPod(uid, node string, phase corev1.PodPhase, cpuReq, memReq, cpuLim, memLim string) *ingest.Row {
	rl := func(c, m string) corev1.ResourceList {
		l := corev1.ResourceList{}
		if c != "" {
			l[corev1.ResourceCPU] = resource.MustParse(c)
		}
		if m != "" {
			l[corev1.ResourceMemory] = resource.MustParse(m)
		}
		return l
	}
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p-" + uid, Namespace: "ns", UID: k8stypes.UID(uid), ResourceVersion: "1"},
		Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "c",
			Resources: corev1.ResourceRequirements{Requests: rl(cpuReq, memReq), Limits: rl(cpuLim, memLim)}}}},
		Status: corev1.PodStatus{Phase: phase}}
	return ingest.Project(p)
}

func TestCapacityModuleIsSyntheticAndHidden(t *testing.T) {
	m := ingest.Lookup(protocol.CapacityGVR)
	if m == nil || !m.Synthetic || m.Virtual || m.Curated || m.Usage != "capacity" {
		t.Fatalf("capacity module = %+v", m)
	}
	for _, v := range ingest.VirtualModules() {
		if v.GVR == protocol.CapacityGVR {
			t.Fatal("the capacity view must not be listed as a kind")
		}
	}
	if len(m.Cols) != 2+numCapCells {
		t.Fatalf("columns = %d, want %d", len(m.Cols), 2+numCapCells)
	}
}

func TestScanCapacity(t *testing.T) {
	nodes, nitems := fill(t, capNode("n1", "4", "8Gi"), capNode("n2", "8", "16Gi"), capNode("n3", "2", "4Gi"))
	pods, _ := fill(t,
		capPod("a", "n1", corev1.PodRunning, "500m", "1Gi", "1", "2Gi"),
		capPod("b", "n1", corev1.PodRunning, "250m", "", "", "1Gi"),    // no CPU limit
		capPod("c", "n2", corev1.PodRunning, "", "", "", ""),           // best effort: counted, no limits
		capPod("d", "", corev1.PodPending, "1", "1Gi", "1", "1Gi"),     // unscheduled: skipped
		capPod("e", "n2", corev1.PodSucceeded, "2", "2Gi", "2", "2Gi"), // finished: skipped
		capPod("f", "n3", corev1.PodFailed, "2", "2Gi", "2", "2Gi"),    // finished: skipped
		capPod("g", "n3", corev1.PodRunning, "100m", "128Mi", "200m", "256Mi"))
	var buf []store.Item

	c := ScanCapacity(pods, nodes, nil, &buf)
	if c.Nodes != 3 || c.CPUAlloc != 14000 || c.MemAlloc != 28<<30 {
		t.Errorf("allocatable = %+v (the denominator is every node)", c)
	}
	if c.Pods != 4 || c.CPUReq != 850 || c.MemReq != 1<<30+128<<20 || c.CPULim != 1200 || c.MemLim != 2<<30+1<<30+256<<20 || c.PodsNoCPU != 2 || c.PodsNoMem != 1 {
		t.Errorf("pods = %+v", c)
	}
	if c.HasUse {
		t.Error("no node metrics: usage is unknown")
	}
	r := c.Row("prod", 1)
	if got := fmt.Sprint(r.Cell(CellCPUUse).N, r.Cell(CellMemUse).N); got != "-1 -1" {
		t.Errorf("unknown usage cells = %s", got)
	}

	// With a node snapshot built from the same store: usage sums the nodes that report.
	sn := BuildNodes(nodes, nitems, []NodeSample{{Name: "n1", At: 1, CPU: 1000, MemKi: 2 << 20}, {Name: "n2", At: 1, CPU: 500, MemKi: 1 << 20}}, nil, VerBase)
	c = ScanCapacity(pods, nodes, sn, &buf)
	if !c.HasUse || c.NodesReporting != 2 || c.CPUUse != 1500 || c.MemUse != 3<<30 {
		t.Errorf("usage = %+v", c)
	}
	r = c.Row("prod", 2)
	want := []int64{3, 2, 14000, 28 << 10, 850, 1152, 1200, 3328, 1500, 3072, 4, 2, 1}
	for i, w := range want {
		if got := r.Cell(i).N; got != w {
			t.Errorf("cell %d (%s) = %d, want %d", i, ingest.Lookup(protocol.CapacityGVR).Cols[2+i].Key, got, w)
		}
	}

	// A snapshot of another store does not count.
	other, oitems := fill(t, capNode("n1", "4", "8Gi"))
	osn := BuildNodes(other, oitems, []NodeSample{{Name: "n1", At: 1, CPU: 1000, MemKi: 1}}, nil, VerBase)
	if c = ScanCapacity(pods, nodes, osn, &buf); c.HasUse || c.NodesReporting != 0 {
		t.Errorf("a snapshot of another store must be ignored: %+v", c)
	}
}

func TestCapacityRowEqualityDrivesPublishing(t *testing.T) {
	nodes, _ := fill(t, capNode("n1", "4", "8Gi"))
	pods, _ := fill(t, capPod("a", "n1", corev1.PodRunning, "500m", "1Gi", "1", "2Gi"))
	var buf []store.Item
	a := ScanCapacity(pods, nodes, nil, &buf).Row("prod", 1)
	b := ScanCapacity(pods, nodes, nil, &buf).Row("prod", 2)
	if !a.CellsEqual(b) {
		t.Error("identical totals must produce equal rows (the store then publishes nothing)")
	}
	if a.UID() != "capacity/prod" || a.Name() != "prod" {
		t.Errorf("uid %q name %q", a.UID(), a.Name())
	}
}

// BenchmarkScanCapacity50k5k is the throttled capacity scan at the size the plan names: 50k pods on 5k nodes, with
// node usage.
func BenchmarkScanCapacity50k5k(b *testing.B) {
	nodes := store.New()
	var nl []interface{}
	var samples []NodeSample
	for i := 0; i < 5000; i++ {
		name := fmt.Sprintf("node-%d", i)
		nl = append(nl, capNode(name, "16", "64Gi"))
		samples = append(samples, NodeSample{Name: name, At: 1, CPU: 3000, MemKi: 20 << 20})
	}
	_ = nodes.Replace(nl, "1")
	pods := store.New()
	var pl []interface{}
	for i := 0; i < 50000; i++ {
		pl = append(pl, capPod(fmt.Sprint("p", i), fmt.Sprintf("node-%d", i%5000), corev1.PodRunning, "100m", "128Mi", "500m", "256Mi"))
	}
	_ = pods.Replace(pl, "1")
	sn := BuildNodes(nodes, nodes.Snapshot(nil, nil), samples, nil, VerBase)
	var buf []store.Item
	b.ReportAllocs()
	b.ResetTimer()
	var c Capacity
	for i := 0; i < b.N; i++ {
		c = ScanCapacity(pods, nodes, sn, &buf)
	}
	if c.Pods != 50000 {
		b.Fatal(c.Pods)
	}
}
