package problems

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
)

// podHit is one finding about one container (or the pod) before it is attached to its workload.
type podHit struct {
	Disc, Label, Title, Msg string
	Since                   int64
	Grace, TTL              time.Duration
	Sev                     Severity
}

func podFactsOf(r *ingest.Row) *ingest.PodFacts {
	f, _ := r.Facts().(*ingest.PodFacts)
	return f
}

// podEval adapts a per-pod matcher to a Detector.Evaluate: each hit becomes a draft whose problem id hashes
// the pod's workload (so pod churn creates no new problems), aggregated by podAggregate.
func podEval(match func(c *Ctx, r *ingest.Row, f *ingest.PodFacts) []podHit) func(c *Ctx, k Key, r *ingest.Row) []Draft {
	return func(c *Ctx, k Key, r *ingest.Row) []Draft {
		f := podFactsOf(r)
		if f == nil {
			return nil
		}
		hits := match(c, r, f)
		if len(hits) == 0 {
			return nil
		}
		sub := c.SubjectOf(GRPods, r)
		var owner *Subject
		if w, ok := c.WorkloadOfOwner(k.NS, r.OwnerKind().Value(), r.OwnerName().Value()); ok {
			owner = &w
		}
		out := make([]Draft, 0, len(hits))
		for _, h := range hits {
			out = append(out, Draft{Subject: sub, Owner: owner, Disc: h.Disc, Severity: h.Sev, Title: h.Title, Message: h.Msg, Label: h.Label,
				Since: h.Since, Grace: h.Grace, TTL: h.TTL})
		}
		return out
	}
}

// podAggregate rolls the pods of one workload that share a class into one problem:
// "Deployment api: 2/3 pods CrashLoopBackOff", with the pods as evidence. A single pod stays a pod row.
func podAggregate(c *Ctx, ms []Draft) Draft {
	if len(ms) == 1 {
		d := ms[0]
		d.Count = 1
		d.Evidence = []Ref{{Kind: "Pod", NS: d.Subject.NS, Name: d.Subject.Name}}
		return d
	}
	first := ms[0]
	out := Draft{Subject: *first.Owner, Disc: first.Disc, Label: first.Label, Message: first.Message, Count: len(ms), Severity: first.Severity}
	total := max(c.PodCount(*first.Owner), len(ms))
	out.Title = fmt.Sprintf("%s %s: %d/%d pods %s", first.Owner.Kind, first.Owner.Name, len(ms), total, first.Label)
	for i := range ms {
		d := &ms[i]
		if out.Since == 0 || (d.Since != 0 && d.Since < out.Since) {
			out.Since = d.Since
		}
		if d.Severity != SevDefault && (out.Severity == SevDefault || d.Severity < out.Severity) {
			out.Severity = d.Severity
		}
		if len(out.Evidence) < 25 {
			out.Evidence = append(out.Evidence, Ref{Kind: "Pod", NS: d.Subject.NS, Name: d.Subject.Name, Note: d.Label})
		}
	}
	return out
}

var (
	imagePullReasons = map[string]bool{"ImagePullBackOff": true, "ErrImagePull": true, "InvalidImageName": true, "ErrImageNeverPull": true}
	configReasons    = map[string]bool{"CreateContainerConfigError": true}
)

func firstNonZero(vs ...int64) int64 {
	for _, v := range vs {
		if v != 0 {
			return v
		}
	}
	return 0
}

func explained(f *ingest.PodFacts) bool {
	for i := range f.Containers {
		w := f.Containers[i].Waiting
		if imagePullReasons[w] || configReasons[w] || w == "CrashLoopBackOff" {
			return true
		}
	}
	return false
}

// crashLooping reports a container that keeps crashing: waiting in CrashLoopBackOff, or (between back-off
// periods the kubelet shows it terminated or just restarted) restarted repeatedly with a failing exit.
func crashLooping(ct *ingest.CtrFact, now time.Time) bool {
	switch {
	case ct.Waiting == "CrashLoopBackOff":
		return true
	case ct.Restarts >= 3 && ct.TermReason != "" && ct.TermReason != "Completed" && ct.TermExit != 0:
		return true
	case ct.Restarts >= 3 && ct.Started != 0 && now.UnixMilli()-ct.Started < 90_000 && ct.LastTermExit != 0:
		return true
	}
	return false
}

