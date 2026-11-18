package ingest

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestNodeUsageColumns(t *testing.T) {
	m := Lookup("v1/nodes")
	if m == nil || m.Usage != "nodes" {
		t.Fatalf("nodes module = %+v", m)
	}
	var keys []string
	hidden := map[string]bool{}
	for _, c := range m.Cols {
		keys = append(keys, c.Key)
		hidden[c.Key] = c.Hidden
	}
	want := []string{"cluster", "name", "status", "roles", "version", "internalIP", "os", "kernel", "runtime",
		"cpuUse", "cpuPctAlloc", "cpu", "memUse", "memPctAlloc", "memory", "pods", "taints", "statusTone", "createdAt"}
	if !slices.Equal(keys, want) {
		t.Fatalf("columns = %v, want %v", keys, want)
	}
	for _, k := range []string{"os", "kernel", "runtime", "taints"} {
		if !hidden[k] {
			t.Errorf("%s should be hidden by default", k)
		}
	}
	for _, k := range []string{"cpuUse", "cpuPctAlloc", "cpu", "memUse", "memPctAlloc", "memory", "internalIP", "pods"} {
		if hidden[k] {
			t.Errorf("%s should be shown by default", k)
		}
	}
	// cpu and memory are numeric now (they sorted lexicographically as strings before) and carry the allocatable.
	if c := m.Col("cpu"); c.Src != SrcRes || c.Use != ResCPUReq || c.Unit != "millicores" || c.Metrics {
		t.Errorf("cpu = %+v", c)
	}
	if c := m.Col("memory"); c.Src != SrcRes || c.Use != ResMemReq || c.Unit != "MiB" {
		t.Errorf("memory = %+v", c)
	}
	if c := m.Col("cpuPctAlloc"); c.Src != SrcUsage || c.Use != UseCPUPctNode || !c.Metrics {
		t.Errorf("cpuPctAlloc = %+v", c)
	}
	n := &corev1.Node{ObjectMeta: om("n1", ""), Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("15890m"), corev1.ResourceMemory: resource.MustParse("63Gi")}}}
	r := m.ProjectObj(n)
	if c, mem := r.Allocatable(); c != 15890 || mem != 63<<30 {
		t.Errorf("allocatable = %d, %d", c, mem)
	}
	if len(r.cells) != m.NCells {
		t.Errorf("row has %d cells, module %d", len(r.cells), m.NCells)
	}
}
