package ingest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

const listJSON = `{"kind":"Table","apiVersion":"meta.k8s.io/v1","metadata":{"resourceVersion":"77","continue":"tok","remainingItemCount":3},
"columnDefinitions":[
 {"name":"Name","type":"string","format":"name","priority":0},
 {"name":"Ready","type":"string","priority":0},
 {"name":"Replicas","type":"integer","priority":0},
 {"name":"Synced","type":"boolean","priority":0},
 {"name":"Detail","type":"string","priority":1},
 {"name":"Age","type":"date","priority":0}],
"rows":[
 {"cells":["web","True",3,true,"hidden","5d"],"object":{"kind":"PartialObjectMetadata","metadata":{"name":"web","namespace":"prod","uid":"u1","resourceVersion":"70","creationTimestamp":"2024-01-02T03:04:05Z"}}},
 {"cells":["api","False",null,false,"x","1h"],"object":{"kind":"PartialObjectMetadata","metadata":{"name":"api","namespace":"prod","uid":"u2","resourceVersion":"71","deletionTimestamp":"2024-02-01T00:00:00Z"}}}]}`

func TestDecodeTable(t *testing.T) {
	tr, err := DecodeTable([]byte(listJSON))
	if err != nil {
		t.Fatal(err)
	}
	if tr.RV != "77" || tr.Continue != "tok" || tr.Remaining == nil || *tr.Remaining != 3 || len(tr.Cols) != 6 || len(tr.Rows) != 2 {
		t.Fatalf("decoded: %+v", tr)
	}
	r := tr.Rows[0]
	if r.Name != "web" || r.Namespace != "prod" || r.UID != "u1" || r.RV != "70" || r.Created != 1704164645000 || r.Deleting {
		t.Fatalf("row: %+v", r)
	}
	if !tr.Rows[1].Deleting {
		t.Fatal("deletionTimestamp not decoded")
	}
	if _, err := DecodeTable([]byte(`{"kind":"Status","status":"Failure","message":"forbidden","code":403,"reason":"Forbidden"}`)); err == nil {
		t.Fatal("a Status body must be an error")
	}
}

func TestGenericModuleColumnsAndProjection(t *testing.T) {
	tr, _ := DecodeTable([]byte(listJSON))
	m := GenericModule("example.io/v1/widgets", "Widget", true, []string{"wd"}, tr.Cols)
	var keys []string
	for _, c := range m.Cols {
		keys = append(keys, c.Key+":"+string(c.Type))
	}
	want := "cluster:dict,name:str,namespace:dict,ready:str,replicas:int,synced:dict,statusTone:tone,createdAt:time"
	if strings.Join(keys, ",") != want {
		t.Fatalf("columns\n got %s\nwant %s", strings.Join(keys, ","), want)
	}
	if m.Col("replicas").Align != "right" || m.Col("ready").Label != "Ready" {
		t.Fatal("column presentation")
	}
	if GenericModule("example.io/v1/widgets", "Other", false, nil, nil) != m {
		t.Fatal("GenericModule must return the registered module")
	}
	mapping := m.mapFor(tr.Cols)
	r0 := m.ProjectTableRow(&tr.Rows[0], mapping)
	if got := cellStrings(m, r0); got != "True|3|true" || r0.Tone() != ToneSuccess || r0.Name() != "web" || r0.GetNamespace() != "prod" {
		t.Fatalf("row0: %q tone %d", got, r0.Tone())
	}
	r1 := m.ProjectTableRow(&tr.Rows[1], mapping)
	if got := cellStrings(m, r1); got != "False|0|false" || r1.Tone() != ToneDanger || !r1.Deleting() {
		t.Fatalf("row1: %q tone %d", got, r1.Tone())
	}
	// A second cluster with a different column order maps by name.
	other := []TableCol{{Name: "Name"}, {Name: "Replicas", Type: "integer"}, {Name: "Extra", Type: "string"}, {Name: "Ready", Type: "string"}}
	mp := m.mapFor(other)
	row := TableRow{Cells: []any{"x", float64(9), "ignored", "True"}, UID: "u", Name: "x", RV: "1"}
	if got := cellStrings(m, m.ProjectTableRow(&row, mp)); got != "True|9|" {
		t.Fatalf("remapped: %q", got)
	}
}

func TestDateCells(t *testing.T) {
	now := time.Date(2025, 1, 10, 12, 0, 30, 0, time.UTC)
	if got := parseDateCell("2d3h", now); got != now.Add(-51*time.Hour).UnixMilli()/60000*60000 {
		t.Errorf("2d3h = %d", got)
	}
	if got := parseDateCell("2024-01-02T03:04:05Z", now); got != 1704164645000 {
		t.Errorf("rfc3339 = %d", got)
	}
	if parseDateCell("<none>", now) != 0 || parseDateCell("", now) != 0 {
		t.Error("empty date cells must be 0")
	}
	if toneFromText("1/3") != ToneWarning || toneFromText("0/2") != ToneDanger || toneFromText("2/2") != ToneSuccess || toneFromText("Running") != ToneSuccess {
		t.Error("tone heuristics")
	}
}

