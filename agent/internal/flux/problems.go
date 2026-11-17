package flux

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems"
)

// GRs of the curated Flux kinds (version-less, as the problems engine keys kinds).
var (
	sourceGRs = []string{
		GroupSource + "/gitrepositories", GroupSource + "/ocirepositories", GroupSource + "/helmrepositories",
		GroupSource + "/helmcharts", GroupSource + "/buckets",
	}
	reconcileGRs = []string{GroupKustomize + "/kustomizations", GroupHelm + "/helmreleases"}
	allGRs       = append(append([]string{}, sourceGRs...), reconcileGRs...)
)

// fluxControllers are the Flux controller Deployments flux.controller-down watches.
var fluxControllers = map[string]bool{
	"source-controller": true, "kustomize-controller": true, "helm-controller": true, "notification-controller": true,
	"image-reflector-controller": true, "image-automation-controller": true, "source-watcher": true,
}

func firstNZ(vs ...int64) int64 {
	for _, v := range vs {
		if v != 0 {
			return v
		}
	}
	return 0
}

func msg(f *Facts) string { return strings.TrimSpace(f.Ready.Reason + ": " + f.Ready.Message) }

func isSource(gr string) bool {
	for _, g := range sourceGRs {
		if g == gr {
			return true
		}
	}
	return false
}

// notReady: Ready=False, excluding suspended objects, stalled ones (flux.stalled), sources (flux.source-failed)
// and dependency waits (flux.dependency-blocked). Progressing within the grace period is not a problem.
func evalNotReady(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
	f := FactsOf(r)
	if f == nil || f.Suspend || f.Ready.Status != "False" || isSource(k.GR) || f.Stalled.Status == "True" || isDepWait(f) {
		return nil
	}
	sub := c.SubjectOf(k.GR, r)
	return []problems.Draft{{Subject: sub, Title: fmt.Sprintf("%s %s is not ready: %s", sub.Kind, sub.Name, f.Ready.Reason), Message: msg(f),
		Since: firstNZ(f.Ready.Since, r.CreatedMs()), Grace: 5 * time.Minute}}
}

func isDepWait(f *Facts) bool {
	return f.Ready.Status == "False" && f.Ready.Reason == "DependencyNotReady"
}

func evalStalled(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
	f := FactsOf(r)
	if f == nil || f.Suspend || f.Stalled.Status != "True" {
		return nil
	}
	sub := c.SubjectOf(k.GR, r)
	return []problems.Draft{{Subject: sub, Title: fmt.Sprintf("%s %s is stalled: %s", sub.Kind, sub.Name, f.Stalled.Reason),
		Message: strings.TrimSpace(f.Stalled.Message), Since: firstNZ(f.Stalled.Since, r.CreatedMs())}}
}

// evalStale: no successful reconcile within 3x the interval. A healthy object has no timestamp that moves
// on every reconcile, so the evidence is a Ready condition that never reached True (Unknown, or missing)
// for 3x its interval: a hung or never-started reconcile. A failing one (Ready=False) is flux.not-ready.
func evalStale(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
	f := FactsOf(r)
	if f == nil || f.Suspend || f.Interval <= 0 || f.Stalled.Status == "True" || f.Ready.Status == "True" || f.Ready.Status == "False" {
		return nil
	}
	sub := c.SubjectOf(k.GR, r)
	since := firstNZ(f.Ready.Since, r.CreatedMs())
	return []problems.Draft{{Subject: sub, Title: fmt.Sprintf("%s %s has not reconciled for %s (interval %s)", sub.Kind, sub.Name, 3*f.Interval, f.Interval),
		Message: msg(f), Since: since, Grace: 3 * f.Interval}}
}

