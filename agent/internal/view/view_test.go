package view

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type spec struct {
	uid, ns, name, node, state string
	restarts                   int32
}

// pod builds a row. state: run | crash | pending | done.
func pod(s spec) *ingest.PodRow {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.name, Namespace: s.ns, UID: types.UID(s.uid), ResourceVersion: "1"}}
	p.Spec.NodeName = s.node
	p.Spec.Containers = []corev1.Container{{Name: "c"}}
	p.Status.Phase = corev1.PodRunning
	cs := corev1.ContainerStatus{Name: "c", Ready: true, RestartCount: s.restarts, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	switch s.state {
	case "crash":
		cs.Ready, cs.State = false, corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
	case "pending":
		p.Status.Phase = corev1.PodPending
		cs = corev1.ContainerStatus{}
		p.Status.ContainerStatuses = nil
	case "done":
		p.Status.Phase = corev1.PodSucceeded
		cs.Ready, cs.State = false, corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}
	}
	if s.state != "pending" {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{cs}
	}
	return ingest.Project(p)
}

func newEngine() *Engine {
	e := NewEngine()
	e.IdleTTL = 0
	return e
}

func attach(e *Engine, name string, rows ...*ingest.PodRow) *store.Store {
	st := store.New()
	for _, r := range rows {
		_ = st.Add(r)
	}
	e.Attach(name, st)
	return st
}

func win(off, limit int) protocol.Window { return protocol.Window{Offset: off, Limit: limit} }

// model is a client that applies patches exactly as the protocol describes.
type model struct {
	gen     uint32
	total   int
	offset  int
	ids     []uint32
	rows    map[uint32]PendRow
	vers    map[uint32]uint32
	patches int
}

func newModel() *model { return &model{rows: map[uint32]PendRow{}, vers: map[uint32]uint32{}} }

// pull runs one Prepare/Commit cycle and applies it; returns false when nothing was sent.
func (m *model) pull(t *testing.T, s *Sub) bool {
	t.Helper()
	p := s.Prepare()
	if p == nil {
		return false
	}
	m.patches++
	if p.Gen < m.gen {
		t.Fatal("stale gen")
	}
	newGen := p.Gen > m.gen
	m.gen, m.total, m.offset = p.Gen, p.Total, p.Offset
	if p.SendIDs {
		m.ids = slices.Clone(p.IDs)
	}
	for _, id := range p.Removed {
		delete(m.rows, id)
		delete(m.vers, id)
	}
	for _, r := range p.Rows {
		if old, ok := m.vers[r.ID]; ok && !newGen && old >= r.Row.Ver() {
			t.Fatalf("row %d resent with non-increasing version %d -> %d", r.ID, old, r.Row.Ver())
		}
		m.rows[r.ID] = r
		m.vers[r.ID] = r.Row.Ver()
	}
	p.Commit(len(p.Rows), len(p.Removed), p.SendIDs)
	return true
}

func (m *model) names() []string {
	var out []string
	for _, id := range m.ids {
		r, ok := m.rows[id]
		if !ok {
			out = append(out, "<missing>")
			continue
		}
		out = append(out, r.Row.Name()+"/"+r.Row.Status().Value()+fmt.Sprint("/", r.Row.Restarts()))
	}
	return out
}

func TestDefaultSortToneThenNameByteOrder(t *testing.T) {
	e := newEngine()
	attach(e, "c1",
		pod(spec{uid: "1", ns: "a", name: "zeta", state: "run"}),
		pod(spec{uid: "2", ns: "a", name: "Alpha", state: "run"}),
		pod(spec{uid: "3", ns: "a", name: "beta", state: "crash"}),
		pod(spec{uid: "4", ns: "a", name: "gamma", state: "pending"}),
		pod(spec{uid: "5", ns: "a", name: "alpha", state: "run"}),
		pod(spec{uid: "6", ns: "a", name: "aardvark", state: "crash"}),
		pod(spec{uid: "7", ns: "a", name: "done", state: "done"}),
	)
	s, err := e.Subscribe(protocol.ViewQuery{GVR: PodsGVR}, 1, win(0, 100))
	if err != nil {
		t.Fatal(err)
	}
	m := newModel()
	m.pull(t, s)
	got := strings.Join(m.names(), " ")
	// danger (aardvark, beta), warning (gamma), then the rest by byte order: uppercase sorts before lowercase.
	want := "aardvark/CrashLoopBackOff/0 beta/CrashLoopBackOff/0 gamma/Pending/0 Alpha/Running/0 alpha/Running/0 done/Completed/0 zeta/Running/0"
	if got != want {
		t.Fatalf("order:\n got %s\nwant %s", got, want)
	}
}

