package view

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// Regression: wire ids must not be reused when a cluster's store is replaced (deselect, grace expiry,
// reselect starts a new store whose handles and generations restart).
func TestWireIDsNotReusedAcrossStoreReplacement(t *testing.T) {
	e := newEngine()
	attach(e, "c1", pod(spec{uid: "old", ns: "a", name: "old-pod"}))
	s, _ := e.Subscribe(protocol.ViewQuery{}, 1, win(0, 10))
	m := newModel()
	m.pull(t, s)
	oldID := m.ids[0]
	e.Detach("c1")
	m.pull(t, s)
	attach(e, "c1", pod(spec{uid: "new", ns: "a", name: "new-pod"})) // new store: handle 0, gen 1 again
	m.pull(t, s)
	if len(m.ids) != 1 || m.rows[m.ids[0]].Row.Name() != "new-pod" {
		t.Fatalf("unexpected view: %v", m.names())
	}
	if m.ids[0] == oldID {
		t.Fatalf("wire id %d was reused for a different object", oldID)
	}
}

// Regression: when the anchor row disappears the window keeps its previous position instead of
// reading the anchor-relative offset as an absolute one.
func TestAnchorMissingKeepsOffset(t *testing.T) {
	e := newEngine()
	var rows []*ingest.PodRow
	for i := 0; i < 100; i++ {
		rows = append(rows, pod(spec{uid: fmt.Sprint("u", i), ns: "a", name: fmt.Sprintf("p%03d", i)}))
	}
	st := attach(e, "c1", rows...)
	s, _ := e.Subscribe(protocol.ViewQuery{}, 1, win(40, 5))
	m := newModel()
	m.pull(t, s)
	anchor := m.ids[1]
	_ = s.Update(nil, 1, &protocol.Window{AnchorID: anchor, Offset: -1, Limit: 5})
	m.pull(t, s)
	if m.offset != 40 {
		t.Fatalf("offset %d", m.offset)
	}
	_ = st.Delete(rows[41]) // the anchor (p041)
	e.Pump()
	m.pull(t, s)
	if m.offset != 40 {
		t.Fatalf("window jumped to %d after the anchor disappeared", m.offset)
	}
}

// Regression: an anchor row that changes and re-sorts far away does not drag the window along.
func TestAnchorResortedFarAwayKeepsWindow(t *testing.T) {
	e := newEngine()
	var rows []*ingest.PodRow
	for i := 0; i < 100; i++ {
		rows = append(rows, pod(spec{uid: fmt.Sprint("u", i), ns: "a", name: fmt.Sprintf("p%03d", i)}))
	}
	st := attach(e, "c1", rows...)
	s, _ := e.Subscribe(protocol.ViewQuery{}, 1, win(40, 5))
	m := newModel()
	m.pull(t, s)
	anchor := m.ids[1]
	_ = s.Update(nil, 1, &protocol.Window{AnchorID: anchor, Offset: -1, Limit: 5})
	m.pull(t, s)
	// the anchor pod starts crash-looping: the default sort moves it to the very top
	_ = st.Update(pod(spec{uid: "u41", ns: "a", name: "p041", state: "crash"}))
	e.Pump()
	m.pull(t, s)
	if m.offset != 40 {
		t.Fatalf("window followed the re-sorted anchor to %d", m.offset)
	}
}

