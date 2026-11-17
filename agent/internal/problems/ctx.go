package problems

import (
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
)

// GRs the engine always indexes (the joins of docs/problems.md use EndpointSlices, owners and events).
const (
	GRPods      = "pods"
	GRNodes     = "nodes"
	GRServices  = "services"
	GREvents    = "events"
	GRPVC       = "persistentvolumeclaims"
	GRPV        = "persistentvolumes"
	GRDeploys   = "apps/deployments"
	GRSets      = "apps/statefulsets"
	GRDaemons   = "apps/daemonsets"
	GRReplicas  = "apps/replicasets"
	GRJobs      = "batch/jobs"
	GRCronJobs  = "batch/cronjobs"
	GRSlices    = "discovery.k8s.io/endpointslices"
	GRIngresses = "networking.k8s.io/ingresses"
	GRHPA       = "autoscaling/horizontalpodautoscalers"
)

var builtinGRs = []string{GRPods, GRReplicas, GRDeploys, GRSets, GRDaemons, GRJobs, GRCronJobs, GRSlices, GRServices, GRIngresses, GREvents, GRNodes}

// Event row cells (module v1/events): type, reason, object, message, count, lastSeen.
const (
	evType = iota
	evReason
	evObject
	evMessage
	evCount
	evLast
)

// EventInfo is a Warning event about an object.
type EventInfo struct {
	Name, Reason, Message string
	Count                 int64
	Last                  int64
}

type indexes struct {
	podsByOwner map[string]map[string]struct{} // "Kind/ns/name" of the direct owner → pod names
	rsByDep     map[string]map[string]struct{} // "ns/deployment" → ReplicaSet names
	jobsByCron  map[string]map[string]struct{} // "ns/cronjob" → Job names
	svcSlices   map[string]map[string][2]int64 // "ns/service" → slice → (ready, total)
	ingBySvc    map[string]map[string]struct{} // "ns/service" → Ingress names
	evBy        map[string]map[string]struct{} // involved "ns/Kind/name" → "eventNS/eventName"
}

func (ix *indexes) init() {
	ix.podsByOwner = map[string]map[string]struct{}{}
	ix.rsByDep = map[string]map[string]struct{}{}
	ix.jobsByCron = map[string]map[string]struct{}{}
	ix.svcSlices = map[string]map[string][2]int64{}
	ix.ingBySvc = map[string]map[string]struct{}{}
	ix.evBy = map[string]map[string]struct{}{}
}

func setAdd(m map[string]map[string]struct{}, k, v string) {
	s := m[k]
	if s == nil {
		s = map[string]struct{}{}
		m[k] = s
	}
	s[v] = struct{}{}
}

func setDel(m map[string]map[string]struct{}, k, v string) {
	if s := m[k]; s != nil {
		delete(s, v)
		if len(s) == 0 {
			delete(m, k)
		}
	}
}

func nsName(ns, name string) string { return ns + "/" + name }

func ownerKey(r *ingest.Row) string {
	if r.OwnerKind().Value() == "" {
		return ""
	}
	return r.OwnerKind().Value() + "/" + r.GetNamespace() + "/" + r.OwnerName().Value()
}

