package ingest

import (
	"strconv"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// A curated Table module (docs/performance.md §4, mode C): the API server renders the printer columns, the
// agent maps the ones it wants to fixed cells by column name (priority does not matter: the severity counts
// of Trivy reports are priority-1 "wide" columns), and reads a few labels (the workload a report is about)
// from the PartialObjectMetadata of each row. No report body is ever decoded.

type labelCell struct {
	label string
	idx   int
}

// TableCellSpec declares one cell of a curated Table module.
type TableCellSpec struct {
	Key, Label string
	Type       protocol.ColumnType // ColDict, ColStr, ColInt or ColTime
	Width      int
	// Header is the printer column this cell reads. A column name that appears twice is addressed as "Name"
	// and "Name#2" (policy reports have two "Name" columns). Empty: the cell reads LabelKey instead.
	Header   string
	LabelKey string
}

// TableModuleSpec describes a curated Table module.
type TableModuleSpec struct {
	GVR, Kind  string
	Namespaced bool
	Short      []string
	Cells      []TableCellSpec
	// Tone computes the row tone from the filled cells (nil: no tone column).
	Tone func(r *Row) uint8
	// Optional marks a CRD-backed kind (watched only where discovery lists it); Tier0 starts it with the baseline.
	Optional, Tier0 bool
	// GateClass: watched as part of the baseline only while this problem class is enabled.
	GateClass string
}

func typeOf(t protocol.ColumnType) string {
	switch t {
	case protocol.ColInt:
		return "integer"
	case protocol.ColTime:
		return "date"
	}
	return "string"
}

// NewTableModule builds (without registering) a curated Table module. Cell i of the row is cell i of the spec.
func NewTableModule(sp TableModuleSpec) *Module {
	m := &Module{GVR: sp.GVR, Kind: sp.Kind, Namespaced: sp.Namespaced, Short: sp.Short, Table: true, Curated: true, NoWatchList: true,
		Optional: sp.Optional, Tier0: sp.Tier0, GateClass: sp.GateClass, toneCell: -1, toneFn: sp.Tone, headerIdx: map[string]int{}}
	m.Cols = []Col{cCluster(), cName()}
	if sp.Namespaced {
		m.Cols = append(m.Cols, cNS())
	}
	for i, c := range sp.Cells {
		col := Col{Column: protocol.Column{Key: c.Key, Type: c.Type, Label: c.Label, Width: c.Width}, Src: SrcCell, Sortable: true, Filterable: c.Type != protocol.ColTime}
		if c.Type == protocol.ColInt {
			col.Align = "right"
		}
		m.Cols = append(m.Cols, col)
		m.tableTypes = append(m.tableTypes, typeOf(c.Type))
		if c.Header != "" {
			m.headerIdx[c.Header] = i
		} else if c.LabelKey != "" {
			m.labelCells = append(m.labelCells, labelCell{label: c.LabelKey, idx: i})
		}
	}
	if sp.Tone != nil {
		m.Cols = append(m.Cols, cTone())
	}
	m.Cols = append(m.Cols, cAge())
	return m
}

// mapHeaders maps a Table's columns to cell indexes by header name (second occurrence: "Name#2").
func (m *Module) mapHeaders(cols []TableCol) []int {
	out := make([]int, len(cols))
	seen := map[string]int{}
	for i, c := range cols {
		seen[c.Name]++
		key := c.Name
		if n := seen[c.Name]; n > 1 {
			key += "#" + strconv.Itoa(n)
		}
		idx, ok := m.headerIdx[key]
		if !ok {
			idx = -1
		}
		out[i] = idx
	}
	return out
}

// StrOf returns the string of cell i of a row by module column key ("" when the column is unknown).
func (m *Module) StrOf(r *Row, key string) string {
	if c := m.Col(key); c != nil && c.Src == SrcCell {
		return r.StrCell(c.Idx)
	}
	return ""
}

// IntOf returns the integer of cell i of a row by module column key.
func (m *Module) IntOf(r *Row, key string) int64 {
	if c := m.Col(key); c != nil && c.Src == SrcCell {
		return r.Cell(c.Idx).N
	}
	return 0
}

// RowFromTable projects one row of a Table whose columnDefinitions are cols (tests, replays).
func (m *Module) RowFromTable(cols []TableCol, tr *TableRow) *Row {
	return m.ProjectTableRow(tr, m.mapFor(cols))
}
