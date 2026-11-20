package ingest

import (
	"testing"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	resourcehelper "k8s.io/component-helpers/resource"
)

func resList(cpu, mem string) corev1.ResourceList {
	l := corev1.ResourceList{}
	if cpu != "" {
		l[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if mem != "" {
		l[corev1.ResourceMemory] = resource.MustParse(mem)
	}
	if len(l) == 0 {
		return nil
	}
	return l
}

func resC(reqCPU, reqMem, limCPU, limMem string) corev1.Container {
	return corev1.Container{Resources: corev1.ResourceRequirements{Requests: resList(reqCPU, reqMem), Limits: resList(limCPU, limMem)}}
}

func resSidecar(c corev1.Container) corev1.Container {
	always := corev1.ContainerRestartPolicyAlways
	c.RestartPolicy = &always
	return c
}

const mi = 1 << 20

func TestPodRes(t *testing.T) {
	cases := []struct {
		name string
		spec corev1.PodSpec
		want Res
		// bounded: every container has both limits, so the helper's limits equal ours (otherwise ours is 0 by design)
		bounded bool
	}{
		{"single container", corev1.PodSpec{Containers: []corev1.Container{resC("100m", "128Mi", "500m", "256Mi")}},
			Res{100, 500, 128 * mi, 256 * mi}, true},
		{"two containers sum", corev1.PodSpec{Containers: []corev1.Container{resC("100m", "128Mi", "200m", "256Mi"), resC("50m", "64Mi", "100m", "128Mi")}},
			Res{150, 300, 192 * mi, 384 * mi}, true},
		{"a missing cpu limit is n/a, the memory limit still counts", corev1.PodSpec{Containers: []corev1.Container{resC("250m", "256Mi", "", "512Mi")}},
			Res{250, 0, 256 * mi, 512 * mi}, false},
		{"one container without a limit makes the pod unbounded", corev1.PodSpec{Containers: []corev1.Container{resC("100m", "64Mi", "200m", "128Mi"), resC("100m", "64Mi", "", "")}},
			Res{200, 0, 128 * mi, 0}, false},
		{"init container larger than the sum", corev1.PodSpec{
			Containers:     []corev1.Container{resC("100m", "100Mi", "200m", "200Mi")},
			InitContainers: []corev1.Container{resC("1", "1Gi", "2", "2Gi")}},
			Res{1000, 2000, 1024 * mi, 2048 * mi}, true},
		{"a sidecar adds to the sum", corev1.PodSpec{
			Containers:     []corev1.Container{resC("100m", "100Mi", "200m", "200Mi")},
			InitContainers: []corev1.Container{resSidecar(resC("50m", "50Mi", "50m", "50Mi"))}},
			Res{150, 250, 150 * mi, 250 * mi}, true},
		{"an init container after a sidecar runs alongside it (q + sidecar sum)", corev1.PodSpec{
			Containers: []corev1.Container{resC("100m", "100Mi", "200m", "200Mi")},
			InitContainers: []corev1.Container{
				resSidecar(resC("200m", "200Mi", "200m", "200Mi")),
				resC("500m", "500Mi", "500m", "500Mi")}},
			Res{700, 700, 700 * mi, 700 * mi}, true},
		{"a non-restartable init container without a limit does not void the pod limit", corev1.PodSpec{
			Containers:     []corev1.Container{resC("100m", "100Mi", "200m", "200Mi")},
			InitContainers: []corev1.Container{resC("10m", "10Mi", "", "")}},
			Res{100, 200, 100 * mi, 200 * mi}, true},
		{"a sidecar without a limit makes the pod unbounded", corev1.PodSpec{
			Containers:     []corev1.Container{resC("100m", "100Mi", "200m", "200Mi")},
			InitContainers: []corev1.Container{resSidecar(resC("10m", "10Mi", "", ""))}},
			Res{110, 0, 110 * mi, 0}, false},
		{"overhead is added to requests and to non-zero limits", corev1.PodSpec{
			Containers: []corev1.Container{resC("100m", "128Mi", "500m", "256Mi")},
			Overhead:   resList("250m", "120Mi")},
			Res{350, 750, 248 * mi, 376 * mi}, true},
		{"overhead alone does not create a limit", corev1.PodSpec{
			Containers: []corev1.Container{resC("100m", "128Mi", "", "")},
			Overhead:   resList("250m", "120Mi")},
			Res{350, 0, 248 * mi, 0}, false},
		{"pod-level resources override the containers", corev1.PodSpec{
			Containers: []corev1.Container{resC("100m", "128Mi", "", ""), resC("100m", "128Mi", "", "")},
			Resources:  &corev1.ResourceRequirements{Requests: resList("1", "1Gi"), Limits: resList("2", "2Gi")}},
			Res{1000, 2000, 1024 * mi, 2048 * mi}, false},
		{"BestEffort", corev1.PodSpec{Containers: []corev1.Container{resC("", "", "", "")}}, Res{}, false},
		{"guaranteed", corev1.PodSpec{Containers: []corev1.Container{resC("100m", "128Mi", "100m", "128Mi")}},
			Res{100, 100, 128 * mi, 128 * mi}, true},
		{"fractional cpu rounds up to a millicore", corev1.PodSpec{Containers: []corev1.Container{resC("0.0005", "", "", "")}}, Res{CPUReq: 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &corev1.Pod{Spec: tc.spec}
			got := podRes(p)
			if got != tc.want {
				t.Fatalf("podRes = %+v, want %+v", got, tc.want)
			}
			// Cross-check with the scheduler's own helpers (spec amounts, as the plan decides: D11).
			req := resourcehelper.PodRequests(p, resourcehelper.PodResourcesOptions{})
			if c, m := req[corev1.ResourceCPU], req[corev1.ResourceMemory]; uint32(c.MilliValue()) != got.CPUReq || uint64(m.Value()) != got.MemReq {
				t.Errorf("requests %dm/%d differ from component-helpers %dm/%d", got.CPUReq, got.MemReq, c.MilliValue(), m.Value())
			}
			lim := resourcehelper.PodLimits(p, resourcehelper.PodResourcesOptions{})
			c, m := lim[corev1.ResourceCPU], lim[corev1.ResourceMemory]
			if tc.bounded {
				if uint32(c.MilliValue()) != got.CPULim || uint64(m.Value()) != got.MemLim {
					t.Errorf("limits %dm/%d differ from component-helpers %dm/%d", got.CPULim, got.MemLim, c.MilliValue(), m.Value())
				}
			} else if got.CPULim != 0 && uint32(c.MilliValue()) != got.CPULim || got.MemLim != 0 && uint64(m.Value()) != got.MemLim {
				// a partially bounded pod: whatever resource we report must still agree with the helper
				t.Errorf("limits %dm/%d disagree with component-helpers %dm/%d", got.CPULim, got.MemLim, c.MilliValue(), m.Value())
			}
		})
	}
}

func TestProjectResAndTerminal(t *testing.T) {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", UID: "u1", ResourceVersion: "1"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{resC("100m", "128Mi", "200m", "256Mi")}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	r := Project(p)
	if c, m := r.Requests(); c != 100 || m != 128*mi {
		t.Fatalf("requests = %d/%d", c, m)
	}
	if c, m := r.Limits(); c != 200 || m != 256*mi {
		t.Fatalf("limits = %d/%d", c, m)
	}
	if r.Terminal() {
		t.Fatal("a running pod is not terminal")
	}

	// An in-place resize changes the row (CellsEqual sees res).
	p2 := p.DeepCopy()
	p2.Spec.Containers[0].Resources.Limits = resList("300m", "256Mi")
	if r.CellsEqual(Project(p2)) {
		t.Fatal("a limit change must make the rows differ")
	}
	p3 := p.DeepCopy()
	p3.Spec.Containers[0].Resources.Requests = resList("150m", "128Mi")
	if r.CellsEqual(Project(p3)) {
		t.Fatal("a request change must make the rows differ")
	}
	if !r.CellsEqual(Project(p.DeepCopy())) {
		t.Fatal("an identical pod must compare equal")
	}

	// The terminal flag is part of the row identity.
	p4 := p.DeepCopy()
	p4.Status.Phase = corev1.PodSucceeded
	done := Project(p4)
	if !done.Terminal() {
		t.Fatal("a Succeeded pod is terminal")
	}
	if r.CellsEqual(done) {
		t.Fatal("the terminal flag must make the rows differ")
	}
	p5 := p.DeepCopy()
	p5.Status.Phase = corev1.PodFailed
	if !Project(p5).Terminal() {
		t.Fatal("a Failed pod is terminal")
	}
}

func TestNodeAllocatable(t *testing.T) {
	m := Lookup("v1/nodes")
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", UID: "n1", ResourceVersion: "1"},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("15890m"), corev1.ResourceMemory: resource.MustParse("63Gi")},
			Capacity:    corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("16"), corev1.ResourceMemory: resource.MustParse("64Gi")}}}
	r := m.ProjectObj(n)
	if c, mem := r.Allocatable(); c != 15890 || mem != 63<<30 {
		t.Fatalf("allocatable = %d/%d (not capacity)", c, mem)
	}
}

