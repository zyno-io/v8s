package problems

import (
	"fmt"
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
)

func workloadFacts(r *ingest.Row) *ingest.WorkloadFacts {
	f, _ := r.Facts().(*ingest.WorkloadFacts)
	return f
}

// unhealthyPodRefs lists up to n pods of a workload that carry facts (not healthy) as evidence.
func unhealthyPodRefs(c *Ctx, w Subject, n int) []Ref {
	var out []Ref
	for _, p := range c.PodsOf(w) {
		if r := c.Get(GRPods, w.NS, p); r != nil && r.Facts() != nil {
			out = append(out, Ref{Kind: "Pod", NS: w.NS, Name: p, Note: r.StrCell(2)})
			if len(out) >= n {
				break
			}
		}
	}
	return out
}

func init() {
	workloads := []string{GRDeploys, GRSets, GRDaemons}

	Register(&Detector{Class: "workload.unavailable", Family: FamWorkloads, Default: true, Severity: SevError, Title: "Workloads with unavailable replicas",
		Inputs: workloads, Triggers: []string{GRPods},
		Dependents: func(c *Ctx, k Key, old, new *ingest.Row) []Key { return nil },
		Evaluate: func(c *Ctx, k Key, r *ingest.Row) []Draft {
			f := workloadFacts(r)
			if f == nil || f.Desired <= 0 || f.Available >= f.Desired || f.Paused {
				return nil
			}
			sub := c.SubjectOf(k.GR, r)
			since := firstNonZero(f.AvailableCond.Since*b2i(f.AvailableCond.Status == "False"), f.Progressing.Since)
			grace := 10 * time.Minute
			if f.ProgressDeadline > 0 {
				grace = time.Duration(f.ProgressDeadline) * time.Second
			}
			return []Draft{{Subject: sub, Title: fmt.Sprintf("%s %s: %d/%d available", sub.Kind, sub.Name, f.Available, f.Desired),
				Message: strings.TrimSpace(f.AvailableCond.Reason + " " + f.AvailableCond.Message), Since: since, Grace: grace,
				Count: int(f.Desired - f.Available), Evidence: unhealthyPodRefs(c, sub, 10)}}
		}})

	Register(&Detector{Class: "workload.rollout-stuck", Family: FamWorkloads, Default: true, Severity: SevError, Title: "Stuck rollouts",
		Inputs: []string{GRDeploys},
		Evaluate: func(c *Ctx, k Key, r *ingest.Row) []Draft {
			f := workloadFacts(r)
			if f == nil || f.Progressing.Status != "False" || f.Progressing.Reason != "ProgressDeadlineExceeded" {
				return nil
			}
			sub := c.SubjectOf(k.GR, r)
			return []Draft{{Subject: sub, Title: fmt.Sprintf("Deployment %s: rollout exceeded its progress deadline", sub.Name), Message: f.Progressing.Message,
				Since: f.Progressing.Since, Evidence: unhealthyPodRefs(c, sub, 10)}}
		}})

	Register(&Detector{Class: "hpa.unable", Family: FamWorkloads, Default: false, Severity: SevWarning, Title: "Autoscalers unable to scale",
		Inputs: []string{GRHPA},
		Evaluate: func(c *Ctx, k Key, r *ingest.Row) []Draft {
			f, _ := r.Facts().(*ingest.HPAFacts)
			if f == nil {
				return nil
			}
			sub := c.SubjectOf(k.GR, r)
			var out []Draft
			if f.ScalingActive.Status == "False" {
				out = append(out, Draft{Subject: sub, Disc: "inactive", Title: "HPA " + sub.Name + ": scaling inactive", Message: f.ScalingActive.Reason + ": " + f.ScalingActive.Message,
					Since: f.ScalingActive.Since})
			}
			if f.Max > 0 && f.Current >= f.Max {
				out = append(out, Draft{Subject: sub, Disc: "atmax", Title: fmt.Sprintf("HPA %s: at max replicas (%d)", sub.Name, f.Max),
					Since: firstNonZero(f.Limited.Since, r.CreatedMs()), Grace: 30 * time.Minute})
			}
			return out
		}})

	// ---- jobs ----
	Register(&Detector{Class: "job.failed", Family: FamJobs, Default: true, Severity: SevError, Title: "Failed Jobs",
		Inputs: []string{GRJobs},
		Evaluate: func(c *Ctx, k Key, r *ingest.Row) []Draft {
			f, _ := r.Facts().(*ingest.JobFacts)
			if f == nil || f.Failed.Status != "True" || r.OwnerKind().Value() == "CronJob" {
				return nil // a CronJob's failed Jobs are reported by cronjob.last-failed
			}
			sub := c.SubjectOf(k.GR, r)
			return []Draft{{Subject: sub, Title: fmt.Sprintf("Job %s failed: %s", sub.Name, f.Failed.Reason), Message: f.Failed.Message, Since: firstNonZero(f.Failed.Since, r.CreatedMs())}}
		}})

	Register(&Detector{Class: "cronjob.last-failed", Family: FamJobs, Default: true, Severity: SevError, Title: "CronJobs whose last Job failed",
		Inputs: []string{GRCronJobs}, Triggers: []string{GRJobs},
		Evaluate: func(c *Ctx, k Key, r *ingest.Row) []Draft {
			var last *ingest.Row
			for _, jn := range c.JobsOf(k.NS, k.Name) {
				if j := c.Get(GRJobs, k.NS, jn); j != nil && (last == nil || j.CreatedMs() > last.CreatedMs()) {
					last = j
				}
			}
			if last == nil {
				return nil
			}
			f, _ := last.Facts().(*ingest.JobFacts)
			if f == nil || f.Failed.Status != "True" {
				return nil
			}
			sub := c.SubjectOf(k.GR, r)
			return []Draft{{Subject: sub, Title: fmt.Sprintf("CronJob %s: last Job %s failed (%s)", sub.Name, last.Name(), f.Failed.Reason),
				Message: f.Failed.Message, Since: firstNonZero(f.Failed.Since, last.CreatedMs()),
				Evidence: []Ref{{Group: "batch", Kind: "Job", NS: k.NS, Name: last.Name()}}}}
		}})

	Register(&Detector{Class: "cronjob.not-scheduling", Family: FamJobs, Default: true, Severity: SevWarning, Title: "CronJobs not scheduling",
		Inputs: []string{GRCronJobs},
		Evaluate: func(c *Ctx, k Key, r *ingest.Row) []Draft {
			f, _ := r.Facts().(*ingest.CronFacts)
			if f == nil || f.Suspend {
				return nil
			}
			iv, ok := cronInterval(f.Schedule, c.Now())
			if !ok || iv <= 0 {
				return nil
			}
			base := max(f.LastSuccess, f.LastSchedule, r.CreatedMs())
			sub := c.SubjectOf(k.GR, r)
			return []Draft{{Subject: sub, Title: fmt.Sprintf("CronJob %s has not run for %s (schedule %s)", sub.Name, fmtDur(2*iv), f.Schedule),
				Message: "no scheduled run within twice the schedule interval", Since: base, Grace: 2 * iv}}
		}})

	// ---- nodes ----
	Register(&Detector{Class: "node.not-ready", Family: FamNodes, Default: true, Severity: SevCritical, Title: "NotReady nodes",
		Inputs: []string{GRNodes},
		Evaluate: func(c *Ctx, k Key, r *ingest.Row) []Draft {
			f, _ := r.Facts().(*ingest.NodeFacts)
			if f == nil || f.Ready.Status == "True" || f.Ready.Status == "" {
				return nil
			}
			sub := c.SubjectOf(k.GR, r)
			return []Draft{{Subject: sub, Title: "Node " + sub.Name + " is not ready", Message: strings.TrimSpace(f.Ready.Reason + ": " + f.Ready.Message),
				Since: firstNonZero(f.Ready.Since, r.CreatedMs()), Grace: 60 * time.Second}}
		}})
	Register(&Detector{Class: "node.pressure", Family: FamNodes, Default: true, Severity: SevError, Title: "Nodes under resource pressure",
		Inputs: []string{GRNodes},
		Evaluate: func(c *Ctx, k Key, r *ingest.Row) []Draft {
			f, _ := r.Facts().(*ingest.NodeFacts)
			if f == nil {
				return nil
			}
			sub := c.SubjectOf(k.GR, r)
			var out []Draft
			for _, p := range f.Pressure {
				out = append(out, Draft{Subject: sub, Disc: p.Type, Title: fmt.Sprintf("Node %s: %s", sub.Name, p.Type), Message: p.Message, Since: p.Since})
			}
			return out
		}})
	Register(&Detector{Class: "node.cordoned", Family: FamNodes, Default: false, Severity: SevInfo, Title: "Cordoned nodes",
		Inputs: []string{GRNodes},
		Evaluate: func(c *Ctx, k Key, r *ingest.Row) []Draft {
			f, _ := r.Facts().(*ingest.NodeFacts)
			if f == nil || !f.Cordoned {
				return nil
			}
			sub := c.SubjectOf(k.GR, r)
			return []Draft{{Subject: sub, Title: "Node " + sub.Name + " is cordoned", Message: "unschedulable"}}
		}})

	// ---- storage ----
	Register(&Detector{Class: "pvc.pending", Family: FamStorage, Default: true, Severity: SevError, Title: "PVCs pending for more than 5 minutes",
		Inputs: []string{GRPVC}, Triggers: []string{GREvents},
		Dependents: func(c *Ctx, k Key, old, new *ingest.Row) []Key {
			r := new
			if r == nil {
				r = old
			}
			if kind, name, ok := strings.Cut(r.StrCell(evObject), "/"); ok && kind == "PersistentVolumeClaim" {
				return []Key{{GRPVC, k.NS, name}}
			}
			return nil
		},
		Evaluate: func(c *Ctx, k Key, r *ingest.Row) []Draft {
			if r.StrCell(0) != "Pending" {
				return nil
			}
			sub := c.SubjectOf(k.GR, r)
			msg := "waiting to be bound"
			if evs := c.EventsFor("PersistentVolumeClaim", k.NS, k.Name, 1); len(evs) > 0 {
				msg = evs[0].Reason + ": " + evs[0].Message
			}
			return []Draft{{Subject: sub, Title: "PVC " + sub.Name + " is pending", Message: msg, Since: r.CreatedMs(), Grace: 5 * time.Minute}}
		}})
	Register(&Detector{Class: "pv.failed", Family: FamStorage, Default: false, Severity: SevWarning, Title: "Failed or released PVs",
		Inputs: []string{GRPV},
		Evaluate: func(c *Ctx, k Key, r *ingest.Row) []Draft {
			st := r.StrCell(0)
			if st != "Failed" && !(st == "Released" && r.StrCell(4) == "Retain") {
				return nil
			}
			sub := c.SubjectOf(k.GR, r)
			return []Draft{{Subject: sub, Title: fmt.Sprintf("PV %s is %s", sub.Name, st), Message: "claim " + r.StrCell(1), Since: r.CreatedMs()}}
		}})

	// ---- networking ----
	Register(&Detector{Class: "ingress.no-backend", Family: FamNetwork, Default: true, Severity: SevError, Title: "Ingresses routing to missing or empty Services",
		Inputs: []string{GRIngresses}, Triggers: []string{GRServices, GRSlices},
		Evaluate: func(c *Ctx, k Key, r *ingest.Row) []Draft {
			f, _ := r.Facts().(*ingest.IngressFacts)
			if f == nil {
				return nil
			}
			var bad []string
			var refs []Ref
			for _, svc := range f.Services {
				srow := c.Get(GRServices, k.NS, svc)
				if srow == nil {
					bad = append(bad, "Service "+svc+" not found")
					refs = append(refs, Ref{Kind: "Service", NS: k.NS, Name: svc, Note: "missing"})
					continue
				}
				if srow.StrCell(0) == "ExternalName" {
					continue
				}
				if ready, _, _ := c.ReadyEndpoints(k.NS, svc); ready == 0 {
					bad = append(bad, "Service "+svc+" has no ready endpoints")
					refs = append(refs, Ref{Kind: "Service", NS: k.NS, Name: svc, Note: "no ready endpoints"})
				}
			}
			if len(bad) == 0 {
				return nil
			}
			sub := c.SubjectOf(k.GR, r)
			return []Draft{{Subject: sub, Title: "Ingress " + sub.Name + " routes to nothing", Message: strings.Join(bad, "; "), Count: len(bad), Evidence: refs}}
		}})

	Register(&Detector{Class: "service.no-endpoints", Family: FamNetwork, Default: true, Severity: SevWarning, Title: "Services without ready endpoints",
		Inputs: []string{GRServices}, Triggers: []string{GRSlices},
		Evaluate: func(c *Ctx, k Key, r *ingest.Row) []Draft {
			// cells: type, clusterIP, externalIP, ports, selector
			if r.StrCell(0) == "ExternalName" || r.StrCell(4) == "" {
				return nil
			}
			if ready, _, _ := c.ReadyEndpoints(k.NS, k.Name); ready > 0 {
				return nil
			}
			sub := c.SubjectOf(k.GR, r)
			return []Draft{{Subject: sub, Title: "Service " + sub.Name + " has no ready endpoints", Message: "selector " + r.StrCell(4), Grace: 2 * time.Minute}}
		}})

	// ---- platform ----
	Register(&Detector{Class: "cluster.unreachable", Family: FamPlatform, Default: true, Severity: SevCritical, Title: "Unreachable clusters"})

	Register(&Detector{Class: "events.warning-burst", Family: FamPlatform, Default: false, Severity: SevWarning, Title: "Bursts of Warning events",
		Inputs: []string{GREvents},
		Evaluate: func(c *Ctx, k Key, r *ingest.Row) []Draft {
			kind, name, ok := strings.Cut(r.StrCell(evObject), "/")
			if !ok {
				return nil
			}
			if kind == "Pod" { // pod problems are explained by the pod classes
				if p := c.Get(GRPods, k.NS, name); p != nil && p.Facts() != nil {
					return nil
				}
			}
			cutoff := c.Now().Add(-10 * time.Minute).UnixMilli()
			var n int64
			oldest := int64(0)
			for _, e := range c.EventsFor(kind, k.NS, name, 0) {
				if e.Last >= cutoff {
					n += max(e.Count, 1)
					if oldest == 0 || e.Last < oldest {
						oldest = e.Last
					}
				}
			}
			if n < 10 {
				return nil
			}
			return []Draft{{Subject: Subject{Kind: kind, NS: k.NS, Name: name}, Title: fmt.Sprintf("%d Warning events for %s %s in 10 minutes", n, kind, name),
				Message: r.StrCell(evReason) + ": " + r.StrCell(evMessage), Count: int(n), Since: oldest, TTL: 10 * time.Minute}}
		}})
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func fmtDur(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