func TestFiltersNamespacesTextAndSorts(t *testing.T) {
	e := newEngine()
	attach(e, "c1",
		pod(spec{uid: "1", ns: "a", name: "web-1", node: "n1", restarts: 3}),
		pod(spec{uid: "2", ns: "b", name: "web-2", node: "n2", state: "crash", restarts: 9}),
		pod(spec{uid: "3", ns: "b", name: "API-1", node: "n1"}),
		pod(spec{uid: "4", ns: "c", name: "db-1", node: "n3", state: "pending"}),
	)
	attach(e, "c2", pod(spec{uid: "5", ns: "a", name: "web-3", node: "n1", restarts: 1}))
	names := func(q protocol.ViewQuery) string {
		t.Helper()
		s, err := e.Subscribe(q, 1, win(0, 100))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		m := newModel()
		m.pull(t, s)
		var out []string
		for _, id := range m.ids {
			out = append(out, m.rows[id].Row.Name())
		}
		return strings.Join(out, ",")
	}
	cases := []struct {
		q    protocol.ViewQuery
		want string
	}{
		{protocol.ViewQuery{Namespaces: []string{"b"}}, "web-2,API-1"},
		{protocol.ViewQuery{Text: "WEB"}, "web-2,web-1,web-3"},
		{protocol.ViewQuery{Clusters: []string{"c2"}}, "web-3"},
		{protocol.ViewQuery{Filters: []protocol.Filter{{Col: "statusTone", Op: "in", Value: []string{"2", "3"}}}}, "web-2,db-1"},
		{protocol.ViewQuery{Filters: []protocol.Filter{{Col: "node", Op: "eq", Value: "n1"}}}, "API-1,web-1,web-3"},
		{protocol.ViewQuery{Filters: []protocol.Filter{{Col: "status", Op: "eq", Value: "Pending"}}}, "db-1"},
		{protocol.ViewQuery{Sort: []protocol.Sort{{Col: "restarts", Desc: true}}}, "web-2,web-1,web-3,API-1,db-1"},
		{protocol.ViewQuery{Sort: []protocol.Sort{{Col: "cluster"}, {Col: "name", Desc: true}}}, "web-2,web-1,db-1,API-1,web-3"},
		{protocol.ViewQuery{Sort: []protocol.Sort{{Col: "namespace"}, {Col: "node"}}}, "web-1,web-3,API-1,web-2,db-1"},
	}
	for _, c := range cases {
		if got := names(c.q); got != c.want {
			t.Errorf("%+v:\n got %s\nwant %s", c.q, got, c.want)
		}
	}
	if _, err := e.Subscribe(protocol.ViewQuery{Sort: []protocol.Sort{{Col: "bogus"}}}, 1, win(0, 1)); err == nil {
		t.Error("bad sort column must fail")
	}
}

func TestEqualQueriesShareAView(t *testing.T) {
	e := newEngine()
	attach(e, "c1", pod(spec{uid: "1", ns: "a", name: "p"}))
	a, _ := e.Subscribe(protocol.ViewQuery{Namespaces: []string{"b", "a"}, Text: " P "}, 1, win(0, 10))
	b, _ := e.Subscribe(protocol.ViewQuery{Namespaces: []string{"a", "b", "a"}, Text: "p"}, 1, win(5, 3))
	if a.v != b.v || e.Views() != 1 {
		t.Fatal("equal normalized queries must share a view")
	}
	if a.win == b.win {
		t.Fatal("per-subscription state must stay separate")
	}
}

