package view

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/internal/usage"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// upod builds a running pod with the given requests and limits ("" = none).
func upod(uid, ns, name, node, cpuReq, memReq, cpuLim, memLim string) *ingest.PodRow {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(uid), ResourceVersion: "1"}}
	p.Spec.NodeName = node
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
	p.Spec.Containers = []corev1.Container{{Name: "c", Resources: corev1.ResourceRequirements{Requests: rl(cpuReq, memReq), Limits: rl(cpuLim, memLim)}}}
	p.Status.Phase = corev1.PodRunning
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "c", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	return ingest.Project(p)
}

// snapFor builds a snapshot of st with the given CPU (millicores) and memory (KiB) by pod name; names absent from the
// map have no sample.
func snapFor(st *store.Store, seq uint32, prev *usage.Snap, samples map[string][2]uint32) *usage.Snap {
	var ss []usage.PodSample
	for n, v := range samples {
		ss = append(ss, usage.PodSample{NS: "ns", Name: n, At: time.Now().UnixMilli(), CPU: v[0], MemKi: v[1]})
	}
	return usage.BuildPods(st, st.Snapshot(nil, nil), nil, ss, map[string]usage.NodeAlloc{"n1": {CPU: 4000, MemMi: 8192}}, prev, seq)
}

type fakeDict struct{}

func (fakeDict) ID(s ingest.Str) uint32 { return 1 }

// encoded returns the cells of a pending row by column key.
func encoded(t *testing.T, mod *ingest.Module, r *PendRow) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	enc.UseCompactInts(true)
	if err := EncodeRow(enc, fakeDict{}, mod, r); err != nil {
		t.Fatal(err)
	}
	var cells []any
	if err := msgpack.Unmarshal(buf.Bytes(), &cells); err != nil {
		t.Fatal(err)
	}
	if len(cells) != 2+len(mod.Cols) {
		t.Fatalf("row has %d cells for %d columns", len(cells), len(mod.Cols))
	}
	if num(cells[1]) != int64(r.Ver) {
		t.Fatalf("encoded version %v != PendRow.Ver %d (the wire version is what is diffed)", cells[1], r.Ver)
	}
	out := map[string]any{}
	for i := range mod.Cols {
		out[mod.Cols[i].Key] = cells[2+i]
	}
	return out
}

func num(v any) int64 {
	switch x := v.(type) {
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(x)
	}
	panic(fmt.Sprintf("not a number: %T %v", v, v))
}

// pendByName returns the rows of a Prepare by pod name.
func pendByName(p *Pending) map[string]*PendRow {
	out := map[string]*PendRow{}
	for i := range p.Rows {
		out[p.Rows[i].Row.Name()] = &p.Rows[i]
	}
	return out
}

func commit(p *Pending) { p.Commit(len(p.Rows), len(p.Removed), p.SendIDs) }

// entryNames lists the view's pod names in order.
func entryNames(v *View) []string {
	out := make([]string, len(v.entries))
	for i, en := range v.entries {
		out[i] = en.row.Name()
	}
	return out
}

