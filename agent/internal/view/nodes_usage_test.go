package view

import (
	"slices"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/internal/usage"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func unode(name, cpu, mem string) *ingest.Row {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("u-" + name), ResourceVersion: "1"}}
	if cpu != "" {
		n.Status.Allocatable = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}
	}
	return ingest.Lookup("v1/nodes").ProjectObj(n)
}

func attachKind(e *Engine, cluster, gvr string, rows ...*ingest.Row) *store.Store {
	st := store.New()
	for _, r := range rows {
		_ = st.Add(r)
	}
	e.AttachKind(cluster, gvr, st)
	return st
}

// The nodes page: usage joins by the node's own allocatable; "cpu" sorts numerically (it was a string); a node with no
// sample sorts last in both directions; usage is nil unless the subscription asked for it.
func TestNodesUsageColumnsSortAndEncode(t *testing.T) {
	e := newEngine()
	st := attachKind(e, "c1", "v1/nodes", unode("a", "4", "8Gi"), unode("b", "16", "64Gi"), unode("c", "9", "32Gi"), unode("d", "8", "16Gi"))
	samples := []usage.NodeSample{
		{Name: "a", At: 1, CPU: 2000, MemKi: 4 << 20},  // 50 % cpu, 50 % mem
		{Name: "b", At: 1, CPU: 1600, MemKi: 8 << 20},  // 10 %, 12.5 %
		{Name: "c", At: 1, CPU: 8100, MemKi: 30 << 20}, // 90 %, 93.75 %
	} // d has no sample
	e.SetUsage("c1", "v1/nodes", usage.BuildNodes(st, st.Snapshot(nil, nil), samples, nil, usage.VerBase))

	mod := ingest.Lookup("v1/nodes")
	order := func(col string, desc bool) []string {
		sub, err := e.Subscribe(protocol.ViewQuery{GVR: "v1/nodes", Sort: []protocol.Sort{{Col: col, Desc: desc}}}, 1, win(0, 10))
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		return entryNames(sub.v)
	}
	for _, c := range []struct {
		col  string
		desc bool
		want []string
	}{
		{"cpuPctAlloc", false, []string{"b", "a", "c", "d"}},
		{"cpuPctAlloc", true, []string{"c", "a", "b", "d"}},
		{"memPctAlloc", true, []string{"c", "a", "b", "d"}},
		{"cpuUse", true, []string{"c", "a", "b", "d"}},
		{"cpu", false, []string{"a", "d", "c", "b"}}, // numeric allocatable: 4, 8, 9, 16 cores ("16000m" < "4000m" as strings)
		{"cpu", true, []string{"b", "c", "d", "a"}},
		{"memory", false, []string{"a", "d", "c", "b"}},
	} {
		if got := order(c.col, c.desc); !slices.Equal(got, c.want) {
			t.Errorf("sort %s desc=%v = %v, want %v", c.col, c.desc, got, c.want)
		}
	}

	// Encoding: the wire units (millicores, MiB, per-mille); nil without the usage flag, nil for the node with no sample.
	sub, _ := e.Subscribe(protocol.ViewQuery{GVR: "v1/nodes", Sort: []protocol.Sort{{Col: "name"}}}, 1, win(0, 10))
	defer sub.Close()
	sub.SetUsage(true)
	p := sub.Prepare()
	rows := pendByName(p)
	a := encoded(t, mod, rows["a"])
	if num(a["cpuUse"]) != 2000 || num(a["cpuPctAlloc"]) != 500 || num(a["cpu"]) != 4000 || num(a["memUse"]) != 4096 ||
		num(a["memPctAlloc"]) != 500 || num(a["memory"]) != 8192 {
		t.Errorf("node a = %v", a)
	}
	if d := encoded(t, mod, rows["d"]); d["cpuUse"] != nil || d["cpuPctAlloc"] != nil || num(d["cpu"]) != 8000 {
		t.Errorf("node d (no sample) = %v", d)
	}
	commit(p)
	sub.SetUsage(false)
	p = sub.Prepare()
	if a := encoded(t, mod, pendByName(p)["a"]); a["cpuUse"] != nil || a["cpuPctAlloc"] != nil || num(a["cpu"]) != 4000 {
		t.Errorf("without the usage flag usage is nil and allocatable stays: %v", a)
	}
}

func TestNodeWithoutAllocatableIsNAnotNone(t *testing.T) {
	e := newEngine()
	st := attachKind(e, "c1", "v1/nodes", unode("x", "", ""))
	e.SetUsage("c1", "v1/nodes", usage.BuildNodes(st, st.Snapshot(nil, nil), []usage.NodeSample{{Name: "x", At: time.Now().UnixMilli(), CPU: 100, MemKi: 1024}}, nil, usage.VerBase))
	sub, _ := e.Subscribe(protocol.ViewQuery{GVR: "v1/nodes"}, 1, win(0, 10))
	defer sub.Close()
	sub.SetUsage(true)
	p := sub.Prepare()
	x := encoded(t, ingest.Lookup("v1/nodes"), pendByName(p)["x"])
	if num(x["cpuUse"]) != 100 || num(x["cpuPctAlloc"]) != -1 || x["cpu"] != nil {
		t.Errorf("node without allocatable = %v", x)
	}
}