func TestWindowAndAnchor(t *testing.T) {
	e := newEngine()
	var rows []*ingest.PodRow
	for i := 0; i < 100; i++ {
		rows = append(rows, pod(spec{uid: fmt.Sprint("u", i), ns: "a", name: fmt.Sprintf("p%03d", i*2+10)}))
	}
	st := attach(e, "c1", rows...)
	s, _ := e.Subscribe(protocol.ViewQuery{}, 1, win(20, 5))
	m := newModel()
	m.pull(t, s)
	if m.total != 100 || m.offset != 20 || len(m.ids) != 5 || m.rows[m.ids[0]].Row.Name() != "p050" {
		t.Fatalf("window: total=%d offset=%d ids=%d first=%s", m.total, m.offset, len(m.ids), m.rows[m.ids[0]].Row.Name())
	}
	anchor := m.ids[1] // p052
	_ = s.Update(nil, 1, &protocol.Window{AnchorID: anchor, Offset: -1, Limit: 5})
	m.pull(t, s)
	if m.offset != 20 {
		t.Fatalf("anchored window offset = %d, want 20", m.offset)
	}
	// 7 new rows sort above the viewport; the anchor must keep its screen position.
	for i := 0; i < 7; i++ {
		_ = st.Add(pod(spec{uid: fmt.Sprint("n", i), ns: "a", name: fmt.Sprintf("p%03d", i)}))
	}
	e.Pump()
	if !s.Dirty() {
		t.Fatal("subscription not dirty after change")
	}
	m.pull(t, s)
	if m.offset != 27 || m.total != 107 || m.rows[m.ids[1]].Row.Name() != "p052" || m.ids[1] != anchor {
		t.Fatalf("anchor moved: offset=%d total=%d names=%v", m.offset, m.total, m.names())
	}
	// Clamp: asking past the end returns the last full window.
	_ = s.Update(nil, 1, &protocol.Window{Offset: 10000, Limit: 5})
	m.pull(t, s)
	if m.offset != 102 || len(m.ids) != 5 {
		t.Fatalf("clamp: offset=%d len=%d", m.offset, len(m.ids))
	}
}

func TestWireIDsNeverReused(t *testing.T) {
	e := newEngine()
	st := attach(e, "c1", pod(spec{uid: "a", ns: "x", name: "same"}), pod(spec{uid: "z", ns: "x", name: "zzz"}))
	s, _ := e.Subscribe(protocol.ViewQuery{}, 1, win(0, 10))
	m := newModel()
	m.pull(t, s)
	oldID := m.ids[0]
	if m.rows[oldID].Row.Name() != "same" {
		t.Fatal("unexpected order")
	}
	seen := map[uint32]bool{}
	for _, id := range m.ids {
		seen[id] = true
	}
	for round := 0; round < 50; round++ {
		uid := fmt.Sprint("b", round)
		_ = st.Delete(pod(spec{uid: lastUID(st, "same"), ns: "x", name: "same"}))
		_ = st.Add(pod(spec{uid: uid, ns: "x", name: "same"})) // reuses the freed handle
		e.Pump()
		m.pull(t, s)
		id := m.ids[0]
		if m.rows[id].Row.Name() != "same" {
			t.Fatal("unexpected order")
		}
		if seen[id] {
			t.Fatalf("wire id %d reused for a replaced object (round %d)", id, round)
		}
		if id <= oldID {
			t.Fatalf("wire ids must be monotonic: %d after %d", id, oldID)
		}
		seen[id], oldID = true, id
	}
	// The unchanged object kept its id throughout.
	if len(m.ids) != 2 || m.ids[1] != 2 {
		t.Fatalf("stable id lost: %v", m.ids)
	}
}

func lastUID(st *store.Store, name string) string {
	for _, it := range st.Snapshot(nil, nil) {
		if it.Row.Name() == name {
			return it.Row.UID()
		}
	}
	return ""
}

func TestIDsStableAcrossQueryChange(t *testing.T) {
	e := newEngine()
	attach(e, "c1", pod(spec{uid: "1", ns: "a", name: "p1"}), pod(spec{uid: "2", ns: "b", name: "p2"}))
	s, _ := e.Subscribe(protocol.ViewQuery{}, 1, win(0, 10))
	m := newModel()
	m.pull(t, s)
	idOfP2 := m.ids[1]
	_ = s.Update(&protocol.ViewQuery{Namespaces: []string{"b"}}, 2, nil)
	m.pull(t, s)
	if m.gen != 2 || len(m.ids) != 1 || m.ids[0] != idOfP2 {
		t.Fatalf("id changed across query update: %v (want %d)", m.ids, idOfP2)
	}
	if m.total != 1 {
		t.Fatalf("total = %d", m.total)
	}
}