func TestUsageCellsNilWithoutUsageAndNAvsNone(t *testing.T) {
	e := newEngine()
	st := attach(e, "c1",
		upod("1", "ns", "full", "n1", "100m", "128Mi", "200m", "256Mi"),
		upod("2", "ns", "nolimit", "n1", "100m", "128Mi", "", ""),
		upod("3", "ns", "nosample", "n1", "100m", "128Mi", "200m", "256Mi"),
	)
	sub, _ := e.Subscribe(protocol.ViewQuery{GVR: ingest.PodsGVR}, 1, win(0, 10))
	defer sub.Close()

	// The subscription does not show usage: every usage cell is nil, requests and limits are still sent.
	e.SetUsage("c1", ingest.PodsGVR, snapFor(st, usage.VerBase, nil, map[string][2]uint32{"full": {50, 64 << 10}, "nolimit": {50, 64 << 10}}))
	p := sub.Prepare()
	rows := pendByName(p)
	c := encoded(t, p.Mod, rows["full"])
	for _, k := range []string{"cpuUse", "memUse", "cpuPctReq", "cpuPctLim", "cpuPctNode", "memPctReq", "memPctLim", "memPctNode"} {
		if c[k] != nil {
			t.Errorf("%s = %v without the usage flag, want nil", k, c[k])
		}
	}
	if num(c["cpuReq"]) != 100 || num(c["cpuLim"]) != 200 || num(c["memReq"]) != 128 || num(c["memLim"]) != 256 {
		t.Errorf("requests/limits = %v %v %v %v", c["cpuReq"], c["cpuLim"], c["memReq"], c["memLim"])
	}
	if rows["full"].Ver != rows["full"].Row.Ver() {
		t.Errorf("a subscription that does not show usage must use row.Ver(): %d vs %d", rows["full"].Ver, rows["full"].Row.Ver())
	}
	commit(p)

	sub.SetUsage(true)
	p = sub.Prepare()
	if p == nil {
		t.Fatal("turning usage on must re-send the rows")
	}
	rows = pendByName(p)
	c = encoded(t, p.Mod, rows["full"])
	want := map[string]int64{"cpuUse": 50, "memUse": 64, "cpuPctReq": 500, "cpuPctLim": 250, "cpuPctNode": 13, "memPctReq": 500, "memPctLim": 250, "memPctNode": 8}
	for k, w := range want {
		if num(c[k]) != w {
			t.Errorf("full.%s = %v, want %d", k, c[k], w)
		}
	}
	c = encoded(t, p.Mod, rows["nolimit"])
	if num(c["cpuUse"]) != 50 || num(c["cpuPctReq"]) != 500 {
		t.Errorf("nolimit use = %v %v", c["cpuUse"], c["cpuPctReq"])
	}
	if num(c["cpuPctLim"]) != -1 || num(c["memPctLim"]) != -1 {
		t.Errorf("no limit must be n/a (-1), got %v %v", c["cpuPctLim"], c["memPctLim"])
	}
	c = encoded(t, p.Mod, rows["nosample"])
	for _, k := range []string{"cpuUse", "memUse", "cpuPctReq", "cpuPctLim", "memPctNode"} {
		if c[k] != nil {
			t.Errorf("nosample.%s = %v, want nil (no data, not n/a)", k, c[k])
		}
	}
	if rows["full"].Ver != rows["full"].Row.Ver()+usage.VerBase {
		t.Errorf("wire version = %d, want row %d + slot %d", rows["full"].Ver, rows["full"].Row.Ver(), usage.VerBase)
	}
	commit(p)

	sub.SetUsage(false)
	p = sub.Prepare()
	if p == nil {
		t.Fatal("turning usage off must re-send the rows (their usage cells become nil)")
	}
	c = encoded(t, p.Mod, pendByName(p)["full"])
	if c["cpuUse"] != nil {
		t.Errorf("cpuUse after usage off = %v", c["cpuUse"])
	}
	commit(p)
	sub.SetUsage(false) // unchanged: not dirty
	_ = sub.Prepare()
	if sub.Dirty() {
		t.Error("an unchanged usage flag must not mark the subscription dirty")
	}
}

func TestPrepareResendsOnlyRowsWhoseSlotChanged(t *testing.T) {
	e := newEngine()
	st := attach(e, "c1",
		upod("1", "ns", "a", "n1", "100m", "", "200m", ""),
		upod("2", "ns", "b", "n1", "100m", "", "200m", ""),
		upod("3", "ns", "c", "n1", "100m", "", "200m", ""))
	sub, _ := e.Subscribe(protocol.ViewQuery{GVR: ingest.PodsGVR}, 1, win(0, 10))
	defer sub.Close()
	sub.SetUsage(true)
	s1 := snapFor(st, usage.VerBase+1, nil, map[string][2]uint32{"a": {10, 1}, "b": {20, 1}, "c": {30, 1}})
	e.SetUsage("c1", ingest.PodsGVR, s1)
	p := sub.Prepare()
	if len(p.Rows) != 3 {
		t.Fatalf("first patch has %d rows", len(p.Rows))
	}
	commit(p)

	s2 := snapFor(st, usage.VerBase+2, s1, map[string][2]uint32{"a": {10, 1}, "b": {25, 1}, "c": {30, 1}}) // only b changed
	e.SetUsage("c1", ingest.PodsGVR, s2)
	if !sub.Dirty() {
		t.Fatal("a poll must mark a usage subscription dirty")
	}
	p = sub.Prepare()
	if p == nil || len(p.Rows) != 1 || p.Rows[0].Row.Name() != "b" {
		t.Fatalf("second patch = %+v, want only b", p)
	}
	if p.SendIDs {
		t.Error("membership did not change: no id list")
	}
	commit(p)

	// The identical snapshot again: nothing to send.
	e.SetUsage("c1", ingest.PodsGVR, snapFor(st, usage.VerBase+3, s2, map[string][2]uint32{"a": {10, 1}, "b": {25, 1}, "c": {30, 1}}))
	if p = sub.Prepare(); p != nil {
		t.Fatalf("unchanged values re-sent %d rows", len(p.Rows))
	}
}

