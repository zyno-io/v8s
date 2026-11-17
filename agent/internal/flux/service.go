package flux

import (
	"context"
	"sort"
	"strings"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const maxInventory = 5000

// Config wires the service to the agent (callbacks keep this package import-light).
type Config struct {
	// Get reads one object on the read context.
	Get func(ctx context.Context, cluster, gvr, ns, name string) (*unstructured.Unstructured, error)
	// Scan visits the rows of a watched kind; it returns false when the kind is not watched.
	Scan func(cluster, gvr string, fn func(*ingest.Row) bool) bool
	// Clusters lists the selected clusters.
	Clusters func() []string
	// Resolve maps apiVersion+kind to a gvr ("group/version/resource") and scope.
	Resolve func(cluster, apiVersion, kind string) (gvr string, namespaced bool, err error)
}

// Service answers flux.inventory and flux.graph.
type Service struct{ cfg Config }

// New builds a Service.
func New(cfg Config) *Service { return &Service{cfg: cfg} }

// parseInventoryID splits "<namespace>_<name>_<group>_<kind>". Group and kind never contain "_", so
// they are taken from the right and the name keeps any underscores; odd ids fall back to best effort.
func parseInventoryID(id string) (ns, name, group, kind string) {
	parts := strings.Split(id, "_")
	n := len(parts)
	if n >= 4 {
		return parts[0], strings.Join(parts[1:n-2], "_"), parts[n-2], parts[n-1]
	}
	switch n {
	case 3:
		return parts[0], parts[1], "", parts[2]
	case 2:
		return "", parts[0], "", parts[1]
	}
	return "", id, "", ""
}

func statusOf(m *ingest.Module, r *ingest.Row) string {
	if m == nil {
		return ""
	}
	for _, key := range []string{"status", "ready"} {
		c := m.Col(key)
		if c == nil || c.Src != ingest.SrcCell {
			continue
		}
		if c.Type == protocol.ColStr || c.Type == protocol.ColDict {
			return r.StrCell(c.Idx)
		}
	}
	return ""
}

type live struct {
	tone   uint8
	status string
}

// Inventory reads status.inventory of a Kustomization and joins each entry to its live object.
func (s *Service) Inventory(ctx context.Context, p protocol.FluxInventoryParams) (protocol.FluxInventoryResult, error) {
	res := protocol.FluxInventoryResult{Entries: []protocol.FluxInventoryEntry{}}
	obj, err := s.cfg.Get(ctx, p.Ref.Cluster, p.Ref.GVR, p.Ref.Namespace, p.Ref.Name)
	if err != nil {
		return res, err
	}
	res.Revision, _, _ = unstructured.NestedString(obj.Object, "status", "lastAppliedRevision")
	raw, _, _ := unstructured.NestedSlice(obj.Object, "status", "inventory", "entries")

	type item struct {
		e   protocol.FluxInventoryEntry
		key string
	}
	items := make([]item, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		v, _ := m["v"].(string)
		if id == "" {
			continue
		}
		ns, name, group, kind := parseInventoryID(id)
		items = append(items, item{e: protocol.FluxInventoryEntry{ID: id, Group: group, Version: v, Kind: kind, Namespace: ns, Name: name}})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].e.ID < items[j].e.ID })
	if len(items) > maxInventory {
		items = items[:maxInventory]
	}

	// resolve gvrs and collect the wanted keys per gvr: one scan each
	wanted := map[string]map[string]struct{}{}
	for i := range items {
		e := &items[i].e
		if e.Kind == "" || e.Version == "" {
			continue
		}
		apiVersion := e.Version
		if e.Group != "" {
			apiVersion = e.Group + "/" + e.Version
		}
		gvr, _, err := s.cfg.Resolve(p.Ref.Cluster, apiVersion, e.Kind)
		if err != nil || gvr == "" {
			continue
		}
		e.GVR = gvr
		items[i].key = e.Namespace + "/" + e.Name
		if wanted[gvr] == nil {
			wanted[gvr] = map[string]struct{}{}
		}
		wanted[gvr][items[i].key] = struct{}{}
	}
	found := map[string]map[string]live{}
	for gvr, want := range wanted {
		mod := ingest.Lookup(gvr)
		got := map[string]live{}
		if s.cfg.Scan(p.Ref.Cluster, gvr, func(r *ingest.Row) bool {
			k := r.GetNamespace() + "/" + r.Name()
			if _, ok := want[k]; ok {
				got[k] = live{tone: r.Tone(), status: statusOf(mod, r)}
			}
			return true
		}) {
			found[gvr] = got
		}
	}
	for i := range items {
		e := items[i].e
		if l, ok := found[e.GVR][items[i].key]; ok && e.GVR != "" {
			e.Live, e.Tone, e.Status = true, int(l.tone), l.status
		}
		res.Entries = append(res.Entries, e)
	}
	return res, nil
}

// alternate versions tried after the preferred one when resolving a kind.
var altVersions = map[string][]string{
	GroupSource:    {"v1beta2", "v1beta1"},
	GroupKustomize: {"v1beta2", "v1beta1"},
	GroupHelm:      {"v2beta2", "v2beta1"},
}

// Graph builds the dependency graph of the Flux objects of a cluster: dependsOn (From waits for To) and
// sourceRef (From reads To) edges between watched objects.
func (s *Service) Graph(ctx context.Context, p protocol.FluxGraphParams) (protocol.FluxGraphResult, error) {
	res := protocol.FluxGraphResult{Nodes: []protocol.FluxGraphNode{}, Edges: []protocol.FluxGraphEdge{}}
	type pend struct{ from, to, typ string }
	var pending []pend
	have := map[string]bool{}
	for _, k := range Kinds() {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		versions := append([]string{k.Version}, altVersions[k.Group]...)
		var gvr string
		for _, v := range versions {
			g, _, err := s.cfg.Resolve(p.Cluster, k.Group+"/"+v, k.Kind)
			if err == nil && g != "" {
				gvr = g
				break
			}
		}
		if gvr == "" {
			continue
		}
		s.cfg.Scan(p.Cluster, gvr, func(r *ingest.Row) bool {
			ns := r.GetNamespace()
			id := k.Kind + "/" + ns + "/" + r.Name()
			have[id] = true
			res.Nodes = append(res.Nodes, protocol.FluxGraphNode{
				ID: id, Kind: k.Kind, GVR: gvr, Namespace: ns, Name: r.Name(),
				Tone: int(r.Tone()), Suspended: r.StrCell(CellSuspended) == "true",
			})
			if src := r.StrCell(CellSource); src != "" {
				parts := strings.Split(src, "/")
				switch len(parts) {
				case 2:
					pending = append(pending, pend{id, parts[0] + "/" + ns + "/" + parts[1], "sourceRef"})
				case 3:
					pending = append(pending, pend{id, src, "sourceRef"})
				}
			}
			if dep := r.StrCell(CellDependsOn); dep != "" {
				for _, d := range strings.Split(dep, ",") {
					if d != "" {
						pending = append(pending, pend{id, k.Kind + "/" + d, "dependsOn"})
					}
				}
			}
			return true
		})
	}
	sort.Slice(res.Nodes, func(i, j int) bool { return res.Nodes[i].ID < res.Nodes[j].ID })
	for _, e := range pending {
		if have[e.from] && have[e.to] {
			res.Edges = append(res.Edges, protocol.FluxGraphEdge{From: e.from, To: e.to, Type: e.typ})
		}
	}
	sort.Slice(res.Edges, func(i, j int) bool {
		a, b := res.Edges[i], res.Edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.Type < b.Type
	})
	return res, nil
}
