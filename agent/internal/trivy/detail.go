package trivy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zyno-io/v8s/agent/internal/security"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var sevRank = map[string]int{"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 3, "UNKNOWN": 4, "NONE": 5}

func rank(s string) int {
	if r, ok := sevRank[strings.ToUpper(s)]; ok {
		return r
	}
	return 6
}

func init() {
	security.RegisterDetail(security.DetailProvider{
		Match: func(gvr string) bool {
			k := info(security.GroupOf(gvr), security.ResourceOf(gvr))
			return k != nil && k.Fam != famCompliance
		},
		Build: func(o *unstructured.Unstructured) (*protocol.ReportDetail, error) {
			return BuildDetail(o)
		},
	})
}

func strOf(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func intOf(m map[string]any, k string) int {
	switch x := m[k].(type) {
	case int64:
		return int(x)
	case float64:
		return int(x)
	case int:
		return x
	}
	return 0
}

func summaryOf(report map[string]any) map[string]int {
	sum, _ := report["summary"].(map[string]any)
	out := map[string]int{}
	for _, k := range []string{"critical", "high", "medium", "low", "unknown"} {
		if v, ok := sum[k+"Count"]; ok {
			_ = v
			out[k] = intOf(sum, k+"Count")
		}
	}
	return out
}

// BuildDetail decodes a full Trivy report (VulnerabilityReport, ConfigAuditReport, ExposedSecretReport and the
// RBAC / infra assessments) into its rows. It is only called when a user opens one report.
func BuildDetail(o *unstructured.Unstructured) (*protocol.ReportDetail, error) {
	report, _ := o.Object["report"].(map[string]any)
	if report == nil {
		return nil, fmt.Errorf("%s %s has no report", o.GetKind(), o.GetName())
	}
	d := &protocol.ReportDetail{Source: protocol.SourceTrivy, Title: o.GetName(), ResourceVersion: o.GetResourceVersion(), Summary: summaryOf(report)}
	switch o.GetKind() {
	case "VulnerabilityReport", "ClusterVulnerabilityReport":
		d.Kind = "vulnerabilities"
		vs, _ := report["vulnerabilities"].([]any)
		d.Vulns = make([]protocol.VulnFinding, 0, len(vs))
		for _, v := range vs {
			m, _ := v.(map[string]any)
			link := strOf(m, "primaryLink")
			if link == "" {
				if ls, ok := m["links"].([]any); ok && len(ls) > 0 {
					link, _ = ls[0].(string)
				}
			}
			d.Vulns = append(d.Vulns, protocol.VulnFinding{ID: strOf(m, "vulnerabilityID"), Severity: strOf(m, "severity"), Package: strOf(m, "resource"),
				Installed: strOf(m, "installedVersion"), Fixed: strOf(m, "fixedVersion"), Title: strOf(m, "title"), Link: link})
		}
		sort.SliceStable(d.Vulns, func(i, j int) bool {
			a, b := d.Vulns[i], d.Vulns[j]
			if ra, rb := rank(a.Severity), rank(b.Severity); ra != rb {
				return ra < rb
			}
			return a.ID < b.ID
		})
		d.Total = len(d.Vulns)
	case "ExposedSecretReport":
		d.Kind = "secrets"
		ss, _ := report["secrets"].([]any)
		for _, s := range ss {
			m, _ := s.(map[string]any)
			d.Secrets = append(d.Secrets, protocol.SecretFinding{RuleID: strOf(m, "ruleID"), Severity: strOf(m, "severity"), Title: strOf(m, "title"),
				Category: strOf(m, "category"), Target: strOf(m, "target"), Match: strOf(m, "match")})
		}
		sort.SliceStable(d.Secrets, func(i, j int) bool { return rank(d.Secrets[i].Severity) < rank(d.Secrets[j].Severity) })
		d.Total = len(d.Secrets)
	default: // ConfigAuditReport, RbacAssessmentReport, InfraAssessmentReport and the cluster variants
		d.Kind = "checks"
		cs, _ := report["checks"].([]any)
		for _, c := range cs {
			m, _ := c.(map[string]any)
			if ok, _ := m["success"].(bool); ok {
				continue // only failing checks are shown
			}
			var msgs []string
			if ms, ok := m["messages"].([]any); ok {
				for _, x := range ms {
					if s, ok := x.(string); ok && s != "" {
						msgs = append(msgs, s)
					}
				}
			}
			d.Checks = append(d.Checks, protocol.ConfigCheckResult{Source: protocol.SourceTrivy, ID: strOf(m, "checkID"), Title: strOf(m, "title"),
				Severity: strOf(m, "severity"), Category: strOf(m, "category"), Description: strOf(m, "description"), Messages: msgs})
		}
		sort.SliceStable(d.Checks, func(i, j int) bool { return rank(d.Checks[i].Severity) < rank(d.Checks[j].Severity) })
		d.Total = len(d.Checks)
	}
	return d, nil
}
