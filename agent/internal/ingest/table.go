package ingest

// Ingest mode C: server-side Table. The API server renders the printer columns (CRDs: the
// additionalPrinterColumns; built-ins: their kubectl columns) and, with includeObject=Metadata, a
// PartialObjectMetadata per row. The agent therefore never decodes a CRD object or a Secret value.

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TableAccept is the Accept header of Table-mode requests.
const TableAccept = "application/json;as=Table;v=v1;g=meta.k8s.io"

// TableCol is a Table columnDefinition.
type TableCol struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Format   string `json:"format"`
	Priority int32  `json:"priority"`
}

// TableRow is one row: the printer cells and the metadata of the object they describe.
type TableRow struct {
	Cells     []any
	UID       string
	Name      string
	Namespace string
	RV        string
	Created   int64 // unix ms
	Deleting  bool
}

// TableResult is a decoded Table (a list response, or one watch event). It is a runtime.Object so
// it can travel through client-go's watch.Event.
type TableResult struct {
	Cols        []TableCol // only present on a list response and on the first event of a watch
	Rows        []TableRow
	RV          string
	Continue    string
	Remaining   *int64
	Annotations map[string]string
	Bookmark    bool // a BOOKMARK event: no rows, only RV and annotations
}

func (*TableResult) GetObjectKind() schema.ObjectKind { return schema.EmptyObjectKind }
func (t *TableResult) DeepCopyObject() runtime.Object {
	c := *t
	return &c
}