func TestSetUsageDirtiesOnlyUsageSubscriptionsAndResortsOnlyUsageSortedViews(t *testing.T) {
	e := newEngine()
	st := attach(e, "c1",
		upod("1", "ns", "a", "n1", "100m", "", "1000m", ""),
		upod("2", "ns", "b", "n1", "100m", "", "1000m", ""),
		upod("3", "ns", "c", "n1", "100m", "", "1000m", ""))
	q := protocol.ViewQuery{GVR: ingest.PodsGVR}
	plain, _ := e.Subscribe(q, 1, win(0, 10))
	plainOff, _ := e.Subscribe(q, 1, win(0, 10))
	plain.SetUsage(true)
	qs := q
	qs.Sort = []protocol.Sort{{Col: "cpuPctLim", Desc: true}}
	sorted, _ := e.Subscribe(qs, 1, win(0, 10))
	for _, s := range []*Sub{plain, plainOff, sorted} {
		if p := s.Prepare(); p != nil {
			commit(p)
		}
		defer s.Close()
	}

	namesOf := func(s *Sub) []string { return entryNames(s.v) }
	if got := namesOf(sorted); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("before any usage, the tail order (name) applies: %v", got)
	}
	e.SetUsage("c1", ingest.PodsGVR, snapFor(st, usage.VerBase, nil, map[string][2]uint32{"a": {100, 1}, "b": {900, 1}, "c": {500, 1}}))
	if got := namesOf(sorted); !slices.Equal(got, []string{"b", "c", "a"}) {
		t.Fatalf("usage-sorted view = %v, want b c a (%%CPU/L desc)", got)
	}
	if got := namesOf(plain); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("a view not sorted by usage must not move: %v", got)
	}
	if !plain.Dirty() || !sorted.Dirty() {
		t.Error("the usage-showing subscription and the usage-sorted one must be dirty")
	}
	if plainOff.Dirty() {
		t.Error("a subscription that does not show usage must not be dirtied by a poll")
	}
	if !sorted.v.useSort || plain.v.useSort {
		t.Error("useSort is set only for views sorted by a usage column")
	}
}

func TestUsageSortMissingValuesLastInBothDirections(t *testing.T) {
	for _, desc := range []bool{false, true} {
		for _, n := range []int{0, 400} { // 400 filler rows: the keyed path (>256 entries) as well as the plain one
			t.Run(fmt.Sprintf("desc=%v/filler=%d", desc, n), func(t *testing.T) {
				e := newEngine()
				var rows []*ingest.PodRow
				rows = append(rows,
					upod("1", "ns", "a", "n1", "100m", "", "1000m", ""), // 50 %
					upod("2", "ns", "b", "n1", "100m", "", "1000m", ""), // 10 %
					upod("3", "ns", "c", "n1", "100m", "", "", ""),      // no limit: n/a
					upod("4", "ns", "d", "n1", "100m", "", "1000m", ""), // no sample: none
					upod("5", "ns", "e", "n1", "100m", "", "200m", ""),  // 150 %
					upod("6", "ns", "f", "n1", "100m", "", "", ""),      // no limit: n/a
					upod("7", "ns", "g", "n1", "100m", "", "1000m", "")) // no sample: none
				samples := map[string][2]uint32{"a": {500, 1}, "b": {100, 1}, "c": {10, 1}, "e": {300, 1}, "f": {10, 1}}
				rng := rand.New(rand.NewPCG(7, 8))
				for i := 0; i < n; i++ {
					name := fmt.Sprintf("z%03d", i)
					rows = append(rows, upod(fmt.Sprint("f", i), "ns", name, "n1", "100m", "", "1000m", ""))
					samples[name] = [2]uint32{uint32(rng.IntN(1500)), 1}
				}
				st := attach(e, "c1", rows...)
				e.SetUsage("c1", ingest.PodsGVR, snapFor(st, usage.VerBase, nil, samples))
				sub, _ := e.Subscribe(protocol.ViewQuery{GVR: ingest.PodsGVR, Sort: []protocol.Sort{{Col: "cpuPctLim", Desc: desc}}}, 1, win(0, 10))
				defer sub.Close()
				checkUsageOrder(t, sub.v, "cpuPctLim", desc)
				names := entryNames(sub.v)
				tail := names[len(names)-4:]
				if !slices.Equal(tail, []string{"c", "f", "d", "g"}) { // n/a first (by name), then none (by name), whatever the direction
					t.Fatalf("tail = %v, want [c f d g]", tail)
				}
				if n == 0 {
					want := []string{"b", "a", "e"}
					if desc {
						want = []string{"e", "a", "b"}
					}
					if got := names[:3]; !slices.Equal(got, want) {
						t.Fatalf("head = %v, want %v", got, want)
					}
				}
			})
		}
	}
}