// update maintains the indexes for one row change and records which workload aggregates to recompose.
func (ix *indexes) update(e *Engine, gr string, key Key, old, new *ingest.Row) {
	switch gr {
	case GRPods:
		if old != nil && ownerKey(old) != "" {
			setDel(ix.podsByOwner, ownerKey(old), key.Name)
		}
		if new != nil && ownerKey(new) != "" {
			setAdd(ix.podsByOwner, ownerKey(new), key.Name)
		}
		if (old == nil) != (new == nil) { // the pod count of the workload changed
			r := new
			if r == nil {
				r = old
			}
			if w, ok := e.ctx().WorkloadOfOwner(key.NS, r.OwnerKind().Value(), r.OwnerName().Value()); ok {
				e.recomposeOwner(w)
			}
		}
	case GRReplicas:
		if old != nil && old.OwnerKind().Value() == "Deployment" {
			setDel(ix.rsByDep, nsName(key.NS, old.OwnerName().Value()), key.Name)
		}
		if new != nil && new.OwnerKind().Value() == "Deployment" {
			setAdd(ix.rsByDep, nsName(key.NS, new.OwnerName().Value()), key.Name)
		}
	case GRJobs:
		if old != nil && old.OwnerKind().Value() == "CronJob" {
			setDel(ix.jobsByCron, nsName(key.NS, old.OwnerName().Value()), key.Name)
		}
		if new != nil && new.OwnerKind().Value() == "CronJob" {
			setAdd(ix.jobsByCron, nsName(key.NS, new.OwnerName().Value()), key.Name)
		}
	case GRSlices:
		if old != nil {
			if svc := old.StrCell(0); svc != "" {
				if s := ix.svcSlices[nsName(key.NS, svc)]; s != nil {
					delete(s, key.Name)
					if len(s) == 0 {
						delete(ix.svcSlices, nsName(key.NS, svc))
					}
				}
			}
		}
		if new != nil {
			if svc := new.StrCell(0); svc != "" {
				s := ix.svcSlices[nsName(key.NS, svc)]
				if s == nil {
					s = map[string][2]int64{}
					ix.svcSlices[nsName(key.NS, svc)] = s
				}
				s[key.Name] = [2]int64{new.Cell(2).N, new.Cell(3).N}
			}
		}
	case GRIngresses:
		if old != nil {
			if f, ok := old.Facts().(*ingest.IngressFacts); ok {
				for _, s := range f.Services {
					setDel(ix.ingBySvc, nsName(key.NS, s), key.Name)
				}
			}
		}
		if new != nil {
			if f, ok := new.Facts().(*ingest.IngressFacts); ok {
				for _, s := range f.Services {
					setAdd(ix.ingBySvc, nsName(key.NS, s), key.Name)
				}
			}
		}
	case GREvents:
		if old != nil {
			setDel(ix.evBy, evSubjectKey(old), nsName(key.NS, key.Name))
		}
		if new != nil {
			setAdd(ix.evBy, evSubjectKey(new), nsName(key.NS, key.Name))
		}
	}
}

// evSubjectKey is the evBy key of an event row: the involved object's namespace, kind and name.
func evSubjectKey(r *ingest.Row) string { return evInvolvedNS(r) + "/" + r.StrCell(evObject) }

// evInvolvedNS is the namespace of an event's involved object ("" for cluster-scoped objects).
func evInvolvedNS(r *ingest.Row) string {
	if f, ok := r.Facts().(*ingest.EventFacts); ok {
		return f.InvolvedNS
	}
	return r.GetNamespace()
}

// dependents are the built-in join edges: which other keys must be re-evaluated because key changed.
func (ix *indexes) dependents(e *Engine, gr string, key Key, old, new *ingest.Row) []Key {
	var out []Key
	switch gr {
	case GRReplicas:
		if old == nil || new == nil || ownerKey(old) != ownerKey(new) {
			for p := range ix.podsByOwner["ReplicaSet/"+key.NS+"/"+key.Name] {
				out = append(out, Key{GRPods, key.NS, p})
			}
		}
	case GRSlices:
		r := new
		if r == nil {
			r = old
		}
		if svc := r.StrCell(0); svc != "" {
			out = append(out, Key{GRServices, key.NS, svc})
			for in := range ix.ingBySvc[nsName(key.NS, svc)] {
				out = append(out, Key{GRIngresses, key.NS, in})
			}
		}
	case GRServices:
		for in := range ix.ingBySvc[nsName(key.NS, key.Name)] {
			out = append(out, Key{GRIngresses, key.NS, in})
		}
	case GRJobs:
		r := new
		if r == nil {
			r = old
		}
		if r.OwnerKind().Value() == "CronJob" {
			out = append(out, Key{GRCronJobs, key.NS, r.OwnerName().Value()})
			if old == nil || new == nil { // the pods of a new (or removed) run roll up to the CronJob
				for p := range ix.podsByOwner["Job/"+key.NS+"/"+key.Name] {
					out = append(out, Key{GRPods, key.NS, p})
				}
			}
		}
	}
	return out
}

// recomposeOwner marks the aggregates over a workload for recomposition (its pod count changed).
func (e *Engine) recomposeOwner(w Subject) {
	for id := range e.aggOwner[w.key()] {
		e.touched[id] = e.touched[id] || false
	}
}

