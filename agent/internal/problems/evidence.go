package problems

import (
	"context"
	"fmt"
	"time"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func refOut(r Ref) protocol.ProblemRef {
	return protocol.ProblemRef{Kind: r.Kind, Namespace: r.NS, Name: r.Name, Note: r.Note}
}

// Evidence answers problems.evidence: the triggering conditions and container states from the live object
// (a pod of the group for rollups), the last 5 Warning events about the subject and its pods (from the
// agent's event cache) and related objects.
func (h *Hub) Evidence(ctx context.Context, id string) (protocol.ProblemsEvidenceResult, error) {
	for _, e := range h.engineList() {
		p, ok := e.Problem(id)
		if !ok {
			continue
		}
		res := protocol.ProblemsEvidenceResult{ID: id, Cluster: e.cluster, Class: p.Class, Title: p.Title, Message: p.Message, Since: p.Since,
			Subject:    protocol.ProblemRef{GVR: p.Subject.GVR, Kind: p.Subject.Kind, Namespace: p.Subject.NS, Name: p.Subject.Name},
			Conditions: []protocol.ProblemCondition{}, Containers: []protocol.ProblemContainer{}, Events: []protocol.ProblemEvent{}, Related: []protocol.ProblemRef{}}
		for _, r := range p.Evidence {
			res.Related = append(res.Related, refOut(r))
		}
		// objects whose live state and events explain the problem: the subject, else the rolled-up pods
		type target struct{ gvr, kind, ns, name string }
		var targets []target
		if p.Subject.Kind == "Pod" || p.Subject.GVR != "" && p.Subject.Kind != "Deployment" && p.Subject.Kind != "StatefulSet" && p.Subject.Kind != "DaemonSet" && p.Subject.Kind != "Job" {
			targets = append(targets, target{p.Subject.GVR, p.Subject.Kind, p.Subject.NS, p.Subject.Name})
		} else {
			if p.Subject.GVR != "" {
				res.Conditions = res.Conditions[:0]
				targets = append(targets, target{p.Subject.GVR, p.Subject.Kind, p.Subject.NS, p.Subject.Name})
			}
			n := 0
			for _, r := range p.Evidence {
				if r.Kind == "Pod" && n < 3 {
					targets = append(targets, target{"v1/pods", "Pod", r.NS, r.Name})
					n++
				}
			}
		}
		seenEv := map[string]bool{}
		var evs []EventInfo
		var evObj []string
		e.mu.Lock()
		c := e.ctx()
		for _, t := range targets {
			for _, ev := range c.EventsFor(t.kind, t.ns, t.name, 5) {
				if !seenEv[t.kind+t.name+ev.Name] {
					seenEv[t.kind+t.name+ev.Name] = true
					evs = append(evs, ev)
					evObj = append(evObj, t.kind+"/"+t.name)
				}
			}
		}
		e.mu.Unlock()
		for i := 1; i < len(evs); i++ { // newest first
			for j := i; j > 0 && evs[j].Last > evs[j-1].Last; j-- {
				evs[j], evs[j-1] = evs[j-1], evs[j]
				evObj[j], evObj[j-1] = evObj[j-1], evObj[j]
			}
		}
		for i, ev := range evs {
			if i >= 5 {
				break
			}
			res.Events = append(res.Events, protocol.ProblemEvent{Reason: ev.Reason, Message: ev.Message, Count: int(ev.Count), LastSeen: ev.Last, Object: evObj[i]})
		}
		if h.cfg.Fetch != nil {
			for i, t := range targets {
				if t.gvr == "" {
					continue
				}
				fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				o, err := h.cfg.Fetch(fctx, e.cluster, t.gvr, t.ns, t.name)
				cancel()
				if err != nil {
					if i == 0 {
						res.Error = err.Error()
					}
					continue
				}
				fillLive(&res, o, i == 0, t.kind == "Pod" && len(targets) > 1)
				if i == 0 {
					res.Related = appendOwners(res.Related, o)
				}
			}
		}
		return res, nil
	}
	return protocol.ProblemsEvidenceResult{}, errUnknown
}

func appendOwners(rel []protocol.ProblemRef, o *unstructured.Unstructured) []protocol.ProblemRef {
	for _, ow := range o.GetOwnerReferences() {
		rel = append(rel, protocol.ProblemRef{Kind: ow.Kind, Namespace: o.GetNamespace(), Name: ow.Name, Note: "owner"})
	}
	return rel
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func parseT(s string) int64 {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixMilli()
	}
	return 0
}

// fillLive copies status.conditions (first target only) and container states of a live object.
func fillLive(res *protocol.ProblemsEvidenceResult, o *unstructured.Unstructured, conds bool, tagPod bool) {
	st, _ := o.Object["status"].(map[string]any)
	if conds {
		if cs, ok := st["conditions"].([]any); ok {
			for _, c := range cs {
				m, _ := c.(map[string]any)
				res.Conditions = append(res.Conditions, protocol.ProblemCondition{Type: str(m, "type"), Status: str(m, "status"), Reason: str(m, "reason"),
					Message: str(m, "message"), Since: parseT(str(m, "lastTransitionTime"))})
			}
		}
	}
	for _, key := range []string{"initContainerStatuses", "containerStatuses", "ephemeralContainerStatuses"} {
		cs, _ := st[key].([]any)
		for _, c := range cs {
			m, _ := c.(map[string]any)
			pc := protocol.ProblemContainer{Name: str(m, "name")}
			if r, ok := m["restartCount"].(int64); ok {
				pc.Restarts = int(r)
			} else if r, ok := m["restartCount"].(float64); ok {
				pc.Restarts = int(r)
			}
			if tagPod {
				pc.Pod = o.GetName()
			}
			state, _ := m["state"].(map[string]any)
			switch {
			case state["waiting"] != nil:
				w, _ := state["waiting"].(map[string]any)
				pc.State, pc.Reason, pc.Message = "waiting", str(w, "reason"), str(w, "message")
			case state["terminated"] != nil:
				t, _ := state["terminated"].(map[string]any)
				pc.State, pc.Reason, pc.Message, pc.Since = "terminated", str(t, "reason"), str(t, "message"), parseT(str(t, "finishedAt"))
				pc.ExitCode = int(num(t["exitCode"]))
			case state["running"] != nil:
				r, _ := state["running"].(map[string]any)
				pc.State, pc.Since = "running", parseT(str(r, "startedAt"))
			}
			if last, _ := m["lastState"].(map[string]any); last != nil {
				if t, _ := last["terminated"].(map[string]any); t != nil {
					pc.LastReason = fmt.Sprintf("%s", str(t, "reason"))
				}
			}
			if pc.State != "" {
				res.Containers = append(res.Containers, pc)
			}
		}
	}
}
