// Package ingest decodes pods from the API server (or the fake source), projects each one
// into a compact *PodRow before the reflector sees it, and discards everything else.
package ingest

import (
	"strings"
	"unique"

	"github.com/zyno-io/v8s/agent/internal/ipx"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Tones mirror protocol.ColTone.
const (
	ToneNeutral uint8 = 0
	ToneSuccess uint8 = 1
	ToneWarning uint8 = 2
	ToneDanger  uint8 = 3
	ToneInfo    uint8 = 4
	ToneRelease uint8 = 5
)

const (
	flagDeleting uint8 = 1 << iota
	flagInitialEventsEnd
	flagTerminal // pod phase Succeeded/Failed (capacity skips these)
	flagVPA      // pods: requests set by a VPA (the store counts these rows; see store.Store.OnVPA)
)

// Res holds the CPU/memory amounts usage columns divide by. Pods: effective requests (CPUReq, MemReq) and
// limits (CPULim, MemLim). Nodes: allocatable in CPUReq/MemReq (limits unused). 0 = none (the percentage shows n/a).
type Res struct {
	CPUReq, CPULim uint32 // millicores
	MemReq, MemLim uint64 // bytes
}

// Str is an interned string.
type Str = unique.Handle[string]

// Cell is one kind-specific column value: S for string/dict columns, N for integer and time columns.
// Cells of string columns always hold an interned (non-zero) S.
type Cell struct {
	S Str
	N int64
}

// Row is the projection of one object the agent holds, whatever its kind. It is immutable once
// published to a store (the store owns Ver). It implements runtime.Object and metav1.Object with
// only name, namespace, uid and resourceVersion so a client-go Reflector can track it.
//
// The common fields cover every kind; kind-specific columns live in cells, in the order the
// module's cell columns are declared. Pods use the layout documented at the pod accessors.
type Row struct {
	noMeta
	uid, name, rv string
	ns            Str
	ownerKind     Str
	ownerName     Str
	created       int64 // unix ms
	ver           uint32
	tone          uint8
	flags         uint8
	res           Res
	cells         []Cell
	facts         any      // kind-specific facts for the problems engine (see facts.go)
	ips           *ipx.Set // addresses the object holds (see ips.go); set at projection only
}

// PodRow is the pod projection (a Row with the pod cell layout).
type PodRow = Row

// allocRow allocates a Row with room for n cells in the same allocation (one object per row).
func allocRow(n int) *Row {
	switch {
	case n == 0:
		return &Row{}
	case n == 5: // pods
		x := &struct {
			Row
			c [5]Cell
		}{}
		x.cells = x.c[:0]
		return &x.Row
	case n <= 2:
		x := &struct {
			Row
			c [2]Cell
		}{}
		x.cells = x.c[:0]
		return &x.Row
	case n <= 4:
		x := &struct {
			Row
			c [4]Cell
		}{}
		x.cells = x.c[:0]
		return &x.Row
	case n <= 6:
		x := &struct {
			Row
			c [6]Cell
		}{}
		x.cells = x.c[:0]
		return &x.Row
	case n <= 8:
		x := &struct {
			Row
			c [8]Cell
		}{}
		x.cells = x.c[:0]
		return &x.Row
	case n <= 12:
		x := &struct {
			Row
			c [12]Cell
		}{}
		x.cells = x.c[:0]
		return &x.Row
	}
	r := &Row{}
	r.cells = make([]Cell, 0, n)
	return r
}

// newRow creates a row for an object with room for n cells. One allocation backs uid, name and
// resourceVersion; the owner (controller reference, else the first) is recorded for every kind.
func newRow(m *metav1.ObjectMeta, n int) *Row { return initRow(m, allocRow(n)) }

// newRowIP1 is newRow for a 6-cell row whose single IP token lives in the same allocation (pods: the common
// case of one address costs no extra object).
func newRowIP1(m *metav1.ObjectMeta, t ipx.Tok) *Row {
	x := &struct {
		Row
		c [6]Cell
		s ipx.Set
		t [1]ipx.Tok
	}{}
	x.cells = x.c[:0]
	x.t[0] = t
	x.s.T = x.t[:]
	x.ips = &x.s
	return initRow(m, &x.Row)
}

func initRow(m *metav1.ObjectMeta, r *Row) *Row {
	var created int64
	if !m.CreationTimestamp.IsZero() {
		created = m.CreationTimestamp.UnixMilli()
	}
	setRowRaw(r, string(m.UID), m.Name, m.ResourceVersion, m.Namespace, created, m.DeletionTimestamp != nil)
	if len(m.OwnerReferences) > 0 {
		o := &m.OwnerReferences[0]
		for i := range m.OwnerReferences {
			if m.OwnerReferences[i].Controller != nil && *m.OwnerReferences[i].Controller {
				o = &m.OwnerReferences[i]
				break
			}
		}
		r.ownerKind, r.ownerName = intern(o.Kind), intern(o.Name)
	}
	return r
}

func newRowRaw(uid, name, rv, ns string, created int64, deleting bool, n int) *Row {
	r := allocRow(n)
	setRowRaw(r, uid, name, rv, ns, created, deleting)
	return r
}

func setRowRaw(r *Row, uid, name, rv, ns string, created int64, deleting bool) {
	all := uid + name + rv
	r.uid = all[:len(uid)]
	r.name = all[len(uid) : len(uid)+len(name)]
	r.rv = all[len(uid)+len(name):]
	r.ns = intern(ns)
	r.ownerKind, r.ownerName = emptyStr, emptyStr
	r.created = created
	if deleting {
		r.flags |= flagDeleting
	}
}

var emptyStr = intern("")

func (r *Row) putS(s string)   { r.cells = append(r.cells, Cell{S: intern(s)}) }
func (r *Row) putH(s Str)      { r.cells = append(r.cells, Cell{S: s}) }
func (r *Row) putN(n int64)    { r.cells = append(r.cells, Cell{S: emptyStr, N: n}) }
func (r *Row) setTone(t uint8) { r.tone = t }

func (r *Row) UID() string      { return r.uid }
func (r *Row) Name() string     { return r.name }
func (r *Row) NS() Str          { return r.ns }
func (r *Row) OwnerKind() Str   { return r.ownerKind }
func (r *Row) OwnerName() Str   { return r.ownerName }
func (r *Row) CreatedMs() int64 { return r.created }
func (r *Row) Ver() uint32      { return r.ver }
func (r *Row) SetVer(v uint32)  { r.ver = v }
func (r *Row) Tone() uint8      { return r.tone }
func (r *Row) Deleting() bool   { return r.flags&flagDeleting != 0 }
func (r *Row) Terminal() bool   { return r.flags&flagTerminal != 0 }

// VPAManaged reports a pod whose requests a VerticalPodAutoscaler set (a non-empty VPA cell). Any kind may ask.
func (r *Row) VPAManaged() bool { return r.flags&flagVPA != 0 }

// Requests returns the pod's effective CPU (millicores) and memory (bytes) requests; 0 = none.
func (r *Row) Requests() (uint32, uint64) { return r.res.CPUReq, r.res.MemReq }

// Limits returns the pod's effective CPU and memory limits; 0 = the pod is unbounded for that resource.
func (r *Row) Limits() (uint32, uint64) { return r.res.CPULim, r.res.MemLim }

// Allocatable returns a node row's allocatable CPU (millicores) and memory (bytes).
func (r *Row) Allocatable() (uint32, uint64) { return r.res.CPUReq, r.res.MemReq }

// SetAllocatable records a node's allocatable amounts; projection only.
func (r *Row) SetAllocatable(cpuMilli uint32, memBytes uint64) {
	r.res = Res{CPUReq: cpuMilli, MemReq: memBytes}
}
func (r *Row) ResourceVersion() string { return r.rv }

// Cell returns kind-specific cell i (zero Cell when out of range).
func (r *Row) Cell(i int) Cell {
	if i < 0 || i >= len(r.cells) {
		return Cell{S: emptyStr}
	}
	return r.cells[i]
}

// StrCell returns the string of cell i ("" when absent).
func (r *Row) StrCell(i int) string { return r.Cell(i).S.Value() }

// Pod cell layout: 0 ready, 1 containers, 2 status, 3 restarts, 4 vpa, 5 node.
func (r *Row) Ready() uint16   { return uint16(r.Cell(0).N) }
func (r *Row) Total() uint16   { return uint16(r.Cell(1).N) }
func (r *Row) Status() Str     { return r.Cell(2).S }
func (r *Row) Restarts() int32 { return int32(r.Cell(3).N) }
func (r *Row) VPA() Str        { return r.Cell(4).S }
func (r *Row) Node() Str       { return r.Cell(5).S }

// IPs returns the row's IP tokens (nil when it holds none). The slice is immutable.
func (r *Row) IPs() []ipx.Tok {
	if r.ips == nil {
		return nil
	}
	return r.ips.T
}

// SetIPs sets the row's tokens; projection only, before the row is published.
func (r *Row) SetIPs(s *ipx.Set) { r.ips = s }

// IPText joins the tokens of field f with ",".
func (r *Row) IPText(f ipx.Field) string {
	var one string
	n := 0
	var sb strings.Builder
	for _, t := range r.IPs() {
		if t.Field != f {
			continue
		}
		if n == 0 {
			one = t.String()
		} else {
			if n == 1 {
				sb.WriteString(one)
			}
			sb.WriteByte(',')
			sb.WriteString(t.String())
		}
		n++
	}
	if n <= 1 {
		return one
	}
	return sb.String()
}

// FirstIP returns the lowest address of field f (for sorting).
func (r *Row) FirstIP(f ipx.Field) (ipx.U128, bool) {
	for i := range r.IPs() {
		if t := &r.ips.T[i]; t.Field == f {
			return t.Lo, true
		}
	}
	return ipx.U128{}, false
}

// CellsEqual reports whether every displayed cell (and owner) is identical.
func (r *Row) CellsEqual(o *Row) bool {
	if r.name != o.name || r.ns != o.ns || r.tone != o.tone || r.created != o.created ||
		r.ownerKind != o.ownerKind || r.ownerName != o.ownerName ||
		r.flags&(flagDeleting|flagTerminal) != o.flags&(flagDeleting|flagTerminal) || r.res != o.res || len(r.cells) != len(o.cells) || !factsEqual(r.facts, o.facts) {
		return false
	}
	for i := range r.cells {
		if r.cells[i] != o.cells[i] {
			return false
		}
	}
	a, b := r.IPs(), o.IPs()
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// SeverityRank orders tones for the default sort: danger, warning, then everything else.
func (r *Row) SeverityRank() uint8 { return ToneRank(r.tone) }

func ToneRank(t uint8) uint8 {
	switch t {
	case ToneDanger:
		return 0
	case ToneWarning:
		return 1
	}
	return 2
}

// ---- runtime.Object / metav1.Object (minimal) ------------------------------------------

func (r *Row) GetObjectKind() schema.ObjectKind { return schema.EmptyObjectKind }
func (r *Row) DeepCopyObject() runtime.Object {
	c := *r
	return &c
}
func (r *Row) GetNamespace() string {
	if r.ns == (Str{}) {
		return ""
	}
	return r.ns.Value()
}
func (r *Row) GetName() string             { return r.name }
func (r *Row) GetUID() types.UID           { return types.UID(r.uid) }
func (r *Row) GetResourceVersion() string  { return r.rv }
func (r *Row) SetResourceVersion(v string) { r.rv = v }

var initialEventsEndAnnotations = map[string]string{metav1.InitialEventsAnnotationKey: "true"}

func (r *Row) GetAnnotations() map[string]string {
	if r.flags&flagInitialEventsEnd != 0 {
		return initialEventsEndAnnotations
	}
	return nil
}

// RowList is what List returns: a list meta plus projected rows.
type RowList struct {
	metav1.TypeMeta
	metav1.ListMeta
	Items []*Row
}

// PodRowList is the pod list (kept for existing callers).
type PodRowList = RowList

func (l *RowList) GetObjectKind() schema.ObjectKind { return schema.EmptyObjectKind }
func (l *RowList) DeepCopyObject() runtime.Object {
	c := *l
	c.Items = append([]*Row(nil), l.Items...)
	return &c
}

// noMeta is a zero-size stub for the metav1.Object methods the reflector never calls.
type noMeta struct{}

func (noMeta) SetNamespace(string)                           {}
func (noMeta) SetName(string)                                {}
func (noMeta) GetGenerateName() string                       { return "" }
func (noMeta) SetGenerateName(string)                        {}
func (noMeta) SetUID(types.UID)                              {}
func (noMeta) GetGeneration() int64                          { return 0 }
func (noMeta) SetGeneration(int64)                           {}
func (noMeta) GetSelfLink() string                           { return "" }
func (noMeta) SetSelfLink(string)                            {}
func (noMeta) GetCreationTimestamp() metav1.Time             { return metav1.Time{} }
func (noMeta) SetCreationTimestamp(metav1.Time)              {}
func (noMeta) GetDeletionTimestamp() *metav1.Time            { return nil }
func (noMeta) SetDeletionTimestamp(*metav1.Time)             {}
func (noMeta) GetDeletionGracePeriodSeconds() *int64         { return nil }
func (noMeta) SetDeletionGracePeriodSeconds(*int64)          {}
func (noMeta) GetLabels() map[string]string                  { return nil }
func (noMeta) SetLabels(map[string]string)                   {}
func (noMeta) SetAnnotations(map[string]string)              {}
func (noMeta) GetFinalizers() []string                       { return nil }
func (noMeta) SetFinalizers([]string)                        {}
func (noMeta) GetOwnerReferences() []metav1.OwnerReference   { return nil }
func (noMeta) SetOwnerReferences([]metav1.OwnerReference)    {}
func (noMeta) GetManagedFields() []metav1.ManagedFieldsEntry { return nil }
func (noMeta) SetManagedFields([]metav1.ManagedFieldsEntry)  {}

var _ metav1.Object = (*Row)(nil)
var _ runtime.Object = (*Row)(nil)

// Intern interns a string the way rows do.
func Intern(s string) Str { return intern(s) }

// ---- construction helpers for modules outside this package (mode B kinds, the Helm release view) ----

// NewRow creates a row with room for n cells; fill them with PutS / PutN in the module's cell column order.
func NewRow(uid, name, rv, ns string, createdMs int64, deleting bool, n int) *Row {
	return newRowRaw(uid, name, rv, ns, createdMs, deleting, n)
}

// PutS appends a string (dict or str column) cell.
func (r *Row) PutS(s string) { r.putS(s) }

// PutN appends an integer or time (unix ms) cell.
func (r *Row) PutN(n int64) { r.putN(n) }

// SetTone sets the row's tone (Tone* constants).
func (r *Row) SetTone(t uint8) { r.tone = t }

// SetOwner records the controlling owner shown by owner columns.
func (r *Row) SetOwner(kind, name string) { r.ownerKind, r.ownerName = intern(kind), intern(name) }

// ColDict, ColStr, ColInt, ColTime and ColTone construct columns for modules outside this package.
func ColIP(key, label string, w int, f ipx.Field) Col { return cIP(key, label, w, f) }
func ColCluster() Col                                 { return cCluster() }
func ColName() Col                                    { return cName() }
func ColNS() Col                                      { return cNS() }
func ColTone() Col                                    { return cTone() }
func ColAge() Col                                     { return cAge() }
func ColDict(key, label string, w int) Col            { return cDict(key, label, w) }
func ColStr(key, label string, w int) Col             { return cStr(key, label, w) }
func ColInt(key, label string, w int) Col             { return cInt(key, label, w) }
func ColTimeCell(key, label string, w int) Col        { return cTime(key, label, w) }