// ---- Ctx accessors ------------------------------------------------------------------------

// Now is the engine clock.
func (c *Ctx) Now() time.Time { return c.e.Now() }

// Cluster is the cluster id.
func (c *Ctx) Cluster() string { return c.e.cluster }

// Get returns a row of a watched kind, or nil.
func (c *Ctx) Get(gr, ns, name string) *ingest.Row {
	if t := c.e.tables[gr]; t != nil {
		return t.rows[nk{ns, name}]
	}
	return nil
}

// Each calls fn for every row of a kind (use only for small kinds, or for kinds a detector must scan).
func (c *Ctx) Each(gr string, fn func(ns, name string, r *ingest.Row) bool) {
	if t := c.e.tables[gr]; t != nil {
		for k, r := range t.rows {
			if !fn(k.ns, k.name, r) {
				return
			}
		}
	}
}

// Has reports whether the kind is watched in this cluster.
func (c *Ctx) Has(gr string) bool { return c.e.tables[gr] != nil }

// Count returns the number of rows of a kind.
func (c *Ctx) Count(gr string) int {
	if t := c.e.tables[gr]; t != nil {
		return len(t.rows)
	}
	return 0
}

// Discovered reports whether discovery lists the kind (even when it is not watched).
func (c *Ctx) Discovered(gr string) bool { return c.e.disco[gr] || c.e.tables[gr] != nil }

// Mod returns the module (columns) of a watched kind.
func (c *Ctx) Mod(gr string) *ingest.Module {
	if t := c.e.tables[gr]; t != nil {
		return t.mod
	}
	return nil
}

// Col returns the cell with the module's column key of a row of kind gr (zero Cell when unknown).
func (c *Ctx) Col(gr string, r *ingest.Row, key string) ingest.Cell {
	if m := c.Mod(gr); m != nil {
		if col := m.Col(key); col != nil && col.Src == ingest.SrcCell {
			return r.Cell(col.Idx)
		}
	}
	return ingest.Cell{S: ingest.Intern("")}
}

// SubjectOf builds the Subject of a row of kind gr.
func (c *Ctx) SubjectOf(gr string, r *ingest.Row) Subject {
	s := Subject{NS: r.GetNamespace(), Name: r.Name(), UID: r.UID()}
	if m := c.Mod(gr); m != nil {
		s.Group, s.Kind, s.GVR = m.Group, m.Kind, m.GVR
	}
	return s
}

// WorkloadOfOwner resolves a pod's controller reference to the workload it rolls up to: ReplicaSet ->
// Deployment, StatefulSet, DaemonSet and Job as themselves. ok is false for standalone pods.
func (c *Ctx) WorkloadOfOwner(ns, kind, name string) (Subject, bool) {
	switch kind {
	case "ReplicaSet":
		if rs := c.Get(GRReplicas, ns, name); rs != nil {
			if rs.OwnerKind().Value() == "Deployment" {
				return Subject{Group: "apps", Kind: "Deployment", NS: ns, Name: rs.OwnerName().Value(), GVR: "apps/v1/deployments"}, true
			}
			return Subject{Group: "apps", Kind: "ReplicaSet", NS: ns, Name: name, GVR: "apps/v1/replicasets"}, true
		}
		if i := strings.LastIndexByte(name, '-'); i > 0 { // the ReplicaSet is not (or no longer) known: strip the template hash
			return Subject{Group: "apps", Kind: "Deployment", NS: ns, Name: name[:i], GVR: "apps/v1/deployments"}, true
		}
	case "StatefulSet", "DaemonSet":
		return Subject{Group: "apps", Kind: kind, NS: ns, Name: name, GVR: "apps/v1/" + strings.ToLower(kind) + "s"}, true
	case "Job":
		// the runs of a CronJob share one problem (and one acknowledgement): the pods roll up to the CronJob
		if j := c.Get(GRJobs, ns, name); j != nil && j.OwnerKind().Value() == "CronJob" {
			return Subject{Group: "batch", Kind: "CronJob", NS: ns, Name: j.OwnerName().Value(), GVR: "batch/v1/cronjobs"}, true
		}
		return Subject{Group: "batch", Kind: "Job", NS: ns, Name: name, GVR: "batch/v1/jobs"}, true
	}
	return Subject{}, false
}