// Regression: building a view while a large feed is pending applies it in bulk (consistent result,
// no per-event application into every existing view).
func TestViewForPumpsPendingFeedFirst(t *testing.T) {
	e := newEngine()
	var rows []*ingest.PodRow
	for i := 0; i < 20000; i++ {
		rows = append(rows, pod(spec{uid: fmt.Sprint("u", i), ns: fmt.Sprint("n", i%7), name: fmt.Sprintf("p%05d", i)}))
	}
	st := attach(e, "c1", rows...)
	var subs []*Sub
	for _, q := range []protocol.ViewQuery{{}, {Namespaces: []string{"n1"}}, {Text: "p1"}} {
		s, _ := e.Subscribe(q, 1, win(0, 10))
		subs = append(subs, s)
	}
	for i := 0; i < 20000; i += 2 { // pending feed, not pumped
		_ = st.Update(pod(spec{uid: fmt.Sprint("u", i), ns: fmt.Sprint("n", i%7), name: fmt.Sprintf("p%05d", i), state: "crash"}))
	}
	start := time.Now()
	s4, _ := e.Subscribe(protocol.ViewQuery{Filters: []protocol.Filter{{Col: "statusTone", Op: "in", Value: []string{"3"}}}}, 1, win(0, 10))
	t.Logf("subscribe with 10k pending events and 3 existing views: %v", time.Since(start))
	if s4.Total() != 10000 {
		t.Fatalf("new view total %d, want 10000", s4.Total())
	}
	e.Pump()
	for _, v := range e.views {
		if got, want := viewNames(v), bruteForce(e, v); !slices.Equal(got, want) {
			t.Fatalf("view %q diverged from the stores", v.n.Key)
		}
	}
}

func TestGenericKindView(t *testing.T) {
	tr := &ingest.TableResult{Cols: []ingest.TableCol{{Name: "Name"}, {Name: "Ready", Type: "string"}, {Name: "Replicas", Type: "integer"}, {Name: "Age", Type: "date"}}}
	m := ingest.GenericModule("viewtest.io/v1/gadgets", "Gadget", true, nil, tr.Cols)
	st := store.New()
	mapping := []int{-1, 0, 1, -1}
	for i, row := range []struct {
		name, ns, ready string
		n               float64
	}{{"c", "a", "True", 3}, {"a", "b", "False", 1}, {"b", "a", "True", 7}} {
		r := m.ProjectTableRow(&ingest.TableRow{Cells: []any{row.name, row.ready, row.n}, UID: fmt.Sprint("u", i), Name: row.name, Namespace: row.ns, RV: "1"}, mapping)
		_ = st.Add(r)
	}
	e := newEngine()
	e.AttachKind("c1", m.GVR, st)
	s, err := e.Subscribe(protocol.ViewQuery{GVR: m.GVR, Sort: []protocol.Sort{{Col: "replicas", Desc: true}}}, 1, win(0, 10))
	if err != nil {
		t.Fatal(err)
	}
	mod := newModel()
	mod.pull(t, s)
	var names []string
	for _, id := range mod.ids {
		names = append(names, mod.rows[id].Row.Name())
	}
	if !slices.Equal(names, []string{"b", "c", "a"}) {
		t.Fatalf("sorted by replicas desc: %v", names)
	}
	s2, _ := e.Subscribe(protocol.ViewQuery{GVR: m.GVR, Filters: []protocol.Filter{{Col: "ready", Op: "eq", Value: "False"}}}, 1, win(0, 10))
	if s2.Total() != 1 {
		t.Fatalf("filter on a generic column: %d", s2.Total())
	}
	s3, _ := e.Subscribe(protocol.ViewQuery{GVR: m.GVR, Namespaces: []string{"a"}}, 1, win(0, 10))
	if s3.Total() != 2 {
		t.Fatalf("namespace filter: %d", s3.Total())
	}
	if _, err := e.Subscribe(protocol.ViewQuery{GVR: m.GVR, Sort: []protocol.Sort{{Col: "nope"}}}, 1, win(0, 10)); err == nil {
		t.Fatal("unknown sort column must fail")
	}
	if _, err := e.Subscribe(protocol.ViewQuery{GVR: "no.such/v1/kind"}, 1, win(0, 10)); err == nil {
		t.Fatal("unknown gvr must fail")
	}
	// pods and the generic kind use different views with independent ids
	if mod.rows[mod.ids[0]].Row.Cell(1).N != 7 {
		t.Fatal("cell read")
	}
}