type tableDoc struct {
	Kind     string `json:"kind"`
	Metadata struct {
		ResourceVersion    string            `json:"resourceVersion"`
		Continue           string            `json:"continue"`
		RemainingItemCount *int64            `json:"remainingItemCount"`
		Annotations        map[string]string `json:"annotations"`
	} `json:"metadata"`
	Cols []TableCol `json:"columnDefinitions"`
	Rows []struct {
		Cells  []any `json:"cells"`
		Object struct {
			Metadata struct {
				Name              string  `json:"name"`
				Namespace         string  `json:"namespace"`
				UID               string  `json:"uid"`
				ResourceVersion   string  `json:"resourceVersion"`
				CreationTimestamp string  `json:"creationTimestamp"`
				DeletionTimestamp *string `json:"deletionTimestamp"`
			} `json:"metadata"`
		} `json:"object"`
	} `json:"rows"`
	// Status objects (watch ERROR events, failed lists)
	Status  string `json:"status"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
	Code    int32  `json:"code"`
}

func (d *tableDoc) result() *TableResult {
	t := &TableResult{Cols: d.Cols, RV: d.Metadata.ResourceVersion, Continue: d.Metadata.Continue,
		Remaining: d.Metadata.RemainingItemCount, Annotations: d.Metadata.Annotations}
	t.Rows = make([]TableRow, len(d.Rows))
	for i := range d.Rows {
		r := &d.Rows[i]
		m := &r.Object.Metadata
		row := TableRow{Cells: r.Cells, UID: m.UID, Name: m.Name, Namespace: m.Namespace, RV: m.ResourceVersion, Deleting: m.DeletionTimestamp != nil}
		if m.CreationTimestamp != "" {
			if ts, err := time.Parse(time.RFC3339, m.CreationTimestamp); err == nil {
				row.Created = ts.UnixMilli()
			}
		}
		t.Rows[i] = row
	}
	return t
}

// DecodeTable parses a Table list response. A Status body (an error) is returned as a *metav1.Status error.
func DecodeTable(b []byte) (*TableResult, error) {
	var d tableDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	if d.Kind == "Status" {
		return nil, &StatusError{metav1.Status{Status: d.Status, Message: d.Message, Reason: metav1.StatusReason(d.Reason), Code: d.Code}}
	}
	return d.result(), nil
}

// StatusError wraps a Status returned in a response body.
type StatusError struct{ S metav1.Status }

func (e *StatusError) Error() string { return e.S.Message }

// TableEvent is one decoded watch event.
type TableEvent struct {
	Type   string
	Table  *TableResult
	Status *metav1.Status
}

// TableEventDecoder reads concatenated JSON watch events.
type TableEventDecoder struct{ dec *json.Decoder }

func NewTableEventDecoder(r io.Reader) *TableEventDecoder {
	return &TableEventDecoder{dec: json.NewDecoder(r)}
}

func (d *TableEventDecoder) Next() (*TableEvent, error) {
	var ev struct {
		Type   string   `json:"type"`
		Object tableDoc `json:"object"`
	}
	if err := d.dec.Decode(&ev); err != nil {
		return nil, err
	}
	te := &TableEvent{Type: ev.Type}
	switch {
	case ev.Type == "ERROR" || ev.Object.Kind == "Status":
		te.Status = &metav1.Status{Status: ev.Object.Status, Message: ev.Object.Message, Reason: metav1.StatusReason(ev.Object.Reason), Code: ev.Object.Code}
		if te.Status.Status == "" {
			te.Status.Status = metav1.StatusFailure
		}
	case ev.Type == "BOOKMARK":
		te.Table = &TableResult{Bookmark: true, RV: ev.Object.Metadata.ResourceVersion, Annotations: ev.Object.Metadata.Annotations}
	default:
		te.Table = ev.Object.result()
	}
	return te, nil
}

// ---- generic modules ---------------------------------------------------------------------

var keySlug = regexp.MustCompile(`[^a-z0-9]+`)

var reservedKeys = map[string]bool{"cluster": true, "name": true, "namespace": true, "createdAt": true, "statusTone": true}

func colKey(name string, used map[string]bool) string {
	k := keySlug.ReplaceAllString(strings.ToLower(name), "")
	if k == "" {
		k = "col"
	}
	for reservedKeys[k] || used[k] {
		k += "_"
	}
	used[k] = true
	return k
}

var toneColNames = map[string]bool{"ready": true, "status": true, "synced": true, "healthy": true, "available": true, "state": true, "phase": true}

// GenericModule builds (or returns the already registered) module for a Table-mode kind from the
// first columnDefinitions seen. Columns: name, namespace (when namespaced), the printer columns
// with priority 0, a severity tone when a Ready/Status-like column exists, and age.
func GenericModule(gvr, kind string, namespaced bool, short []string, cols []TableCol) *Module {
	if m := Lookup(gvr); m != nil {
		return m
	}
	m := &Module{GVR: gvr, Kind: kind, Namespaced: namespaced, Short: short, Table: true, toneCell: -1}
	m.Cols = []Col{cCluster(), cName()}
	if namespaced {
		m.Cols = append(m.Cols, cNS())
	}
	used := map[string]bool{}
	m.srcMap = make([]int, len(cols))
	cell := 0
	toneCol := ""
	for i, c := range cols {
		m.srcMap[i] = -1
		if c.Priority != 0 || c.Name == "Name" || c.Name == "Age" || c.Name == "Namespace" {
			continue
		}
		key := colKey(c.Name, used)
		col := Col{Column: protocol.Column{Key: key, Label: c.Name, Width: 140}, Src: SrcCell, Sortable: true, Filterable: true}
		switch c.Type {
		case "integer", "number":
			col.Type, col.Align, col.Width = protocol.ColInt, "right", 90
		case "date":
			col.Type, col.Align, col.Width, col.Filterable = protocol.ColTime, "right", 90, false
		case "boolean":
			col.Type = protocol.ColDict
		default:
			col.Type, col.Width = protocol.ColStr, 180
		}
		if toneCol == "" && toneColNames[strings.ToLower(c.Name)] && col.Type != protocol.ColTime {
			toneCol, m.toneCell = c.Name, cell
		}
		m.srcMap[i] = cell
		m.tableTypes = append(m.tableTypes, c.Type)
		m.Cols = append(m.Cols, col)
		cell++
	}
	if toneCol != "" {
		m.Cols = append(m.Cols, cTone())
	}
	m.Cols = append(m.Cols, cAge())
	m.tableCols = append([]TableCol(nil), cols...)
	reg.Lock()
	if ex := reg.mods[gvr]; ex != nil { // lost a race
		reg.Unlock()
		return ex
	}
	reg.Unlock()
	Register(m)
	return m
}

// mapFor returns the table-column → cell-index mapping for cols against m's schema (by column
// name; columns the schema does not know are dropped, missing ones stay empty).
func (m *Module) mapFor(cols []TableCol) []int {
	if len(cols) == 0 {
		return m.srcMap
	}
	out := make([]int, len(cols))
	for i, c := range cols {
		out[i] = -1
		if c.Priority != 0 {
			continue
		}
		for j, mc := range m.tableCols {
			if mc.Name == c.Name && m.srcMap[j] >= 0 {
				out[i] = m.srcMap[j]
				break
			}
		}
	}
	return out
}

// ProjectTableRow turns one table row into a Row using mapping (from mapFor).
func (m *Module) ProjectTableRow(tr *TableRow, mapping []int) *Row {
	r := newRowRaw(tr.UID, tr.Name, tr.RV, tr.Namespace, tr.Created, tr.Deleting, m.NCells)
	r.cells = r.cells[:m.NCells]
	for i := range r.cells {
		r.cells[i] = Cell{S: emptyStr}
	}
	for i, v := range tr.Cells {
		if i >= len(mapping) || mapping[i] < 0 {
			continue
		}
		idx := mapping[i]
		r.cells[idx] = m.convertCell(idx, v)
	}
	if m.toneCell >= 0 {
		r.tone = toneFromText(r.cells[m.toneCell].S.Value())
	}
	return r
}

func (m *Module) convertCell(idx int, v any) Cell {
	typ := "string"
	if idx < len(m.tableTypes) {
		typ = m.tableTypes[idx]
	}
	switch typ {
	case "integer", "number":
		switch x := v.(type) {
		case float64:
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return Cell{S: emptyStr}
			}
			return Cell{S: emptyStr, N: int64(math.Round(x))}
		case string:
			if n, err := strconv.ParseInt(x, 10, 64); err == nil {
				return Cell{S: emptyStr, N: n}
			}
			if f, err := strconv.ParseFloat(x, 64); err == nil {
				return Cell{S: emptyStr, N: int64(math.Round(f))}
			}
		}
		return Cell{S: emptyStr}
	case "date":
		if s, ok := v.(string); ok {
			return Cell{S: emptyStr, N: parseDateCell(s, time.Now())}
		}
		return Cell{S: emptyStr}
	}
	return Cell{S: intern(cellString(v))}
}

func cellString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

var durPart = regexp.MustCompile(`(\d+)([smhdy])`)

// parseDateCell converts a date cell to unix ms. The API server renders `date` printer columns as
// human durations ("5d3h", "2y125d", "45s") relative to now; those become now minus the duration,
// rounded to the minute so an unchanged object does not look changed on a relist. RFC 3339 passes through.
func parseDateCell(s string, now time.Time) int64 {
	if s == "" || s == "<none>" || s == "<unknown>" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixMilli()
	}
	parts := durPart.FindAllStringSubmatch(s, -1)
	if len(parts) == 0 {
		return 0
	}
	var d time.Duration
	for _, p := range parts {
		n, _ := strconv.Atoi(p[1])
		switch p[2] {
		case "s":
			d += time.Duration(n) * time.Second
		case "m":
			d += time.Duration(n) * time.Minute
		case "h":
			d += time.Duration(n) * time.Hour
		case "d":
			d += time.Duration(n) * 24 * time.Hour
		case "y":
			d += time.Duration(n) * 365 * 24 * time.Hour
		}
	}
	ms := now.Add(-d).UnixMilli()
	return ms - ms%60000
}

// toneFromText classifies a status-like printer value.
func toneFromText(v string) uint8 {
	s := strings.ToLower(strings.TrimSpace(v))
	switch s {
	case "":
		return ToneNeutral
	case "true", "ready", "running", "active", "bound", "healthy", "available", "synced", "succeeded", "complete", "ok", "up", "approved", "issued":
		return ToneSuccess
	case "false", "failed", "error", "notready", "degraded", "unhealthy", "lost", "denied", "crashloopbackoff", "invalid":
		return ToneDanger
	case "unknown", "pending", "progressing", "terminating", "reconciling", "suspended", "unavailable":
		return ToneWarning
	case "completed":
		return ToneInfo
	}
	if a, b, ok := strings.Cut(s, "/"); ok {
		x, e1 := strconv.Atoi(a)
		y, e2 := strconv.Atoi(b)
		if e1 == nil && e2 == nil {
			switch {
			case y == 0 || x >= y:
				return ToneSuccess
			case x == 0:
				return ToneDanger
			}
			return ToneWarning
		}
	}
	return ToneNeutral
}

// ---- watch event bridging ----------------------------------------------------------------

// DecodeTableBytes is a convenience for tests.
func DecodeTableBytes(b []byte) (*TableResult, error) { return DecodeTable(bytes.TrimSpace(b)) }