// oomKilled reports the time of the container's last OOM kill: the reported reason, or exit 137 of a
// container that has a memory limit (some runtimes report "Error" for an OOM kill).
func oomKilled(ct *ingest.CtrFact) (at int64, ok bool) {
	switch {
	case ct.LastTermReason == "OOMKilled":
		return ct.LastTermAt, true
	case ct.TermReason == "OOMKilled":
		return ct.TermAt, true
	case ct.MemLimit && ct.LastTermExit == 137 && ct.LastTermReason != "":
		return ct.LastTermAt, true
	case ct.MemLimit && ct.TermExit == 137 && ct.TermReason != "":
		return ct.TermAt, true
	}
	return 0, false
}

func lastExit(c *ingest.CtrFact) string {
	switch {
	case c.LastTermReason != "":
		return fmt.Sprintf("last exit: %s (code %d)", c.LastTermReason, c.LastTermExit)
	case c.TermReason != "":
		return fmt.Sprintf("last exit: %s (code %d)", c.TermReason, c.TermExit)
	}
	return ""
}

func init() {
	podIn := []string{GRPods}
	podTrig := []string{GRReplicas}
	reg := func(d *Detector) {
		d.Inputs, d.Triggers, d.Aggregate = podIn, podTrig, podAggregate
		Register(d)
	}

	reg(&Detector{Class: "pod.crashloop", Family: FamPods, Default: true, Severity: SevError, Title: "Crash-looping pods",
		Evaluate: podEval(func(c *Ctx, r *ingest.Row, f *ingest.PodFacts) []podHit {
			var out []podHit
			for i := range f.Containers {
				ct := &f.Containers[i]
				if !crashLooping(ct, c.Now()) {
					continue
				}
				out = append(out, podHit{Disc: ct.Name, Label: "CrashLoopBackOff",
					Title: fmt.Sprintf("CrashLoopBackOff: %s (restarts %d)", ct.Name, ct.Restarts), Msg: lastExit(ct),
					Since: firstNonZero(f.Ready.Since, ct.LastTermAt, ct.TermAt, f.Scheduled.Since)})
			}
			return out
		})})

	reg(&Detector{Class: "pod.image-pull", Family: FamPods, Default: true, Severity: SevError, Title: "Image pull failures",
		Evaluate: podEval(func(c *Ctx, r *ingest.Row, f *ingest.PodFacts) []podHit {
			var out []podHit
			for i := range f.Containers {
				ct := &f.Containers[i]
				if !imagePullReasons[ct.Waiting] {
					continue
				}
				out = append(out, podHit{Disc: ct.Name, Label: ct.Waiting, Title: fmt.Sprintf("%s: %s", ct.Waiting, ct.Name), Msg: ct.WaitMsg,
					Since: firstNonZero(f.Scheduled.Since, r.CreatedMs())})
			}
			return out
		})})

	reg(&Detector{Class: "pod.oomkilled", Family: FamPods, Default: true, Severity: SevWarning, Title: "OOMKilled within the last hour",
		Evaluate: podEval(func(c *Ctx, r *ingest.Row, f *ingest.PodFacts) []podHit {
			var out []podHit
			for i := range f.Containers {
				ct := &f.Containers[i]
				at, ok := oomKilled(ct)
				if !ok {
					continue
				}
				out = append(out, podHit{Disc: ct.Name, Label: "OOMKilled", Title: fmt.Sprintf("OOMKilled: %s (restarts %d)", ct.Name, ct.Restarts),
					Msg: "container exceeded its memory limit (exit 137)", Since: at, TTL: time.Hour})
			}
			return out
		})})

	reg(&Detector{Class: "pod.pending", Family: FamPods, Default: true, Severity: SevWarning, Title: "Pods pending for more than 5 minutes",
		Evaluate: podEval(func(c *Ctx, r *ingest.Row, f *ingest.PodFacts) []podHit {
			if f.Phase != "Pending" || f.DeletingAt != 0 || explained(f) {
				return nil
			}
			msg := "waiting to be scheduled"
			if f.Scheduled.Status == "False" {
				msg = strings.TrimSpace(f.Scheduled.Reason + ": " + f.Scheduled.Message)
			} else if f.Scheduled.Status == "True" {
				msg = "scheduled, containers not started"
				for i := range f.Containers {
					if w := f.Containers[i].Waiting; w != "" {
						msg = w
						break
					}
				}
			}
			return []podHit{{Label: "Pending", Title: "Pending: " + r.Name(), Msg: msg, Since: firstNonZero(f.Scheduled.Since, r.CreatedMs()), Grace: 5 * time.Minute}}
		})})

	reg(&Detector{Class: "pod.not-ready", Family: FamPods, Default: true, Severity: SevWarning, Title: "Running pods not ready for more than 5 minutes",
		Evaluate: podEval(func(c *Ctx, r *ingest.Row, f *ingest.PodFacts) []podHit {
			if f.Phase != "Running" || f.DeletingAt != 0 || f.Ready.Status != "False" || explained(f) {
				return nil
			}
			var notReady []string
			for i := range f.Containers {
				if ct := &f.Containers[i]; !ct.Init && !ct.Ready {
					notReady = append(notReady, ct.Name)
				}
			}
			sort.Strings(notReady)
			return []podHit{{Label: "NotReady", Title: "Not ready: " + r.Name(), Msg: "readiness failing: " + strings.Join(notReady, ", "),
				Since: firstNonZero(f.Ready.Since, r.CreatedMs()), Grace: 5 * time.Minute}}
		})})

	reg(&Detector{Class: "pod.evicted", Family: FamPods, Default: false, Severity: SevInfo, Title: "Evicted pods",
		Evaluate: podEval(func(c *Ctx, r *ingest.Row, f *ingest.PodFacts) []podHit {
			if f.Phase != "Failed" || f.Reason != "Evicted" {
				return nil
			}
			return []podHit{{Label: "Evicted", Title: "Evicted: " + r.Name(), Msg: f.Message, Since: r.CreatedMs()}}
		})})

	reg(&Detector{Class: "pod.stuck-terminating", Family: FamPods, Default: true, Severity: SevWarning, Title: "Pods stuck terminating",
		Evaluate: podEval(func(c *Ctx, r *ingest.Row, f *ingest.PodFacts) []podHit {
			if f.DeletingAt == 0 || f.Phase == "Succeeded" || f.Phase == "Failed" {
				return nil
			}
			return []podHit{{Label: "Terminating", Title: "Stuck terminating: " + r.Name(), Msg: "deletionTimestamp set, pod still present", Since: f.DeletingAt, Grace: 10 * time.Minute}}
		})})

	reg(&Detector{Class: "pod.config-error", Family: FamPods, Default: true, Severity: SevError, Title: "Container configuration errors",
		Evaluate: podEval(func(c *Ctx, r *ingest.Row, f *ingest.PodFacts) []podHit {
			var out []podHit
			for i := range f.Containers {
				ct := &f.Containers[i]
				if !configReasons[ct.Waiting] {
					continue
				}
				out = append(out, podHit{Disc: ct.Name, Label: ct.Waiting, Title: fmt.Sprintf("%s: %s", ct.Waiting, ct.Name), Msg: ct.WaitMsg,
					Since: firstNonZero(f.Scheduled.Since, r.CreatedMs())})
			}
			return out
		})})

	reg(&Detector{Class: "pod.restarting", Family: FamPods, Default: false, Severity: SevWarning, Title: "Containers restarted within 10 minutes",
		Evaluate: podEval(func(c *Ctx, r *ingest.Row, f *ingest.PodFacts) []podHit {
			var out []podHit
			for i := range f.Containers {
				ct := &f.Containers[i]
				if ct.Restarts == 0 || ct.LastTermAt == 0 || crashLooping(ct, c.Now()) {
					continue
				}
				out = append(out, podHit{Disc: ct.Name, Label: "restarting", Title: fmt.Sprintf("Restarted: %s (restarts %d)", ct.Name, ct.Restarts), Msg: lastExit(ct),
					Since: ct.LastTermAt, TTL: 10 * time.Minute})
			}
			return out
		})})
}
