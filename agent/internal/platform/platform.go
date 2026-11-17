// Package platform holds the baseline modules and problem detectors for cluster-platform kinds: APIServices,
// admission webhook configurations, cert-manager Certificates and Gateway API routes. Each kind is read as
// typed partial JSON (ingest mode B), projected to a small row plus compact Facts for the detectors.
package platform

import (
	"encoding/json"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Obj is a decoded object: its metadata for the reflector and the row projected from it.
type Obj struct {
	metav1.TypeMeta
	metav1.ObjectMeta
	row *ingest.Row
}

func (o *Obj) GetObjectKind() schema.ObjectKind { return &o.TypeMeta }
func (o *Obj) DeepCopyObject() runtime.Object {
	c := *o
	o.ObjectMeta.DeepCopyInto(&c.ObjectMeta)
	return &c
}

// meta is the part of every object the projection needs.
type meta struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name              string       `json:"name"`
		Namespace         string       `json:"namespace"`
		UID               types.UID    `json:"uid"`
		ResourceVersion   string       `json:"resourceVersion"`
		CreationTimestamp metav1.Time  `json:"creationTimestamp"`
		DeletionTimestamp *metav1.Time `json:"deletionTimestamp"`
	} `json:"metadata"`
}

func (m *meta) newRow(cells int) *ingest.Row {
	var created int64
	if !m.Metadata.CreationTimestamp.IsZero() {
		created = m.Metadata.CreationTimestamp.UnixMilli()
	}
	return ingest.NewRow(string(m.Metadata.UID), m.Metadata.Name, m.Metadata.ResourceVersion, m.Metadata.Namespace, created, m.Metadata.DeletionTimestamp != nil, cells)
}

func (m *meta) obj(row *ingest.Row) *Obj {
	return &Obj{ObjectMeta: metav1.ObjectMeta{Name: m.Metadata.Name, Namespace: m.Metadata.Namespace, UID: m.Metadata.UID, ResourceVersion: m.Metadata.ResourceVersion,
		CreationTimestamp: m.Metadata.CreationTimestamp}, row: row}
}

// condition is a status condition as the API serves it.
type condition struct {
	Type               string      `json:"type"`
	Status             string      `json:"status"`
	Reason             string      `json:"reason"`
	Message            string      `json:"message"`
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`
}

func (c *condition) fact() ingest.CondFact {
	f := ingest.CondFact{Status: c.Status, Reason: c.Reason, Message: clip(c.Message)}
	if !c.LastTransitionTime.IsZero() {
		f.Since = c.LastTransitionTime.UnixMilli()
	}
	return f
}

func findCond(cs []condition, typ string) ingest.CondFact {
	for i := range cs {
		if cs[i].Type == typ {
			return cs[i].fact()
		}
	}
	return ingest.CondFact{}
}

func clip(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// kind describes one module: its identity, columns and decoder.
type kind struct {
	group, resource, kind string
	versions              []string // preferred first; the module is registered at the first, others through the resolver
	namespaced            bool
	short                 []string
	cols                  []ingest.Col
	decode                func(raw []byte) (runtime.Object, error)
}

func (k kind) module(version string) *ingest.Module {
	cols := []ingest.Col{ingest.ColCluster(), ingest.ColName()}
	if k.namespaced {
		cols = append(cols, ingest.ColNS())
	}
	cols = append(cols, k.cols...)
	m := &ingest.Module{GVR: k.group + "/" + version + "/" + k.resource, Kind: k.kind, Namespaced: k.namespaced, Short: k.short,
		Curated: true, Optional: true, Tier0: true, NoWatchList: true,
		Cols: cols, NewAPI: ingest.NewPartialAPI(k.decode), DecodeJSON: k.decode}
	m.ProjectObj = func(o runtime.Object) *ingest.Row {
		if x, ok := o.(*Obj); ok {
			return x.row
		}
		return nil
	}
	m.Each = ingest.PartialEach(m)
	return m
}

var kinds []kind

// register adds a kind: registered at its preferred version, any other served version through a resolver.
func register(k kind) {
	kinds = append(kinds, k)
	ingest.Register(k.module(k.versions[0]))
	ingest.AddResolver(func(group, version, resource, kd string, namespaced bool) *ingest.Module {
		if group != k.group || kd != k.kind || resource != k.resource {
			return nil
		}
		for _, v := range k.versions {
			if v == version {
				return k.module(version)
			}
		}
		return nil
	})
}

// decodeInto is a helper for decoders: it unmarshals raw into w.
func decodeInto(raw []byte, w any) error { return json.Unmarshal(raw, w) }

func firstNZ(vs ...int64) int64 {
	for _, v := range vs {
		if v != 0 {
			return v
		}
	}
	return 0
}
