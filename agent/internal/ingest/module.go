package ingest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
)

// ColSrc says where a column's value comes from in a Row.
type ColSrc uint8

const (
	SrcCluster ColSrc = iota // the cluster name (supplied by the view engine)
	SrcName
	SrcNS
	SrcCreated
	SrcTone
	SrcCell  // Row.cells[Idx]
	SrcOwner // the controlling owner's name (dict)
)

// Col is one column of a kind's view: the wire schema plus how to read, sort and filter it.
type Col struct {
	protocol.Column
	Src        ColSrc
	Idx        int // cell index for SrcCell
	Sortable   bool
	Filterable bool
}

// Module is everything the engine needs to serve one resource type: identity, columns, projection,
// default sort and the list/watch tweaks. Curated modules (mode A) decode protobuf into typed
// objects and project before the cache; generic modules (mode C) are built from a server-side Table.
type Module struct {
	GVR                            string // "v1/pods", "apps/v1/deployments"
	Group, Version, Resource, Kind string
	Namespaced                     bool
	Short                          []string
	Curated                        bool
	Tier0                          bool
	Cols                           []Col
	NCells                         int
	DefaultSort                    []protocol.Sort // nil: statusTone severity, then name
	ToneCol                        bool            // has a statusTone column
	Table                          bool            // mode C
	// Optional marks a CRD-backed curated kind (Flux): known and curated, but only served where discovery says so.
	Optional bool
	// Virtual marks a kind with no API resource of its own (the Helm release view): served by every cluster.
	Virtual bool
	// Synthetic marks a kind whose rows the agent computes itself (v8s/problems): it needs no watch, every cluster serves it.
	Synthetic bool
	// GateClass names a problem class: the kind is watched as part of the baseline only while that class is enabled
	// (hpa.unable, pv.failed); otherwise it is watched on demand like any other kind.
	GateClass string
	// NoWatchList forces a paginated LIST + watch (no streaming list) for kinds whose list is not a plain API list.
	NoWatchList bool
	// NewAPI builds the list/watch source on a real REST client (mode B and virtual kinds). Nil: typed or Table.
	NewAPI func(c rest.Interface, group, version, resource string) API
	// DecodeJSON decodes one object of a mode B kind from its JSON (list items and watch events).
	DecodeJSON func(raw []byte) (runtime.Object, error)
	Tweak      func(*metav1.ListOptions)
	NewList    func() runtime.Object
	Each       func(list runtime.Object, fn func(*Row))
	ProjectObj func(obj runtime.Object) *Row
	colIdx     map[string]int
	tableNames map[string]int // generic: table column name → cell index
	tableTypes []string       // generic: per cell, columnDefinition type
	toneCell   int            // generic: cell index the tone heuristic reads (-1 none)
	srcMap     []int
	tableCols  []TableCol
	headerIdx  map[string]int // curated Table module: printer column ("Name", "Name#2") → cell index
	labelCells []labelCell    // curated Table module: cells read from metadata.labels
	toneFn     func(r *Row) uint8
}

// Columns returns the wire schema.
func (m *Module) Columns() []protocol.Column {
	out := make([]protocol.Column, len(m.Cols))
	for i := range m.Cols {
		out[i] = m.Cols[i].Column
	}
	return out
}

// Col returns the column with key, or nil.
func (m *Module) Col(key string) *Col {
	if i, ok := m.colIdx[key]; ok {
		return &m.Cols[i]
	}
	return nil
}

func (m *Module) finish() *Module {
	m.colIdx = make(map[string]int, len(m.Cols))
	n := 0
	for i := range m.Cols {
		c := &m.Cols[i]
		if c.Src == SrcCell {
			c.Idx = n
			n++
		}
		if c.Src == SrcTone {
			m.ToneCol = true
		}
		m.colIdx[c.Key] = i
	}
	m.NCells = n
	parts := strings.Split(m.GVR, "/")
	switch len(parts) {
	case 2:
		m.Version, m.Resource = parts[0], parts[1]
	case 3:
		m.Group, m.Version, m.Resource = parts[0], parts[1], parts[2]
	}
	return m
}

// ---- column constructors -------------------------------------------------------------

func cCluster() Col {
	return Col{Column: protocol.Column{Key: "cluster", Type: protocol.ColDict, Label: "Cluster", Width: 110}, Src: SrcCluster, Sortable: true, Filterable: true}
}
func cName() Col {
	return Col{Column: protocol.Column{Key: "name", Type: protocol.ColStr, Label: "Name", Width: 320}, Src: SrcName, Sortable: true, Filterable: true}
}
func cNS() Col {
	return Col{Column: protocol.Column{Key: "namespace", Type: protocol.ColDict, Label: "Namespace", Width: 160}, Src: SrcNS, Sortable: true, Filterable: true}
}
func cTone() Col {
	return Col{Column: protocol.Column{Key: "statusTone", Type: protocol.ColTone, Label: "Tone", Width: 40}, Src: SrcTone, Sortable: true, Filterable: true}
}
func cAge() Col {
	return Col{Column: protocol.Column{Key: "createdAt", Type: protocol.ColTime, Label: "Age", Width: 72, Align: "right"}, Src: SrcCreated, Sortable: true}
}

