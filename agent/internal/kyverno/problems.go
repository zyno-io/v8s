package kyverno

import (
	"fmt"
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/security"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// Report GRs (problems.GR form) the policy.fail and policy.error classes read; every one is optional, a cluster
// serves wgpolicyk8s.io, openreports.io or both.
var reportGRs = []string{
	GroupWG + "/policyreports", GroupWG + "/clusterpolicyreports",
	GroupOR + "/reports", GroupOR + "/clusterreports",
}

// policyGRs are the policy kinds policy.not-ready reads.
func policyGRs() []string {
	var out []string
	for _, k := range policyKinds {
		if k.Kind != "PolicyException" {
			out = append(out, k.Group+"/"+k.Resource)
		}
	}
	return out
}

func scopeSubject(c *problems.Ctx, k problems.Key, r *ingest.Row) (problems.Subject, bool) {
	m := c.Mod(k.GR)
	if m == nil {
		return problems.Subject{}, false
	}
	kind, name := m.StrOf(r, ColScopeKind), m.StrOf(r, ColScopeName)
	if kind == "" || name == "" {
		return problems.Subject{}, false
	}
	g, gvr := security.KindGVR(kind)
	return problems.Subject{Group: g, Kind: kind, NS: r.GetNamespace(), Name: name, GVR: gvr}, true
}

// resultClass builds policy.fail / policy.error: one problem per resource from the report's count (the report's name
// is the resource uid, so the scope kind and name identify the subject, and the id has no discriminator).
func resultClass(class string, sev problems.Severity, title, col, noun string) {
	problems.Register(&problems.Detector{Class: class, Family: problems.FamPolicy, Default: true, Severity: sev, Title: title,
		Inputs: reportGRs, Optional: reportGRs,
		Evaluate: func(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
			m := c.Mod(k.GR)
			if m == nil {
				return nil
			}
			n := int(m.IntOf(r, col))
			if n == 0 {
				return nil
			}
			sub, ok := scopeSubject(c, k, r)
			if !ok {
				return nil
			}
			plural := noun
			if n != 1 {
				plural += "s"
			}
			return []problems.Draft{{Subject: sub, Source: protocol.SourceKyverno, Count: n, Since: r.CreatedMs(),
				Title:    fmt.Sprintf("%s %s: %d %s", sub.Kind, sub.Name, n, plural),
				Message:  "open the report for the failing policies and rules",
				Evidence: []problems.Ref{{Group: k.GR, Kind: m.Kind, NS: r.GetNamespace(), Name: r.Name()}}}}
		}})
}

func init() {
	resultClass("policy.fail", problems.SevWarning, "Resources failing policies", ColFail, "policy failure")
	resultClass("policy.error", problems.SevError, "Resources with policy errors", ColError, "policy error")

	pg := policyGRs()
	problems.Register(&problems.Detector{Class: "policy.not-ready", Family: problems.FamPolicy, Default: true, Severity: problems.SevWarning, Title: "Policies that are not ready",
		Inputs: pg, Optional: pg,
		Evaluate: func(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
			f, _ := r.Facts().(*PolicyFacts)
			if f == nil || f.Ready.Status != "False" {
				return nil
			}
			sub := c.SubjectOf(k.GR, r)
			return []problems.Draft{{Subject: sub, Source: protocol.SourceKyverno, Title: fmt.Sprintf("%s %s is not ready", sub.Kind, sub.Name),
				Message: f.Message, Since: f.Ready.Since, Grace: 2 * time.Minute}}
		}})

	problems.Register(&problems.Detector{Class: "policy.admission-denied", Family: problems.FamPolicy, Default: true, Severity: problems.SevWarning, Title: "Admission requests denied by policy",
		Inputs: []string{problems.GREvents},
		Evaluate: func(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
			if c.Col(k.GR, r, "reason").S.Value() != "PolicyViolation" {
				return nil
			}
			// Kyverno emits PolicyViolation for audit-mode and background findings too; only an enforced policy
			// that stopped a request says "(blocked)" (older versions: "was blocked").
			if !strings.Contains(strings.ToLower(c.Col(k.GR, r, "message").S.Value()), "blocked") {
				return nil
			}
			kind, name, ok := strings.Cut(c.Col(k.GR, r, "object").S.Value(), "/")
			if !ok {
				return nil
			}
			last := c.Col(k.GR, r, "lastSeen").N
			g, gvr := security.KindGVR(kind)
			return []problems.Draft{{Subject: problems.Subject{Group: g, Kind: kind, NS: r.GetNamespace(), Name: name, GVR: gvr}, Source: protocol.SourceKyverno,
				Title:   fmt.Sprintf("Admission denied for %s %s", kind, name),
				Message: c.Col(k.GR, r, "message").S.Value(), Count: int(max(c.Col(k.GR, r, "count").N, 1)), Since: last, TTL: time.Hour}}
		}})
}
