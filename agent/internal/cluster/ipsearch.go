package cluster

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/ipx"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	ipPerKindDefault = 5
	ipPerKindMax     = 50
)

// ipWait bounds how long a strong IP search waits for the on-demand sources it started to list; a source
// that is not ready by then is reported as pending and the search is asked again.
var ipWait = 800 * time.Millisecond

// touchIPSources acquires and at once releases every on-demand IP kind discovery lists on the cluster: a new
// watch starts, an existing one restarts its linger. It returns the touched gvrs and the kinds that could not
// start (budget exhausted, or another error). Discovery lists one version per resource; kinds are deduplicated
// by group/resource anyway.
func (r *runner) touchIPSources() ([]string, []protocol.IPSource) {
	d := r.disc.Load()
	if d == nil {
		return nil, nil
	}
	var touched []string
	var failed []protocol.IPSource
	seen := map[string]bool{}
	for _, res := range d.list {
		if !res.Listable() || seen[res.Group+"/"+res.Resource] {
			continue
		}
		m := ingest.ResolveDiscovered(res.Group, res.Version, res.Resource, res.Kind, res.Namespaced)
		if m == nil || !m.IPOnDemand {
			continue
		}
		seen[res.Group+"/"+res.Resource] = true
		rel, err := r.acquire(m.GVR)
		if err != nil {
			reason := "error"
			if errors.Is(err, ErrBudget) {
				reason = "budget"
			}
			failed = append(failed, protocol.IPSource{Cluster: r.id, Kind: m.Kind, Reason: reason})
			continue
		}
		touched = append(touched, m.GVR)
		rel()
	}
	return touched, failed
}

// settled reports whether the watch of gvr has listed (or is gone, or failed for good).
func (r *runner) settled(gvr string) bool {
	r.mu.Lock()
	w := r.watches[gvr]
	r.mu.Unlock()
	return w == nil || w.st.Synced() || w.lastErr() != nil
}

// SearchAll answers a search request: the name search, plus an IP search when the text reads as an IP query.
func (m *Manager) SearchAll(ctx context.Context, p protocol.SearchParams) protocol.SearchResult {
	res := protocol.SearchResult{Results: m.Search(p.Text, p.Limit)}
	if q, ok := ipx.Parse(p.Text); ok {
		res.IP = m.SearchIP(ctx, q, p.IPPerKind)
	}
	return res
}

type ipCand struct {
	rank uint32
	hit  protocol.IPHit
}

type ipGroup struct {
	kind  string
	order int
	total int
	best  uint32
	hits  []ipCand
}

// after reports whether the candidate sorts after (rank, cluster, namespace, name) (rank, cluster, namespace, name) before c.
func (c *ipCand) after(rank uint32, cluster, ns, name string) bool {
	if c.rank != rank {
		return c.rank > rank
	}
	if c.hit.Cluster != cluster {
		return c.hit.Cluster > cluster
	}
	if c.hit.Namespace != ns {
		return c.hit.Namespace > ns
	}
	return c.hit.Name > name
}

func (g *ipGroup) insert(c ipCand, k int) {
	i := sort.Search(len(g.hits), func(i int) bool { return g.hits[i].after(c.rank, c.hit.Cluster, c.hit.Namespace, c.hit.Name) })
	if len(g.hits) < k {
		g.hits = append(g.hits, ipCand{})
	} else if i >= k {
		return
	}
	copy(g.hits[i+1:], g.hits[i:])
	g.hits[i] = c
}

