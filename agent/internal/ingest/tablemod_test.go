package ingest

import (
	"testing"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// A Table with a duplicated column name ("Name" twice, as policy reports have), priority-1 count columns and
// metadata labels: the curated Table module reads all of them without a body.
const tableJSON = `{"kind":"Table","columnDefinitions":[
 {"name":"Name","type":"string","priority":0},{"name":"Kind","type":"string","priority":0},{"name":"Name","type":"string","priority":0},
 {"name":"Critical","type":"integer","priority":1},{"name":"Age","type":"date","priority":0}],
 "rows":[{"cells":["uid-1","Deployment","api",4,"5m"],"object":{"metadata":{"name":"uid-1","namespace":"shop","uid":"u1","resourceVersion":"7",
  "creationTimestamp":"2026-11-17T08:21:00Z","labels":{"app.kubernetes.io/managed-by":"x","res.kind":"ReplicaSet"},
  "ownerReferences":[{"kind":"ReplicaSet","name":"api-abc","controller":true}]}}}]}`

func TestTableModuleMapsHeadersLabelsAndOwner(t *testing.T) {
	m := NewTableModule(TableModuleSpec{GVR: "x.io/v1/things", Kind: "Thing", Namespaced: true,
		Cells: []TableCellSpec{
			{Key: "scopeKind", Type: protocol.ColDict, Header: "Kind"},
			{Key: "scopeName", Type: protocol.ColStr, Header: "Name#2"},
			{Key: "critical", Type: protocol.ColInt, Header: "Critical"},
			{Key: "resKind", Type: protocol.ColDict, LabelKey: "res.kind"},
			{Key: "missing", Type: protocol.ColStr, Header: "Nope"},
		},
		Tone: func(r *Row) uint8 {
			if r.Cell(2).N > 0 {
				return ToneDanger
			}
			return ToneSuccess
		}})
	Register(m)
	tbl, err := DecodeTable([]byte(tableJSON))
	if err != nil {
		t.Fatal(err)
	}
	r := m.RowFromTable(tbl.Cols, &tbl.Rows[0])
	if got := m.StrOf(r, "scopeKind"); got != "Deployment" {
		t.Errorf("scopeKind %q", got)
	}
	if got := m.StrOf(r, "scopeName"); got != "api" {
		t.Errorf("second Name column: %q", got)
	}
	if got := m.IntOf(r, "critical"); got != 4 {
		t.Errorf("priority-1 column dropped: %d", got)
	}
	if got := m.StrOf(r, "resKind"); got != "ReplicaSet" {
		t.Errorf("label cell %q", got)
	}
	if m.StrOf(r, "missing") != "" || r.Tone() != ToneDanger {
		t.Errorf("tone %d", r.Tone())
	}
	if r.OwnerKind().Value() != "ReplicaSet" || r.OwnerName().Value() != "api-abc" || r.Name() != "uid-1" || r.GetNamespace() != "shop" {
		t.Errorf("owner/identity %v %v", r.OwnerKind().Value(), r.Name())
	}
	// a later watch event without columnDefinitions keeps using the first mapping
	r2 := m.ProjectTableRow(&tbl.Rows[0], m.mapFor(tbl.Cols))
	if !r.CellsEqual(r2) {
		t.Error("rows differ")
	}
}
