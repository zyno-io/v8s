package usage

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

func usageList(cpu, mem string) corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}
}

func podMetricsFixture() *metricsv1beta1.PodMetricsList {
	ts := metav1.NewTime(time.UnixMilli(1_700_000_000_000))
	return &metricsv1beta1.PodMetricsList{Items: []metricsv1beta1.PodMetrics{
		{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns1"}, Timestamp: ts, Window: metav1.Duration{Duration: 15 * time.Second},
			Containers: []metricsv1beta1.ContainerMetrics{
				{Name: "x", Usage: usageList("12345678n", "100Mi")},
				{Name: "y", Usage: usageList("1500u", "1500Ki")}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns2"}, Timestamp: ts,
			Containers: []metricsv1beta1.ContainerMetrics{{Name: "x", Usage: usageList("0", "1023")}}},
	}}
}

func TestFromPodMetricsConversions(t *testing.T) {
	s := FromPodMetrics(podMetricsFixture())
	if len(s) != 2 {
		t.Fatalf("samples = %d", len(s))
	}
	// 12345678n + 1500u = 13,845,678 nanocores = 13.845678m, ceil 14m (kubectl top parity: the sum is rounded, not each container)
	if s[0].CPU != 14 {
		t.Errorf("cpu = %d, want 14", s[0].CPU)
	}
	// 100Mi + 1500Ki = 104,857,600 + 1,536,000 bytes = 106,393,600 >> 10 = 103,900 KiB
	if s[0].MemKi != 103900 {
		t.Errorf("memKi = %d, want 103900", s[0].MemKi)
	}
	if s[0].NS != "ns1" || s[0].Name != "a" || s[0].At != 1_700_000_000_000 {
		t.Errorf("identity/time = %+v", s[0])
	}
	if s[1].CPU != 0 || s[1].MemKi != 0 {
		t.Errorf("zero usage = %+v (1023 bytes floors to 0 KiB)", s[1])
	}
	for in, want := range map[int64]uint32{0: 0, 1: 1, 999_999: 1, 1_000_000: 1, 1_000_001: 2, -5: 0} {
		if got := milli(in); got != want {
			t.Errorf("milli(%d) = %d, want %d", in, got, want)
		}
	}
}

// The same list decodes identically from JSON and from the protobuf the tuned client asks for.
func TestDecodeJSONAndProtobuf(t *testing.T) {
	want := FromPodMetrics(podMetricsFixture())

	js, err := json.Marshal(podMetricsFixture())
	if err != nil {
		t.Fatal(err)
	}
	var fromJSON metricsv1beta1.PodMetricsList
	if err := json.Unmarshal(js, &fromJSON); err != nil {
		t.Fatal(err)
	}
	pb, err := podMetricsFixture().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var fromPB metricsv1beta1.PodMetricsList
	if err := fromPB.Unmarshal(pb); err != nil {
		t.Fatal(err)
	}
	for name, l := range map[string]*metricsv1beta1.PodMetricsList{"json": &fromJSON, "protobuf": &fromPB} {
		got := FromPodMetrics(l)
		if len(got) != len(want) {
			t.Fatalf("%s: %d samples", name, len(got))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%s: sample %d = %+v, want %+v", name, i, got[i], want[i])
			}
		}
	}

	nl := &metricsv1beta1.NodeMetricsList{Items: []metricsv1beta1.NodeMetrics{
		{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Timestamp: metav1.NewTime(time.UnixMilli(5000)), Usage: usageList("2500m", "3Gi")}}}
	ns := FromNodeMetrics(nl)
	if len(ns) != 1 || ns[0].Name != "n1" || ns[0].CPU != 2500 || ns[0].MemKi != 3<<20 || ns[0].At != 5000 {
		t.Fatalf("node sample = %+v", ns)
	}
}

// ---- BuildPods ----

func podRow(uid, ns, name, node string, created time.Time) *ingest.Row {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: k8stypes.UID("u-" + uid), Namespace: ns, Name: name, ResourceVersion: "1", CreationTimestamp: metav1.NewTime(created)},
		Spec: corev1.PodSpec{NodeName: node}}
	return ingest.Project(p)
}

