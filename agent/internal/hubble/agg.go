package hubble

import (
	"sort"
	"strings"

	"github.com/zyno-io/v8s/agent/internal/hubble/pb"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// ---- drops view -------------------------------------------------------------------------

type dropKey struct{ srcNS, srcWL, dstNS, dstWL, reason string }

type dropVal struct {
	count  int
	last   int64
	labels []string // labels of the endpoint the policy applies to (destination for ingress, source for egress)
	egress bool
}

// maxGroups bounds the groups one aggregation holds (a flood of distinct pairs cannot grow it).
const maxGroups = 5000

// dropAgg groups dropped flows by (source workload, destination workload, reason).
type dropAgg struct{ m map[dropKey]*dropVal }

func newDropAgg() *dropAgg { return &dropAgg{m: map[dropKey]*dropVal{}} }

func (a *dropAgg) add(f *pb.Flow) {
	if f.Verdict != pb.Verdict_DROPPED {
		return
	}
	sns, swl := workloadIdentity(f.Source)
	dns, dwl := workloadIdentity(f.Destination)
	k := dropKey{sns, swl, dns, dwl, DropReasonName(f.DropReasonDesc)}
	v := a.m[k]
	if v == nil {
		if len(a.m) >= maxGroups {
			return
		}
		v = &dropVal{egress: f.TrafficDirection == pb.TrafficDirection_EGRESS}
		side := f.Destination
		if v.egress {
			side = f.Source
		}
		v.labels = append([]string(nil), side.GetLabels()...)
		a.m[k] = v
	}
	v.count++
	if t := f.GetTime(); t != nil {
		if ms := t.AsTime().UnixMilli(); ms > v.last {
			v.last = ms
		}
	}
}

// groups returns the aggregation as wire groups, most dropped first.
func (a *dropAgg) groups(cluster string, pol PolicyLister) []protocol.DropGroup {
	out := make([]protocol.DropGroup, 0, len(a.m))
	for k, v := range a.m {
		g := protocol.DropGroup{Cluster: cluster, Src: protocol.DropWorkload{Namespace: k.srcNS, Workload: k.srcWL},
			Dst: protocol.DropWorkload{Namespace: k.dstNS, Workload: k.dstWL}, Reason: k.reason, Count: v.count, LastSeen: v.last, Policies: []protocol.DropPolicy{}}
		if pol != nil {
			ns := k.dstNS
			dir := "Ingress"
			if v.egress {
				ns, dir = k.srcNS, "Egress"
			}
			g.Policies = MatchPolicies(pol, ns, dir, v.labels)
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Src.Workload+out[i].Dst.Workload+out[i].Reason < out[j].Src.Workload+out[j].Dst.Workload+out[j].Reason
	})
	return out
}

// ---- policies -----------------------------------------------------------------------------

// PolicySummary is the part of a (Cilium)NetworkPolicy the drops view needs.
type PolicySummary struct {
	Kind, Namespace, Name string
	Selector              map[string]string // podSelector / endpointSelector matchLabels (nil: matches every pod)
	Types                 []string          // Ingress / Egress (k8s policyTypes); empty for Cilium policies
}

// PolicyLister lists the policies that can apply in a namespace (namespaced policies plus cluster-wide ones).
type PolicyLister interface {
	Policies(ns string) []PolicySummary
}

// MatchPolicies returns the policies in ns that select a pod with the given labels (Cilium label strings such as
// "k8s:app=backend") and govern the direction. Policies that do not select it are not responsible.
func MatchPolicies(l PolicyLister, ns, direction string, labels []string) []protocol.DropPolicy {
	have := map[string]string{}
	for _, s := range labels {
		s = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(s, "k8s:"), "any:"), "k8s:")
		if k, v, ok := strings.Cut(s, "="); ok {
			have[k] = v
		}
	}
	out := []protocol.DropPolicy{}
	for _, p := range l.Policies(ns) {
		if len(p.Types) > 0 && !contains(p.Types, direction) {
			continue
		}
		ok := true
		for k, v := range p.Selector {
			if have[k] != v {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, protocol.DropPolicy{Kind: p.Kind, Namespace: p.Namespace, Name: p.Name})
		}
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ---- service map --------------------------------------------------------------------------

type edgeKey struct{ from, to string }

type edgeVal struct{ requests, errors, drops int }

// mapAgg aggregates flows into workload-to-workload edges.
type mapAgg struct {
	ns     string
	edges  map[edgeKey]*edgeVal
	nodes  map[string]protocol.ServiceMapNode
	frames int
}

func newMapAgg(ns string) *mapAgg {
	return &mapAgg{ns: ns, edges: map[edgeKey]*edgeVal{}, nodes: map[string]protocol.ServiceMapNode{}}
}

func isErrorFlow(f *pb.Flow) bool {
	if f.Verdict == pb.Verdict_ERROR {
		return true
	}
	if l7 := f.GetL7(); l7 != nil && l7.Type == pb.L7FlowType_RESPONSE {
		switch r := l7.GetRecord().(type) {
		case *pb.Layer7_Http:
			return r.Http.Code >= 500
		case *pb.Layer7_Dns:
			return r.Dns.Rcode == 2 || r.Dns.Rcode == 3
		}
	}
	return false
}

func (a *mapAgg) node(ns, wl string) string {
	id := identityID(ns, wl)
	if _, ok := a.nodes[id]; !ok {
		a.nodes[id] = protocol.ServiceMapNode{ID: id, Namespace: ns, Workload: wl, External: ns != a.ns}
	}
	return id
}

func (a *mapAgg) add(f *pb.Flow) {
	sns, swl := workloadIdentity(f.Source)
	dns, dwl := workloadIdentity(f.Destination)
	if sns != a.ns && dns != a.ns {
		return
	}
	k := edgeKey{a.node(sns, swl), a.node(dns, dwl)}
	e := a.edges[k]
	if e == nil {
		e = &edgeVal{}
		a.edges[k] = e
	}
	a.frames++
	switch {
	case f.Verdict == pb.Verdict_DROPPED:
		e.drops++
		return
	case isErrorFlow(f):
		e.errors++
	}
	if l7 := f.GetL7(); l7 != nil {
		if l7.Type == pb.L7FlowType_REQUEST {
			e.requests++
		}
		return
	}
	if !f.GetIsReply().GetValue() {
		e.requests++
	}
}

func (a *mapAgg) result(cluster string, window int, truncated bool) protocol.HubbleServiceMap {
	res := protocol.HubbleServiceMap{Cluster: cluster, Namespace: a.ns, WindowSec: window, Truncated: truncated,
		Nodes: make([]protocol.ServiceMapNode, 0, len(a.nodes)), Edges: make([]protocol.ServiceMapEdge, 0, len(a.edges))}
	for _, n := range a.nodes {
		res.Nodes = append(res.Nodes, n)
	}
	sort.Slice(res.Nodes, func(i, j int) bool { return res.Nodes[i].ID < res.Nodes[j].ID })
	for k, e := range a.edges {
		res.Edges = append(res.Edges, protocol.ServiceMapEdge{From: k.from, To: k.to, Requests: e.requests, Errors: e.errors, Drops: e.drops})
	}
	sort.Slice(res.Edges, func(i, j int) bool {
		if res.Edges[i].From != res.Edges[j].From {
			return res.Edges[i].From < res.Edges[j].From
		}
		return res.Edges[i].To < res.Edges[j].To
	})
	return res
}

// ---- minute counters for the problem classes -----------------------------------------------

// counters keeps, per key, event counts per minute for the last few minutes (and nothing else).
type counters struct {
	slots map[string]map[int64]int
	max   int
}

func newCounters() *counters { return &counters{slots: map[string]map[int64]int{}, max: maxGroups} }

func (c *counters) add(key string, nowMs int64, n int) {
	m := c.slots[key]
	if m == nil {
		if len(c.slots) >= c.max {
			return
		}
		m = map[int64]int{}
		c.slots[key] = m
	}
	m[nowMs/60000] += n
}

// sum returns the count within the last window minutes (the current minute included) and the start of the
// oldest minute that contributed (unix ms; 0 when none).
func (c *counters) sum(key string, nowMs int64, windowMin int) (n int, first int64) {
	cur := nowMs / 60000
	for min, v := range c.slots[key] {
		if min > cur-int64(windowMin) {
			n += v
			if first == 0 || min*60000 < first {
				first = min * 60000
			}
		}
	}
	return
}

// prune drops minutes older than keepMin and empty keys.
func (c *counters) prune(nowMs int64, keepMin int) {
	cur := nowMs / 60000
	for k, m := range c.slots {
		for min := range m {
			if min <= cur-int64(keepMin) {
				delete(m, min)
			}
		}
		if len(m) == 0 {
			delete(c.slots, k)
		}
	}
}

func (c *counters) keys() []string {
	out := make([]string, 0, len(c.slots))
	for k := range c.slots {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
