package flux

import (
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
)

// DepRef is a dependsOn entry of a Kustomization or HelmRelease (same kind, namespace defaulted).
type DepRef struct{ NS, Name string }

// Facts is what the Flux detectors read next to the display cells (docs/problems.md).
type Facts struct {
	Suspend       bool
	Interval      time.Duration
	Ready         ingest.CondFact
	Stalled       ingest.CondFact
	Reconciling   ingest.CondFact
	LastReconcile int64 // unix ms: lastHandledReconcileAt, else the Ready transition (0 unknown)
	DependsOn     []DepRef
}

func condFact(o *Object, typ string) ingest.CondFact {
	c := cond(o, typ)
	if c == nil {
		return ingest.CondFact{}
	}
	f := ingest.CondFact{Status: c.Status, Reason: c.Reason, Message: truncMsg(c.Message)}
	if !c.LastTransitionTime.IsZero() {
		f.Since = c.LastTransitionTime.UnixMilli()
	}
	return f
}

func factsOf(o *Object) *Facts {
	f := &Facts{Suspend: o.Suspend, Ready: condFact(o, "Ready"), Stalled: condFact(o, "Stalled"), Reconciling: condFact(o, "Reconciling"),
		LastReconcile: lastReconcileMs(o)}
	if d, err := time.ParseDuration(o.Interval); err == nil {
		f.Interval = d
	}
	for _, d := range o.DependsOn {
		ns := d.Namespace
		if ns == "" {
			ns = o.Namespace
		}
		f.DependsOn = append(f.DependsOn, DepRef{NS: ns, Name: d.Name})
	}
	return f
}

// FactsOf returns the Flux facts of a row (nil for other kinds).
func FactsOf(r *ingest.Row) *Facts {
	f, _ := r.Facts().(*Facts)
	return f
}