func fill(t *testing.T, rows ...*ingest.Row) (*store.Store, []store.Item) {
	t.Helper()
	st := store.New()
	items := make([]interface{}, len(rows))
	for i, r := range rows {
		items[i] = r
	}
	if err := st.Replace(items, "1"); err != nil {
		t.Fatal(err)
	}
	return st, st.Snapshot(nil, nil)
}

func slotOf(sn *Snap, it store.Item) *Slot { return sn.Lookup(it.H, it.Gen) }

func TestBuildPodsJoin(t *testing.T) {
	created := time.UnixMilli(1_000_000)
	st, items := fill(t,
		podRow("1", "ns1", "a", "node-1", created),
		podRow("2", "ns1", "b", "node-2", created),
		podRow("3", "ns2", "a", "", created), // same name, other namespace, unscheduled
	)
	samples := []PodSample{{NS: "ns1", Name: "a", At: 2_000_000, CPU: 10, MemKi: 2048}, {NS: "ns2", Name: "a", At: 2_000_000, CPU: 7, MemKi: 100}}
	nodes := map[string]NodeAlloc{"node-1": {CPU: 4000, MemMi: 8192}}
	sn := BuildPods(st, items, nil, samples, nodes, nil, VerBase)

	by := map[string]*Slot{}
	for _, it := range items {
		by[it.Row.NS().Value()+"/"+it.Row.Name()] = slotOf(sn, it)
	}
	if s := by["ns1/a"]; s == nil || !s.Has || s.CPU != 10 || s.MemKi != 2048 || s.NodeCPU != 4000 || s.NodeMemMi != 8192 {
		t.Fatalf("ns1/a = %+v", s)
	}
	if s := by["ns1/b"]; s == nil || s.Has || s.NodeCPU != 0 {
		t.Fatalf("ns1/b (in scope, no sample, node unknown) = %+v", s)
	}
	if s := by["ns2/a"]; s == nil || !s.Has || s.CPU != 7 || s.NodeCPU != 0 {
		t.Fatalf("ns2/a = %+v (the join is by namespace and name)", s)
	}
	if sn.Store != st || sn.Seq != VerBase {
		t.Fatalf("snap identity = %+v", sn)
	}
	for _, s := range by {
		if s.Ver != VerBase {
			t.Fatalf("a fresh slot's Ver = %d, want %d", s.Ver, VerBase)
		}
	}
}

func TestBuildPodsIgnoresASampleOlderThanThePod(t *testing.T) {
	// A StatefulSet pod recreated after the sample was taken: the sample belongs to its predecessor.
	st, items := fill(t, podRow("1", "ns", "web-0", "", time.UnixMilli(5_000)))
	sn := BuildPods(st, items, nil, []PodSample{{NS: "ns", Name: "web-0", At: 4_999, CPU: 99}}, nil, nil, VerBase)
	if s := slotOf(sn, items[0]); s == nil || s.Has {
		t.Fatalf("stale sample used: %+v", s)
	}
	sn = BuildPods(st, items, nil, []PodSample{{NS: "ns", Name: "web-0", At: 5_000, CPU: 99}}, nil, nil, VerBase)
	if s := slotOf(sn, items[0]); s == nil || !s.Has || s.CPU != 99 {
		t.Fatalf("sample at the creation time must count: %+v", s)
	}
}

func TestLookupGenerationAndRange(t *testing.T) {
	created := time.UnixMilli(1)
	st, items := fill(t, podRow("1", "ns", "a", "", created))
	sn := BuildPods(st, items, nil, nil, nil, nil, VerBase)
	it := items[0]
	if sn.Lookup(it.H, it.Gen) == nil {
		t.Fatal("live slot not found")
	}
	if sn.Lookup(it.H, it.Gen+1) != nil {
		t.Fatal("a reused handle (new generation) must not inherit usage")
	}
	if sn.Lookup(it.H+10, it.Gen) != nil {
		t.Fatal("out-of-range handle must be nil")
	}
	var nilSnap *Snap
	if nilSnap.Lookup(0, 1) != nil {
		t.Fatal("nil snapshot must answer nil")
	}
	// A handle that was never in the items has Ver 0 and no slot.
	st2, items2 := fill(t, podRow("1", "ns", "a", "", created), podRow("2", "ns", "b", "", created))
	sn2 := BuildPods(st2, items2[1:], []string{"ns"}, nil, nil, nil, VerBase)
	if sn2.Lookup(items2[0].H, items2[0].Gen) != nil {
		t.Fatal("a row outside the build items has no slot")
	}
}