// checkUsageOrder asserts the view's entries are in the order the column's comparison defines: rank, then value by
// direction (and that the view's own comparator agrees with usageOrd).
func checkUsageOrder(t *testing.T, v *View, col string, desc bool) {
	t.Helper()
	c := v.n.Mod.Col(col)
	type k struct {
		val  uint64
		rank uint8
	}
	var keys []k
	for i := range v.entries {
		val, rank := usageOrd(c.Use, v.entries[i].row, v.slot(&v.entries[i]))
		keys = append(keys, k{val, rank})
	}
	for i := 1; i < len(keys); i++ {
		a, b := keys[i-1], keys[i]
		ok := a.rank < b.rank || a.rank == b.rank && (a.rank != 0 || (!desc && a.val <= b.val) || (desc && a.val >= b.val))
		if !ok {
			t.Fatalf("entries %d,%d out of order: %+v then %+v", i-1, i, a, b)
		}
	}
}

func TestUsageSortAcrossClusters(t *testing.T) {
	e := newEngine()
	s1 := attach(e, "c1", upod("1", "ns", "a", "n1", "100m", "", "1000m", ""), upod("2", "ns", "b", "n1", "100m", "", "1000m", ""))
	s2 := attach(e, "c2", upod("3", "ns", "c", "n1", "100m", "", "1000m", ""), upod("4", "ns", "d", "n1", "100m", "", "1000m", ""))
	e.SetUsage("c1", ingest.PodsGVR, snapFor(s1, usage.VerBase, nil, map[string][2]uint32{"a": {300, 1}, "b": {100, 1}}))
	sub, _ := e.Subscribe(protocol.ViewQuery{GVR: ingest.PodsGVR, Sort: []protocol.Sort{{Col: "cpuPctLim"}}}, 1, win(0, 10))
	defer sub.Close()
	// c2 has no snapshot yet: its rows are "none" and sort last.
	if got := entryNames(sub.v); !slices.Equal(got, []string{"b", "a", "c", "d"}) {
		t.Fatalf("got %v", got)
	}
	e.SetUsage("c2", ingest.PodsGVR, snapFor(s2, usage.VerBase+1, nil, map[string][2]uint32{"c": {200, 1}, "d": {50, 1}}))
	if got := entryNames(sub.v); !slices.Equal(got, []string{"d", "b", "c", "a"}) {
		t.Fatalf("after c2's poll: %v, want d b c a", got)
	}
	e.SetUsage("c1", ingest.PodsGVR, nil)
	if got := entryNames(sub.v); !slices.Equal(got, []string{"d", "c", "a", "b"}) {
		t.Fatalf("after clearing c1: %v, want d c a b", got)
	}
}