// SearchIP finds the objects holding the queried address in every watched IP kind of the selected clusters.
// Each object yields one hit (its best-ranked token); hits are grouped by kind (group/resource, so versions and
// clusters merge), capped at perKind per group, and each group reports its true total. There is no index: the
// scan over compact tokens costs a few ns per token.
func (m *Manager) SearchIP(ctx context.Context, q *ipx.Query, perKind int) *protocol.IPSearch {
	if perKind <= 0 {
		perKind = ipPerKindDefault
	}
	perKind = min(perKind, ipPerKindMax)
	out := &protocol.IPSearch{Query: q.Text, Mode: protocol.IPQueryMode(q.Mode.String()), Groups: []protocol.IPGroup{}}
	groups := map[string]*ipGroup{}
	runners := m.selectedRunners()
	// Only a strong query (an address, CIDR, range or a prefix with two complete parts) starts on-demand
	// sources: typing "1." or "10.4" never touches CRD watches.
	touched := map[*runner][]string{}
	if q.Strong() {
		for _, r := range runners {
			t, failed := r.touchIPSources()
			touched[r] = t
			out.Skipped = append(out.Skipped, failed...)
		}
		for end := time.Now().Add(ipWait); time.Now().Before(end) && ctx.Err() == nil; time.Sleep(25 * time.Millisecond) {
			all := true
			for r, ts := range touched {
				for _, g := range ts {
					all = all && r.settled(g)
				}
			}
			if all {
				break
			}
		}
	}
	for _, r := range runners {
		// Before discovery has loaded, sources cannot be known: ask again shortly.
		switch ph, _ := r.phase.Load().(protocol.ClusterPhase); ph {
		case protocol.PhaseConnecting, protocol.PhaseSyncing, protocol.PhaseReady:
			if r.disc.Load() == nil {
				out.Pending = append(out.Pending, protocol.IPSource{Cluster: r.id})
			}
		}
		r.mu.Lock()
		ws := make([]*watchEntry, 0, len(r.watches))
		for _, w := range r.watches {
			ws = append(ws, w)
		}
		r.mu.Unlock()
		sort.Slice(ws, func(i, j int) bool { return ws[i].gvr < ws[j].gvr })
		present := make(map[string]bool, len(ws))
		for _, w := range ws {
			present[w.gvr] = true
		}
		for _, g := range touched[r] { // started a moment ago, evicted before the scan: the budget is full
			if !present[g] {
				if mod := ingest.Lookup(g); mod != nil {
					out.Skipped = append(out.Skipped, protocol.IPSource{Cluster: r.id, Kind: mod.Kind, Reason: "budget"})
				}
			}
		}
		for _, w := range ws {
			if ctx.Err() != nil {
				return out
			}
			mod := ingest.Lookup(w.gvr)
			if mod == nil || !mod.IPs {
				continue
			}
			if err := w.lastErr(); err != nil {
				switch {
				case apierrors.IsForbidden(err):
					out.Skipped = append(out.Skipped, protocol.IPSource{Cluster: r.id, Kind: mod.Kind, Reason: "forbidden"})
					continue
				case apierrors.IsNotFound(err):
					out.Skipped = append(out.Skipped, protocol.IPSource{Cluster: r.id, Kind: mod.Kind, Reason: "not found"})
					continue
				}
			}
			if !w.st.Synced() && w.lastErr() == nil { // still listing: its rows are partial (scanned anyway)
				out.Pending = append(out.Pending, protocol.IPSource{Cluster: r.id, Kind: mod.Kind})
			}
			key := mod.Group + "/" + mod.Resource
			g := groups[key]
			if g == nil {
				g = &ipGroup{kind: mod.Kind, order: mod.IPOrder, best: ^uint32(0)}
				groups[key] = g
			}
			detail := -1
			if mod.IPDetailCol != "" {
				if c := mod.Col(mod.IPDetailCol); c != nil && c.Src == ingest.SrcCell {
					detail = c.Idx
				}
			}
			cluster, gvr := r.id, w.gvr
			w.st.Scan(func(row *ingest.Row) bool {
				ts := row.IPs()
				if len(ts) == 0 {
					return true
				}
				mt, i := q.Best(ts)
				if i < 0 {
					return true
				}
				rank := ipx.RankOf(mt, &ts[i])
				g.total++
				g.best = min(g.best, rank)
				ns := row.GetNamespace()
				if n := len(g.hits); n >= perKind && !g.hits[n-1].after(rank, cluster, ns, row.Name()) {
					return true
				}
				h := protocol.IPHit{Cluster: cluster, GVR: gvr, Namespace: ns, Name: row.Name(), Field: ts[i].Field.Path(),
					Label: ts[i].Field.Label(), Value: ts[i].String(), Match: protocol.IPMatch(mt.String())}
				if detail >= 0 {
					h.Detail = row.StrCell(detail)
				}
				g.insert(ipCand{rank, h}, perKind)
				return true
			})
		}
	}
	ordered := make([]*ipGroup, 0, len(groups))
	for _, g := range groups {
		if g.total > 0 {
			ordered = append(ordered, g)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.best != b.best {
			return a.best < b.best
		}
		if a.order != b.order {
			return a.order < b.order
		}
		return a.kind < b.kind
	})
	for _, g := range ordered {
		hits := make([]protocol.IPHit, len(g.hits))
		for i := range g.hits {
			hits[i] = g.hits[i].hit
		}
		out.Groups = append(out.Groups, protocol.IPGroup{GVR: hits[0].GVR, Kind: g.kind, Total: g.total, Hits: hits})
	}
	return out
}