func evalSourceFailed(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
	f := FactsOf(r)
	if f == nil || f.Suspend || f.Ready.Status != "False" {
		return nil
	}
	sub := c.SubjectOf(k.GR, r)
	return []problems.Draft{{Subject: sub, Title: fmt.Sprintf("%s %s failed: %s", sub.Kind, sub.Name, f.Ready.Reason), Message: truncate(f.Ready.Message),
		Since: firstNZ(f.Ready.Since, r.CreatedMs()), Grace: time.Minute}}
}

func truncate(s string) string {
	if len(s) > 200 {
		return strings.ToValidUTF8(s[:200], "") + "..."
	}
	return s
}

func evalSuspended(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
	f := FactsOf(r)
	if f == nil || !f.Suspend {
		return nil
	}
	sub := c.SubjectOf(k.GR, r)
	return []problems.Draft{{Subject: sub, Title: fmt.Sprintf("%s %s is suspended", sub.Kind, sub.Name), Since: r.CreatedMs()}}
}

// ---- dependency-blocked ---------------------------------------------------------------------

// rootCause follows a chain of DependencyNotReady waits to the object that actually fails: the first
// dependency that is not Ready and is not itself waiting on another. A missing dependency is its own root.
// blocked is the chain of objects waiting on it, in order.
func rootCause(c *problems.Ctx, gr string, k problems.Key, r *ingest.Row) (root problems.Key, rootRow *ingest.Row, missing string) {
	seen := map[problems.Key]bool{k: true}
	cur, curRow := k, r
	for {
		f := FactsOf(curRow)
		var next *DepRef
		var nextRow *ingest.Row
		for i := range f.DependsOn {
			d := f.DependsOn[i]
			row := c.Get(gr, d.NS, d.Name)
			if row == nil {
				return cur, curRow, d.NS + "/" + d.Name
			}
			if df := FactsOf(row); df != nil && df.Ready.Status != "True" && !df.Suspend {
				next, nextRow = &d, row
				break
			}
		}
		if next == nil {
			return cur, curRow, ""
		}
		nk := problems.Key{GR: gr, NS: next.NS, Name: next.Name}
		if seen[nk] {
			return nk, nextRow, "" // a dependency cycle: report where it closes
		}
		seen[nk] = true
		nf := FactsOf(nextRow)
		if !isDepWait(nf) {
			return nk, nextRow, ""
		}
		cur, curRow = nk, nextRow
	}
}

func evalDepBlocked(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
	f := FactsOf(r)
	if f == nil || f.Suspend || !isDepWait(f) {
		return nil
	}
	rk, rr, missing := rootCause(c, k.GR, k, r)
	sub := c.SubjectOf(k.GR, rr)
	rf := FactsOf(rr)
	d := problems.Draft{Subject: sub, Label: k.NS + "/" + k.Name, Since: firstNZ(f.Ready.Since, r.CreatedMs()), Grace: 5 * time.Minute,
		Evidence: []problems.Ref{{Group: sub.Group, Kind: sub.Kind, NS: k.NS, Name: k.Name, Note: "blocked"}}}
	if missing != "" {
		d.Message = "dependency " + missing + " not found"
		d.Disc = "missing"
	} else if rf != nil && rk != k {
		d.Message = msg(rf)
	} else {
		d.Message = f.Ready.Message
	}
	return []problems.Draft{d}
}

func depBlockedAggregate(c *problems.Ctx, ms []problems.Draft) problems.Draft {
	out := ms[0]
	out.Count = len(ms)
	out.Evidence = nil
	out.Since = 0
	for i := range ms {
		out.Evidence = append(out.Evidence, ms[i].Evidence...)
		if out.Since == 0 || (ms[i].Since != 0 && ms[i].Since < out.Since) {
			out.Since = ms[i].Since
		}
	}
	sort.Slice(out.Evidence, func(i, j int) bool {
		return out.Evidence[i].NS+out.Evidence[i].Name < out.Evidence[j].NS+out.Evidence[j].Name
	})
	if out.Disc == "missing" {
		out.Title = fmt.Sprintf("%s %s: missing dependency blocks %d object(s)", out.Subject.Kind, out.Subject.Name, len(ms))
	} else {
		out.Title = fmt.Sprintf("%s %s/%s is blocking %d dependent(s)", out.Subject.Kind, out.Subject.NS, out.Subject.Name, len(ms))
	}
	return out
}