// PodsOf returns the names of the pods of a workload.
func (c *Ctx) PodsOf(w Subject) []string {
	ix := &c.e.idx
	var out []string
	add := func(m map[string]struct{}) {
		for p := range m {
			out = append(out, p)
		}
	}
	switch w.Kind {
	case "Deployment":
		for rs := range ix.rsByDep[nsName(w.NS, w.Name)] {
			add(ix.podsByOwner["ReplicaSet/"+w.NS+"/"+rs])
		}
	case "CronJob":
		for j := range ix.jobsByCron[nsName(w.NS, w.Name)] {
			add(ix.podsByOwner["Job/"+w.NS+"/"+j])
		}
	default:
		add(ix.podsByOwner[w.Kind+"/"+w.NS+"/"+w.Name])
	}
	return out
}

// PodCount is len(PodsOf(w)) without allocating.
func (c *Ctx) PodCount(w Subject) int {
	ix := &c.e.idx
	if w.Kind == "Deployment" {
		n := 0
		for rs := range ix.rsByDep[nsName(w.NS, w.Name)] {
			n += len(ix.podsByOwner["ReplicaSet/"+w.NS+"/"+rs])
		}
		return n
	}
	if w.Kind == "CronJob" {
		n := 0
		for j := range ix.jobsByCron[nsName(w.NS, w.Name)] {
			n += len(ix.podsByOwner["Job/"+w.NS+"/"+j])
		}
		return n
	}
	return len(ix.podsByOwner[w.Kind+"/"+w.NS+"/"+w.Name])
}

// ReadyEndpoints sums the ready and total endpoints over every EndpointSlice of a Service; slices is how
// many there are (0: the Service has no slices at all).
func (c *Ctx) ReadyEndpoints(ns, svc string) (ready, total int64, slices int) {
	for _, v := range c.e.idx.svcSlices[nsName(ns, svc)] {
		ready += v[0]
		total += v[1]
		slices++
	}
	return
}

// IngressesOf returns the Ingresses that route to a Service.
func (c *Ctx) IngressesOf(ns, svc string) []string {
	var out []string
	for in := range c.e.idx.ingBySvc[nsName(ns, svc)] {
		out = append(out, in)
	}
	return out
}

// JobsOf returns the Jobs owned by a CronJob.
func (c *Ctx) JobsOf(ns, cron string) []string {
	var out []string
	for j := range c.e.idx.jobsByCron[nsName(ns, cron)] {
		out = append(out, j)
	}
	return out
}

// EventsFor returns the Warning events about an object, newest first, at most n.
func (c *Ctx) EventsFor(kind, ns, name string, n int) []EventInfo {
	var out []EventInfo
	for ek := range c.e.idx.evBy[ns+"/"+kind+"/"+name] {
		ens, en, _ := strings.Cut(ek, "/")
		if r := c.Get(GREvents, ens, en); r != nil {
			out = append(out, EventInfo{Name: en, Reason: r.StrCell(evReason), Message: r.StrCell(evMessage), Count: r.Cell(evCount).N, Last: r.Cell(evLast).N})
		}
	}
	for i := 1; i < len(out); i++ { // insertion sort: a handful of events
		for j := i; j > 0 && out[j].Last > out[j-1].Last; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

type progState struct {
	ready, updated, avail int32
	at                    time.Time
}

// ProgressSince returns when a workload's rollout last made progress: the last time its ready, updated or
// available replica counts increased (the first time the engine saw the workload when they never did).
// Decreases are recorded but are not progress.
func (c *Ctx) ProgressSince(k Key, ready, updated, avail int32) time.Time {
	e := c.e
	st, ok := e.prog[k]
	switch {
	case !ok:
		st = progState{ready, updated, avail, e.Now()}
	default:
		if ready > st.ready || updated > st.updated || avail > st.avail {
			st.at = e.Now()
		}
		st.ready, st.updated, st.avail = ready, updated, avail
	}
	e.prog[k] = st
	return st.at
}