// Regression for the find(gen) fix: an incremental event on a usage-sorted view must find and move the right entry.
func TestUsageSortedViewIncrementalUpdates(t *testing.T) {
	e := newEngine()
	var rows []*ingest.PodRow
	samples := map[string][2]uint32{}
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("p%02d", i)
		rows = append(rows, upod(fmt.Sprint("u", i), "ns", name, "n1", "100m", "", "1000m", ""))
		samples[name] = [2]uint32{uint32(i * 20), 1}
	}
	st := attach(e, "c1", rows...)
	e.SetUsage("c1", ingest.PodsGVR, snapFor(st, usage.VerBase, nil, samples))
	sub, _ := e.Subscribe(protocol.ViewQuery{GVR: ingest.PodsGVR, Sort: []protocol.Sort{{Col: "cpuPctLim", Desc: true}}}, 1, win(0, 100))
	defer sub.Close()

	check := func(stage string) {
		t.Helper()
		e.Pump()
		checkUsageOrder(t, sub.v, "cpuPctLim", true)
		if len(sub.v.entries) != st.Len() {
			t.Fatalf("%s: %d entries for %d rows", stage, len(sub.v.entries), st.Len())
		}
		seen := map[uint64]bool{}
		for _, en := range sub.v.entries {
			k := uint64(en.h)<<32 | uint64(en.gen)
			if seen[k] {
				t.Fatalf("%s: duplicate entry", stage)
			}
			seen[k] = true
		}
	}
	check("start")

	// Updates (a restart-count style change keeps the same handle and gen): the entry must be found by its old row.
	for i := 0; i < 40; i += 3 {
		upd := upod(fmt.Sprint("u", i), "ns", fmt.Sprintf("p%02d", i), "n1", "100m", "", "500m", "") // limit halved: its % moves
		_ = st.Update(upd)
	}
	check("limit changes")

	// A delete and an add reuse a handle with a new generation; the new pod must not inherit the old slot.
	_ = st.Delete(rows[5])
	e.Pump()
	_ = st.Add(upod("new", "ns", "reborn", "n1", "100m", "", "1000m", ""))
	check("handle reuse")
	var reborn *entry
	for i := range sub.v.entries {
		if sub.v.entries[i].row.Name() == "reborn" {
			reborn = &sub.v.entries[i]
		}
	}
	if reborn == nil || sub.v.slot(reborn) != nil {
		t.Fatalf("a reused handle inherited usage: %+v", reborn)
	}
	if reborn != &sub.v.entries[len(sub.v.entries)-1] {
		t.Fatal("a pod with no slot sorts last (none)")
	}

	// A bulk change goes through the snapshot merge instead of per-event inserts.
	for i := 0; i < 700; i++ {
		_ = st.Add(upod(fmt.Sprint("b", i), "ns", fmt.Sprintf("bulk%03d", i), "n1", "100m", "", "1000m", ""))
	}
	check("bulk")
}

func TestAttachKindWithANewStoreClearsUsage(t *testing.T) {
	e := newEngine()
	st := attach(e, "c1", upod("1", "ns", "a", "n1", "100m", "", "1000m", ""))
	sn := snapFor(st, usage.VerBase, nil, map[string][2]uint32{"a": {500, 1}})
	e.SetUsage("c1", ingest.PodsGVR, sn)
	sub, _ := e.Subscribe(protocol.ViewQuery{GVR: ingest.PodsGVR, Sort: []protocol.Sort{{Col: "cpuUse"}}}, 1, win(0, 10))
	defer sub.Close()
	if sub.v.slot(&sub.v.entries[0]) == nil {
		t.Fatal("setup: no slot")
	}

	// The same store re-attached (a reselect inside the grace period) keeps the snapshot...
	e.AttachKind("c1", ingest.PodsGVR, st)
	if sub.v.slot(&sub.v.entries[0]) == nil {
		t.Error("re-attaching the same store must keep usage")
	}
	// ...a different store never inherits it, and the old snapshot is refused (invariant 1).
	st2 := store.New()
	_ = st2.Add(upod("1", "ns", "a", "n1", "100m", "", "1000m", ""))
	e.AttachKind("c1", ingest.PodsGVR, st2)
	if sub.v.slot(&sub.v.entries[0]) != nil {
		t.Fatal("usage survived a store swap")
	}
	e.SetUsage("c1", ingest.PodsGVR, sn)
	if sub.v.slot(&sub.v.entries[0]) != nil {
		t.Fatal("a snapshot built from another store was accepted")
	}
	e.SetUsage("c1", ingest.PodsGVR, snapFor(st2, usage.VerBase+1, nil, map[string][2]uint32{"a": {500, 1}}))
	if sub.v.slot(&sub.v.entries[0]) == nil {
		t.Fatal("a snapshot of the attached store must be accepted")
	}

	// Detach clears it, and a snapshot for a detached pair is ignored.
	e.DetachKind("c1", ingest.PodsGVR)
	e.SetUsage("c1", ingest.PodsGVR, snapFor(st2, usage.VerBase+2, nil, nil))
	if rec := e.srcs[srcKey{e.idx["c1"], e.gvrs[ingest.PodsGVR]}]; rec.use != nil {
		t.Fatal("a detached pair accepted a snapshot")
	}
	e.AttachKind("c1", ingest.PodsGVR, st2)
	if sub2, _ := e.Subscribe(protocol.ViewQuery{GVR: ingest.PodsGVR}, 1, win(0, 10)); len(sub2.v.use) > 0 && sub2.v.use[0] != nil {
		t.Fatal("usage survived a detach")
	}
}