// depDependents returns the objects that (transitively) wait on key k, for re-evaluation when k changes.
func depDependents(c *problems.Ctx, k problems.Key, _, _ *ingest.Row) []problems.Key {
	var out []problems.Key
	seen := map[problems.Key]bool{k: true}
	queue := []problems.Key{k}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		c.Each(cur.GR, func(ns, name string, r *ingest.Row) bool {
			f := FactsOf(r)
			if f == nil {
				return true
			}
			for _, d := range f.DependsOn {
				if d.NS == cur.NS && d.Name == cur.Name {
					nk := problems.Key{GR: cur.GR, NS: ns, Name: name}
					if !seen[nk] {
						seen[nk] = true
						out = append(out, nk)
						queue = append(queue, nk)
					}
				}
			}
			return true
		})
	}
	return out
}

// ---- controller-down ------------------------------------------------------------------------

// evalControllerDown flags a Flux controller Deployment in flux-system with no available replica. Deployment
// cells: ready ("x/y"), upToDate, available.
func evalControllerDown(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
	if k.NS != "flux-system" || !fluxControllers[k.Name] {
		return nil
	}
	if r.Cell(2).N > 0 {
		return nil
	}
	sub := c.SubjectOf(k.GR, r)
	since := r.CreatedMs()
	if wf, ok := r.Facts().(*ingest.WorkloadFacts); ok && wf.AvailableCond.Since != 0 {
		since = wf.AvailableCond.Since
	}
	return []problems.Draft{{Subject: sub, Title: "Flux " + k.Name + " is down", Message: "no available replicas (" + r.StrCell(0) + ")", Since: since, Grace: time.Minute}}
}

func init() {
	problems.Register(&problems.Detector{Class: "flux.not-ready", Family: problems.FamFlux, Default: true, Severity: problems.SevError, Title: "Flux objects not ready",
		Inputs: reconcileGRs, Evaluate: evalNotReady})
	problems.Register(&problems.Detector{Class: "flux.stalled", Family: problems.FamFlux, Default: true, Severity: problems.SevError, Title: "Stalled Flux objects",
		Inputs: reconcileGRs, Evaluate: evalStalled})
	problems.Register(&problems.Detector{Class: "flux.stale", Family: problems.FamFlux, Default: true, Severity: problems.SevWarning, Title: "Flux objects not reconciled for 3x their interval",
		Inputs: reconcileGRs, Evaluate: evalStale})
	problems.Register(&problems.Detector{Class: "flux.source-failed", Family: problems.FamFlux, Default: true, Severity: problems.SevError, Title: "Failed Flux sources",
		Inputs: sourceGRs, Evaluate: evalSourceFailed})
	problems.Register(&problems.Detector{Class: "flux.controller-down", Family: problems.FamFlux, Default: true, Severity: problems.SevCritical, Title: "Flux controllers down",
		Inputs: []string{problems.GRDeploys}, Evaluate: evalControllerDown})
	problems.Register(&problems.Detector{Class: "flux.dependency-blocked", Family: problems.FamFlux, Default: true, Severity: problems.SevWarning, Title: "Flux objects blocked by a dependency",
		Inputs: reconcileGRs, Triggers: reconcileGRs, Evaluate: evalDepBlocked, Dependents: depDependents, Aggregate: depBlockedAggregate})
	problems.Register(&problems.Detector{Class: "flux.suspended", Family: problems.FamFlux, Default: false, Severity: problems.SevInfo, Title: "Suspended Flux objects",
		Inputs: allGRs, Evaluate: evalSuspended})
}