// TestPatchesConvergeToServerView applies random store mutations and checks that a model client fed only
// with patches always shows exactly the server's window, for several queries and windows at once.
func TestPatchesConvergeToServerView(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	e := newEngine()
	st1, st2 := store.New(), store.New()
	e.Attach("c1", st1)
	e.Attach("c2", st2)
	states := []string{"run", "run", "run", "crash", "pending", "done"}
	live := map[string]spec{}
	mk := func() spec {
		n := rng.IntN(1 << 30)
		return spec{uid: fmt.Sprint("u", n), ns: fmt.Sprint("ns", rng.IntN(4)), name: fmt.Sprintf("pod-%04d", rng.IntN(500)),
			node: fmt.Sprint("n", rng.IntN(5)), state: states[rng.IntN(len(states))], restarts: int32(rng.IntN(5))}
	}
	queries := []protocol.ViewQuery{
		{},
		{Namespaces: []string{"ns1", "ns2"}, Sort: []protocol.Sort{{Col: "restarts", Desc: true}, {Col: "name"}}},
		{Text: "pod-01", Filters: []protocol.Filter{{Col: "statusTone", Op: "in", Value: []string{"1", "3"}}}},
		{Clusters: []string{"c2"}, Sort: []protocol.Sort{{Col: "node"}}},
	}
	type client struct {
		s *Sub
		m *model
	}
	var cs []client
	for _, q := range queries {
		s, err := e.Subscribe(q, 1, win(5, 40))
		if err != nil {
			t.Fatal(err)
		}
		cs = append(cs, client{s, newModel()})
	}
	check := func() {
		t.Helper()
		for i, c := range cs {
			for c.m.pull(t, c.s) {
			}
			// Ground truth straight from the view, via a fresh independent subscription.
			e.mu.Lock()
			v := c.s.v
			total := len(v.entries)
			start, end := c.s.bounds(total)
			var want []string
			for _, en := range v.entries[start:end] {
				want = append(want, en.row.Name()+"/"+en.row.Status().Value()+fmt.Sprint("/", en.row.Restarts()))
			}
			e.mu.Unlock()
			if c.m.total != total || c.m.offset != start || !slices.Equal(c.m.names(), want) {
				t.Fatalf("client %d diverged:\n got %v (total %d off %d)\nwant %v (total %d off %d)", i, c.m.names(), c.m.total, c.m.offset, want, total, start)
			}
			// And the view itself equals a brute-force filter+sort of both stores.
			if bf := bruteForce(e, v); bf != nil && !slices.Equal(bf, viewNames(v)) {
				t.Fatalf("view %d != brute force", i)
			}
		}
	}
	for step := 0; step < 3000; step++ {
		stt := st1
		if rng.IntN(2) == 0 {
			stt = st2
		}
		switch r := rng.IntN(10); {
		case r < 4 || len(live) < 20:
			sp := mk()
			live[sp.uid] = sp
			_ = stt.Add(pod(sp))
		case r < 7:
			for uid, sp := range live { // update a random live pod (restarts / state)
				sp.restarts++
				sp.state = states[rng.IntN(len(states))]
				live[uid] = sp
				_ = st1.Update(pod(sp))
				_ = st2.Update(pod(sp))
				break
			}
		default:
			for uid, sp := range live {
				delete(live, uid)
				_ = st1.Delete(pod(sp))
				_ = st2.Delete(pod(sp))
				break
			}
		}
		if step%7 == 0 {
			e.Pump()
			check()
		}
	}
	e.Pump()
	check()
}

func viewNames(v *View) []string {
	out := make([]string, len(v.entries))
	for i, en := range v.entries {
		out[i] = fmt.Sprintf("%d/%s/%s", en.cl, en.row.UID(), en.row.Name())
	}
	return out
}

// bruteForce recomputes a view from the stores with sort.SliceStable-free independent code.
func bruteForce(e *Engine, v *View) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var all []entry
	for _, rec := range e.srcL {
		if rec.gv != v.gv {
			continue
		}
		for _, it := range rec.st.Snapshot(nil, nil) {
			if v.match(it.Row, rec.cl) {
				all = append(all, mkEntry(it.Row, rec.cl, it.H, 0))
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return v.cmp(&all[i], &all[j]) < 0 })
	out := make([]string, len(all))
	for i, en := range all {
		out[i] = fmt.Sprintf("%d/%s/%s", en.cl, en.row.UID(), en.row.Name())
	}
	return out
}

func TestBulkLoadAndDetach(t *testing.T) {
	e := newEngine()
	s, _ := e.Subscribe(protocol.ViewQuery{}, 1, win(0, 10))
	st := store.New()
	e.Attach("late", st)
	var list []interface{}
	for i := 0; i < 5000; i++ { // > rebuildThreshold: exercises the snapshot-merge path
		list = append(list, pod(spec{uid: fmt.Sprint("u", i), ns: "a", name: fmt.Sprintf("p%05d", 5000-i)}))
	}
	_ = st.Replace(list, "1")
	e.Pump()
	m := newModel()
	m.pull(t, s)
	if m.total != 5000 || m.rows[m.ids[0]].Row.Name() != "p00001" {
		t.Fatalf("bulk: total=%d", m.total)
	}
	e.Detach("late")
	m.pull(t, s)
	if m.total != 0 || len(m.ids) != 0 {
		t.Fatalf("detach: total=%d", m.total)
	}
}