func TestNewViewStartsWithTheCurrentSnapshot(t *testing.T) {
	e := newEngine()
	st := attach(e, "c1", upod("1", "ns", "a", "n1", "100m", "", "1000m", ""), upod("2", "ns", "b", "n1", "100m", "", "1000m", ""))
	e.SetUsage("c1", ingest.PodsGVR, snapFor(st, usage.VerBase, nil, map[string][2]uint32{"a": {100, 1}, "b": {900, 1}}))
	sub, _ := e.Subscribe(protocol.ViewQuery{GVR: ingest.PodsGVR, Sort: []protocol.Sort{{Col: "cpuUse", Desc: true}}}, 1, win(0, 10))
	defer sub.Close()
	if got := entryNames(sub.v); !slices.Equal(got, []string{"b", "a"}) {
		t.Fatalf("a view created after the poll must sort with its snapshot: %v", got)
	}
}

func TestUsageOrdUnits(t *testing.T) {
	row := upod("1", "ns", "a", "n1", "200m", "256Mi", "400m", "512Mi")
	sl := &usage.Slot{Has: true, CPU: 100, MemKi: 128 << 10, NodeCPU: 4000, NodeMemMi: 1024, Ver: usage.VerBase}
	cases := []struct {
		k    ingest.UseKind
		want uint64
		rank uint8
	}{
		{ingest.UseCPU, 100, 0}, {ingest.UseMem, 128 << 10, 0},
		{ingest.UseCPUPctReq, 500, 0}, {ingest.UseCPUPctLim, 250, 0}, {ingest.UseCPUPctNode, 25, 0},
		{ingest.UseMemPctReq, 500, 0}, {ingest.UseMemPctLim, 250, 0}, {ingest.UseMemPctNode, 125, 0},
		{ingest.ResCPUReq, 200, 0}, {ingest.ResCPULim, 400, 0}, {ingest.ResMemReq, 256 << 10, 0}, {ingest.ResMemLim, 512 << 10, 0},
	}
	for _, c := range cases {
		if v, r := usageOrd(c.k, row, sl); v != c.want || r != c.rank {
			t.Errorf("kind %d = (%d, %d), want (%d, %d)", c.k, v, r, c.want, c.rank)
		}
	}
	// half-up rounding: 1/3 of 1000 per-mille is 333, 2/3 is 667
	if v, _ := pct(1, 3); v != 333 {
		t.Errorf("pct(1,3) = %d", v)
	}
	if v, _ := pct(2, 3); v != 667 {
		t.Errorf("pct(2,3) = %d", v)
	}
	if _, r := usageOrd(ingest.UseCPU, row, nil); r != rankNone {
		t.Error("no slot is none")
	}
	if _, r := usageOrd(ingest.UseCPU, row, &usage.Slot{Ver: 1}); r != rankNone {
		t.Error("a slot without a sample is none")
	}
	bare := upod("2", "ns", "b", "n1", "", "", "", "")
	if _, r := usageOrd(ingest.UseCPUPctReq, bare, sl); r != rankNA {
		t.Error("no request is n/a")
	}
	if _, r := usageOrd(ingest.ResCPUReq, bare, nil); r != rankNone {
		t.Error("no request amount is none (sent as nil)")
	}
	if _, r := usageOrd(ingest.UseCPUPctNode, row, &usage.Slot{Has: true, CPU: 1, Ver: 1}); r != rankNA {
		t.Error("an unknown node is n/a")
	}
}
