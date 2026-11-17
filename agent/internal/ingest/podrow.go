// Package ingest decodes pods from the API server (or the fake source), projects each one
// into a compact *PodRow before the reflector sees it, and discards everything else.
package ingest

import (
	"unique"

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
)

// Str is an interned string.
type Str = unique.Handle[string]

// PodRow is the projection of a pod the agent holds. It is immutable once published to a store
// (the store owns Ver). It implements runtime.Object and metav1.Object with only name, namespace,
// uid and resourceVersion so a client-go Reflector can track it.
type PodRow struct {
	noMeta
	uid, name, rv string
	ns, node      Str
	status        Str // kubectl-style status reason
	ownerKind     Str
	ownerName     Str
	created       int64 // unix ms
	ver           uint32
	restarts      int32
	ready, total  uint16
	tone          uint8
	flags         uint8
}

func (r *PodRow) UID() string             { return r.uid }
func (r *PodRow) Name() string            { return r.name }
func (r *PodRow) NS() Str                 { return r.ns }
func (r *PodRow) Node() Str               { return r.node }
func (r *PodRow) Status() Str             { return r.status }
func (r *PodRow) OwnerKind() Str          { return r.ownerKind }
func (r *PodRow) OwnerName() Str          { return r.ownerName }
func (r *PodRow) CreatedMs() int64        { return r.created }
func (r *PodRow) Ver() uint32             { return r.ver }
func (r *PodRow) SetVer(v uint32)         { r.ver = v }
func (r *PodRow) Restarts() int32         { return r.restarts }
func (r *PodRow) Ready() uint16           { return r.ready }
func (r *PodRow) Total() uint16           { return r.total }
func (r *PodRow) Tone() uint8             { return r.tone }
func (r *PodRow) Deleting() bool          { return r.flags&flagDeleting != 0 }
func (r *PodRow) ResourceVersion() string { return r.rv }

// CellsEqual reports whether every displayed cell (and owner) is identical.
func (r *PodRow) CellsEqual(o *PodRow) bool {
	return r.name == o.name && r.ns == o.ns && r.node == o.node && r.status == o.status &&
		r.tone == o.tone && r.ready == o.ready && r.total == o.total && r.restarts == o.restarts &&
		r.created == o.created && r.ownerKind == o.ownerKind && r.ownerName == o.ownerName &&
		r.flags&flagDeleting == o.flags&flagDeleting
}

// SeverityRank orders tones for the default sort: danger, warning, then everything else.
func (r *PodRow) SeverityRank() uint8 { return ToneRank(r.tone) }

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

func (r *PodRow) GetObjectKind() schema.ObjectKind { return schema.EmptyObjectKind }
func (r *PodRow) DeepCopyObject() runtime.Object {
	c := *r
	return &c
}
func (r *PodRow) GetNamespace() string        { return r.ns.Value() }
func (r *PodRow) GetName() string             { return r.name }
func (r *PodRow) GetUID() types.UID           { return types.UID(r.uid) }
func (r *PodRow) GetResourceVersion() string  { return r.rv }
func (r *PodRow) SetResourceVersion(v string) { r.rv = v }

var initialEventsEndAnnotations = map[string]string{metav1.InitialEventsAnnotationKey: "true"}

func (r *PodRow) GetAnnotations() map[string]string {
	if r.flags&flagInitialEventsEnd != 0 {
		return initialEventsEndAnnotations
	}
	return nil
}

// PodRowList is what List returns: a list meta plus projected rows.
type PodRowList struct {
	metav1.TypeMeta
	metav1.ListMeta
	Items []*PodRow
}

func (l *PodRowList) GetObjectKind() schema.ObjectKind { return schema.EmptyObjectKind }
func (l *PodRowList) DeepCopyObject() runtime.Object {
	c := *l
	c.Items = append([]*PodRow(nil), l.Items...)
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

var _ metav1.Object = (*PodRow)(nil)
var _ runtime.Object = (*PodRow)(nil)

// Intern interns a string the way rows do.
func Intern(s string) Str { return intern(s) }