// PodColumns is the documented schema: the pods module must match it key for key, including the usage metadata.
func TestPodModuleMatchesProtocolColumns(t *testing.T) {
	m := Lookup(PodsGVR)
	got, want := m.Columns(), protocol.PodColumns
	if len(got) != len(want) {
		t.Fatalf("pods module has %d columns, protocol.PodColumns %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("column %d: module %+v, protocol %+v", i, got[i], want[i])
		}
	}
	if m.Usage != "pods" {
		t.Fatalf("Usage = %q", m.Usage)
	}
	for _, c := range m.Cols {
		if (c.Src == SrcUsage || c.Src == SrcRes) && (c.Filterable || !c.Sortable || c.Unit == "") {
			t.Errorf("%s: usage columns are sortable, not filterable, and carry a unit", c.Key)
		}
		if c.Src == SrcUsage && !c.Metrics {
			t.Errorf("%s: SrcUsage columns need metrics", c.Key)
		}
		if c.Src == SrcRes && c.Metrics {
			t.Errorf("%s: SrcRes columns need no metrics", c.Key)
		}
	}
	if m.NCells != 6 {
		t.Fatalf("pod cells = %d, the usage columns are not cells", m.NCells)
	}
}

func BenchmarkPodRes(b *testing.B) {
	p := &corev1.Pod{Spec: corev1.PodSpec{
		Containers:     []corev1.Container{resC("100m", "128Mi", "500m", "256Mi"), resC("20m", "32Mi", "100m", "64Mi")},
		InitContainers: []corev1.Container{resSidecar(resC("10m", "32Mi", "10m", "32Mi"))},
		Overhead:       resList("250m", "120Mi")}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = podRes(p)
	}
}

func BenchmarkPodResBestEffort(b *testing.B) {
	p := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{resC("", "", "", ""), resC("", "", "", "")}}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = podRes(p)
	}
}