// cDict is a low-cardinality string cell (sent as dictionary ids).
func cDict(key, label string, w int) Col {
	return Col{Column: protocol.Column{Key: key, Type: protocol.ColDict, Label: label, Width: w}, Src: SrcCell, Sortable: true, Filterable: true}
}

// cStr is an inline string cell (high cardinality or long).
func cStr(key, label string, w int) Col {
	return Col{Column: protocol.Column{Key: key, Type: protocol.ColStr, Label: label, Width: w}, Src: SrcCell, Sortable: true, Filterable: true}
}

func cInt(key, label string, w int) Col {
	return Col{Column: protocol.Column{Key: key, Type: protocol.ColInt, Label: label, Width: w, Align: "right"}, Src: SrcCell, Sortable: true, Filterable: true}
}

func cTime(key, label string, w int) Col {
	return Col{Column: protocol.Column{Key: key, Type: protocol.ColTime, Label: label, Width: w, Align: "right"}, Src: SrcCell, Sortable: true}
}

// ---- registry ---------------------------------------------------------------------------

var reg = struct {
	sync.RWMutex
	mods    map[string]*Module
	waiters map[string][]chan struct{}
}{mods: map[string]*Module{}, waiters: map[string][]chan struct{}{}}

// Register adds a module (curated modules at init; generic ones when their Table schema is known).
func Register(m *Module) {
	m.finish()
	reg.Lock()
	reg.mods[m.GVR] = m
	ws := reg.waiters[m.GVR]
	delete(reg.waiters, m.GVR)
	reg.Unlock()
	for _, w := range ws {
		close(w)
	}
}

// Lookup returns the module for a GVR, or nil.
func Lookup(gvr string) *Module {
	reg.RLock()
	defer reg.RUnlock()
	return reg.mods[gvr]
}

// WaitModule blocks until a module for gvr is registered (a generic kind's first Table arrived) or ctx ends.
func WaitModule(ctx context.Context, gvr string) (*Module, error) {
	reg.Lock()
	if m := reg.mods[gvr]; m != nil {
		reg.Unlock()
		return m, nil
	}
	ch := make(chan struct{})
	reg.waiters[gvr] = append(reg.waiters[gvr], ch)
	reg.Unlock()
	select {
	case <-ch:
		return Lookup(gvr), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Resolver builds (and registers) the module of a CRD-backed kind from its discovered group, version and
// kind, or returns nil when it is not one it knows. It lets a kind be read at whatever version the cluster
// serves (docs/integrations.md: version negotiation).
type Resolver func(group, version, resource, kind string, namespaced bool) *Module

var resolvers []Resolver

// AddResolver registers a Resolver (at init).
func AddResolver(r Resolver) { reg.Lock(); resolvers = append(resolvers, r); reg.Unlock() }

// ResolveDiscovered returns the module for a discovered resource, registering it when a resolver knows it.
func ResolveDiscovered(group, version, resource, kind string, namespaced bool) *Module {
	gvr := group + "/" + version + "/" + resource
	if m := Lookup(gvr); m != nil {
		return m
	}
	reg.RLock()
	rs := append([]Resolver(nil), resolvers...)
	reg.RUnlock()
	for _, r := range rs {
		if m := r(group, version, resource, kind, namespaced); m != nil {
			if ex := Lookup(m.GVR); ex != nil {
				return ex
			}
			Register(m)
			return m
		}
	}
	return nil
}

// CuratedModules returns the hand-written modules sorted by GVR.
func CuratedModules() []*Module {
	reg.RLock()
	defer reg.RUnlock()
	var out []*Module
	for _, m := range reg.mods {
		if m.Curated {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GVR < out[j].GVR })
	return out
}

// VirtualModules returns the modules that exist on every cluster without an API resource of their own.
func VirtualModules() []*Module {
	reg.RLock()
	defer reg.RUnlock()
	var out []*Module
	for _, m := range reg.mods {
		if m.Virtual {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GVR < out[j].GVR })
	return out
}

// Tier0Modules returns the modules watched for every selected cluster, pods first.
func Tier0Modules() []*Module {
	var out []*Module
	for _, m := range CuratedModules() {
		if m.Tier0 && !m.Optional {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GVR == PodsGVR && out[j].GVR != PodsGVR })
	return out
}

const PodsGVR = "v1/pods"

// ParseGVR splits "v1/pods" or "apps/v1/deployments".
func ParseGVR(s string) (group, version, resource string, err error) {
	parts := strings.Split(s, "/")
	switch len(parts) {
	case 2:
		return "", parts[0], parts[1], nil
	case 3:
		return parts[0], parts[1], parts[2], nil
	}
	return "", "", "", fmt.Errorf("bad gvr %q", s)
}

// objAccessor reads resourceVersion and annotations of any typed object (bookmarks).
func objAccessor(o runtime.Object) (rv string, annotations map[string]string) {
	if a, err := meta.Accessor(o); err == nil {
		return a.GetResourceVersion(), a.GetAnnotations()
	}
	return "", nil
}

var errNoSchema = errors.New("kind has no schema yet")

func listMeta(o runtime.Object) (metav1.ListInterface, error) { return meta.ListAccessor(o) }