func TestTableWatchDecoding(t *testing.T) {
	body := `{"type":"ADDED","object":` + listJSON + `}
{"type":"BOOKMARK","object":{"kind":"Table","metadata":{"resourceVersion":"80","annotations":{"k8s.io/initial-events-end":"true"}}}}
{"type":"ERROR","object":{"kind":"Status","status":"Failure","code":410,"reason":"Expired","message":"too old"}}`
	w := NewTableWatch(readCloser{strings.NewReader(body)})
	var evs []watch.Event
	for e := range w.ResultChan() {
		evs = append(evs, e)
	}
	if len(evs) != 3 {
		t.Fatalf("events: %d", len(evs))
	}
	if tr := evs[0].Object.(*TableResult); len(tr.Rows) != 2 || len(tr.Cols) != 6 {
		t.Fatal("added event")
	}
	if tr := evs[1].Object.(*TableResult); !tr.Bookmark || tr.RV != "80" || tr.Annotations[metav1.InitialEventsAnnotationKey] != "true" {
		t.Fatalf("bookmark: %+v", tr)
	}
	if st, ok := evs[2].Object.(*metav1.Status); !ok || evs[2].Type != watch.Error || st.Code != 410 {
		t.Fatal("error event")
	}
}

type readCloser struct{ *strings.Reader }

func (readCloser) Close() error { return nil }

type tableAPI struct {
	list   *TableResult
	events []watch.Event
}

func (a *tableAPI) List(context.Context, metav1.ListOptions) (runtime.Object, error) {
	return a.list, nil
}
func (a *tableAPI) Watch(context.Context, metav1.ListOptions) (watch.Interface, error) {
	ch := make(chan watch.Event, len(a.events))
	for _, e := range a.events {
		ch <- e
	}
	close(ch)
	return watch.NewProxyWatcher(ch), nil
}

func TestTableListerWatcherCreatesModuleLazily(t *testing.T) {
	tr, _ := DecodeTable([]byte(listJSON))
	api := &tableAPI{list: tr}
	lw := &ListerWatcher{API: api, Generic: &GenericSpec{GVR: "lazy.io/v1/things", Kind: "Thing", Namespaced: true}}
	if !lw.IsWatchListSemanticsUnSupported() {
		t.Fatal("table kinds must not use WatchList")
	}
	obj, err := lw.ListWithContext(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	l := obj.(*RowList)
	if len(l.Items) != 2 || l.ResourceVersion != "77" || l.Continue != "tok" || Lookup("lazy.io/v1/things") == nil {
		t.Fatalf("list: %d items, module %v", len(l.Items), Lookup("lazy.io/v1/things"))
	}
	// Watch events after the first carry no columns: the mapping of the list is reused.
	api.events = []watch.Event{{Type: watch.Modified, Object: &TableResult{Rows: []TableRow{{Cells: []any{"web", "False", float64(1), false, "h", "1h"}, UID: "u1", Name: "web", Namespace: "prod", RV: "90"}}}},
		{Type: watch.Bookmark, Object: &TableResult{Bookmark: true, RV: "91"}}}
	w, _ := lw.WatchWithContext(context.Background(), metav1.ListOptions{})
	var rows []*Row
	for e := range w.ResultChan() {
		rows = append(rows, e.Object.(*Row))
	}
	m := Lookup("lazy.io/v1/things")
	if len(rows) != 2 || cellStrings(m, rows[0]) != "False|1|false" || rows[0].Tone() != ToneDanger || rows[1].ResourceVersion() != "91" {
		t.Fatalf("watch rows: %d %q", len(rows), cellStrings(m, rows[0]))
	}
	// Without columns and without a module, list fails with a clear error.
	lw2 := &ListerWatcher{API: &tableAPI{list: &TableResult{}}, Generic: &GenericSpec{GVR: "nocols.io/v1/x"}}
	if _, err := lw2.ListWithContext(context.Background(), metav1.ListOptions{}); err == nil {
		t.Fatal("expected no-schema error")
	}
}

func TestGenericColumnKeysAreUniqueAndSafe(t *testing.T) {
	cols := []TableCol{{Name: "Name"}, {Name: "Status"}, {Name: "STATUS"}, {Name: "Created At", Type: "date"}, {Name: "Namespace"}, {Name: "Cluster"}}
	m := GenericModule("keys.io/v1/ks", "K", true, nil, cols)
	seen := map[string]bool{}
	for _, c := range m.Cols {
		if seen[c.Key] {
			t.Fatalf("duplicate key %q", c.Key)
		}
		seen[c.Key] = true
	}
	if m.Col("cluster_") == nil || m.Col("status_") == nil {
		t.Fatalf("reserved keys not escaped: %v", m.Columns())
	}
	var _ = protocol.ColStr
	_ = apierrors.IsGone
}