func TestVerInheritedWhenUnchangedBumpedWhenChanged(t *testing.T) {
	created := time.UnixMilli(1)
	st, items := fill(t, podRow("1", "ns", "a", "node-1", created), podRow("2", "ns", "b", "node-1", created), podRow("3", "ns", "c", "node-1", created))
	nodes := map[string]NodeAlloc{"node-1": {CPU: 1000, MemMi: 1024}}
	s1 := []PodSample{{NS: "ns", Name: "a", At: 10, CPU: 5, MemKi: 5}, {NS: "ns", Name: "b", At: 10, CPU: 6, MemKi: 6}}
	first := BuildPods(st, items, nil, s1, nodes, nil, VerBase+1)

	s2 := []PodSample{{NS: "ns", Name: "a", At: 20, CPU: 5, MemKi: 5}, // same values, newer timestamp
		{NS: "ns", Name: "b", At: 20, CPU: 7, MemKi: 6}, // changed
		{NS: "ns", Name: "c", At: 20, CPU: 1, MemKi: 1}} // appeared
	second := BuildPods(st, items, nil, s2, nodes, first, VerBase+2)
	a, b, c := slotOf(second, items[0]), slotOf(second, items[1]), slotOf(second, items[2])
	if a.Ver != VerBase+1 {
		t.Errorf("unchanged slot Ver = %d, want it inherited (%d)", a.Ver, VerBase+1)
	}
	if b.Ver != VerBase+2 || c.Ver != VerBase+2 {
		t.Errorf("changed/new slot Ver = %d/%d, want %d", b.Ver, c.Ver, VerBase+2)
	}
	// A node allocatable change is a change too (a %N cell moves).
	third := BuildPods(st, items, nil, s2, map[string]NodeAlloc{"node-1": {CPU: 2000, MemMi: 1024}}, second, VerBase+3)
	if v := slotOf(third, items[0]).Ver; v != VerBase+3 {
		t.Errorf("allocatable change: Ver = %d, want %d", v, VerBase+3)
	}
	// Ver never decreases for a live slot, and is never below VerBase.
	for _, sn := range []*Snap{first, second, third} {
		for _, it := range items {
			if s := slotOf(sn, it); s.Ver < VerBase {
				t.Errorf("Ver %d below VerBase", s.Ver)
			}
		}
	}
	// A previous snapshot of another store is not inherited from.
	other, itemsOther := fill(t, podRow("1", "ns", "a", "node-1", created))
	x := BuildPods(other, itemsOther, nil, s2[:1], nodes, second, VerBase+9)
	if v := slotOf(x, itemsOther[0]).Ver; v != VerBase+9 {
		t.Errorf("Ver inherited across stores: %d", v)
	}
}

func TestCovers(t *testing.T) {
	cases := []struct {
		scope, ask []string
		want       bool
	}{
		{nil, nil, true},
		{nil, []string{"a"}, true},
		{[]string{"a", "b"}, []string{"a"}, true},
		{[]string{"a", "b"}, []string{"a", "b"}, true},
		{[]string{"a", "b"}, []string{"a", "c"}, false},
		{[]string{"a"}, nil, false}, // all namespaces is not covered by a scoped snapshot
	}
	for _, c := range cases {
		if got := (&Snap{Scope: c.scope}).Covers(c.ask); got != c.want {
			t.Errorf("scope %v covers %v = %v, want %v", c.scope, c.ask, got, c.want)
		}
	}
	var nilSnap *Snap
	if nilSnap.Covers(nil) {
		t.Error("a nil snapshot covers nothing")
	}
}

func TestBuildNodesCarriesOwnAllocatable(t *testing.T) {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", UID: "n1", ResourceVersion: "1"},
		Status: corev1.NodeStatus{Allocatable: usageList("4", "8Gi")}}
	row := ingest.Lookup("v1/nodes").ProjectObj(n)
	st, items := fill(t, row)
	sn := BuildNodes(st, items, []NodeSample{{Name: "n1", At: 1, CPU: 1000, MemKi: 1 << 20}}, nil, VerBase)
	s := slotOf(sn, items[0])
	if s == nil || !s.Has || s.CPU != 1000 || s.NodeCPU != 4000 || s.NodeMemMi != 8192 {
		t.Fatalf("node slot = %+v", s)
	}
}
