package kubescape

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/security"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// maxDoc bounds a rendered document (an ApplicationProfile of a busy workload can be large).
const maxDoc = 1 << 20

var rankOf = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "negligible": 4, "unknown": 5}

func rank(s string) int {
	if r, ok := rankOf[strings.ToLower(s)]; ok {
		return r
	}
	return 6
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func registerDetail() {
	security.RegisterDetail(security.DetailProvider{
		Match: func(gvr string) bool {
			return security.GroupOf(gvr) == Group && detailKind(security.ResourceOf(gvr)) != ""
		},
		Fetch: fetch,
		Build: build,
	})
}

func detailKind(res string) string {
	switch res {
	case ResVulnSummary, "vulnerabilitymanifests":
		return "vuln"
	case ResConfigSummary, "workloadconfigurationscans":
		return "config"
	case "applicationprofiles", "networkneighborhoods", "containerprofiles":
		return "doc"
	}
	return ""
}

// fetch reads the object a detail request is about. A summary points to its full manifest (spec.vulnerabilitiesRef);
// a configuration scan summary has a WorkloadConfigurationScan of the same name.
func fetch(ctx context.Context, cfg *security.Config, ref protocol.ObjectRef) (*unstructured.Unstructured, error) {
	obj, err := security.GetObject(ctx, cfg, ref.Cluster, ref.GVR, ref.Namespace, ref.Name)
	if err != nil {
		return nil, err
	}
	g, _ := security.ParseGVR(ref.GVR)
	switch g.Resource {
	case ResVulnSummary:
		name, ns := "", ref.Namespace
		for _, k := range []string{"all", "relevant"} {
			if r, ok, _ := unstructured.NestedMap(obj.Object, "spec", "vulnerabilitiesRef", k); ok && str(r, "name") != "" {
				name = str(r, "name")
				if n := str(r, "namespace"); n != "" {
					ns = n
				}
				break
			}
		}
		if name == "" {
			return nil, fmt.Errorf("summary %s has no vulnerabilitiesRef", ref.Name)
		}
		return security.GetObject(ctx, cfg, ref.Cluster, strings.Replace(ref.GVR, ResVulnSummary, "vulnerabilitymanifests", 1), ns, name)
	case ResConfigSummary:
		return security.GetObject(ctx, cfg, ref.Cluster, strings.Replace(ref.GVR, ResConfigSummary, "workloadconfigurationscans", 1), ref.Namespace, ref.Name)
	}
	return obj, nil
}

func build(o *unstructured.Unstructured) (*protocol.ReportDetail, error) {
	d := &protocol.ReportDetail{Source: protocol.SourceKubescape, Title: o.GetName(), ResourceVersion: o.GetResourceVersion()}
	switch o.GetKind() {
	case "VulnerabilityManifest":
		d.Kind = "vulnerabilities"
		d.Summary = map[string]int{}
		matches, _, _ := unstructured.NestedSlice(o.Object, "spec", "payload", "matches")
		for _, x := range matches {
			m, _ := x.(map[string]any)
			v, _ := m["vulnerability"].(map[string]any)
			art, _ := m["artifact"].(map[string]any)
			f := protocol.VulnFinding{ID: str(v, "id"), Severity: str(v, "severity"), Package: str(art, "name"), Installed: str(art, "version"),
				Title: trunc(str(v, "description"), 160), Link: str(v, "dataSource")}
			if urls, ok := v["urls"].([]any); ok && len(urls) > 0 && f.Link == "" {
				f.Link, _ = urls[0].(string)
			}
			if fix, ok := v["fix"].(map[string]any); ok {
				if vs, ok := fix["versions"].([]any); ok {
					var parts []string
					for _, s := range vs {
						if str, ok := s.(string); ok {
							parts = append(parts, str)
						}
					}
					f.Fixed = strings.Join(parts, ", ")
				}
			}
			d.Vulns = append(d.Vulns, f)
			d.Summary[strings.ToLower(f.Severity)]++
		}
		sort.SliceStable(d.Vulns, func(i, j int) bool {
			if a, b := rank(d.Vulns[i].Severity), rank(d.Vulns[j].Severity); a != b {
				return a < b
			}
			return d.Vulns[i].ID < d.Vulns[j].ID
		})
		d.Total = len(d.Vulns)
	case "WorkloadConfigurationScan":
		d.Kind = "checks"
		ctl, _, _ := unstructured.NestedMap(o.Object, "spec", "controls")
		ids := make([]string, 0, len(ctl))
		for id := range ctl {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			c, _ := ctl[id].(map[string]any)
			if st, _ := c["status"].(map[string]any); str(st, "status") == "passed" || str(st, "status") == "skipped" {
				continue
			}
			sv := str(c, "severity")
			if m, ok := c["severity"].(map[string]any); ok {
				sv = str(m, "severity")
			}
			chk := protocol.ConfigCheckResult{Source: protocol.SourceKubescape, ID: id, Title: str(c, "name"), Severity: sv}
			if fw, ok := c["frameworks"].([]any); ok {
				for _, x := range fw {
					if s, ok := x.(string); ok {
						chk.Frameworks = append(chk.Frameworks, s)
					}
				}
			}
			d.Checks = append(d.Checks, chk)
		}
		sort.SliceStable(d.Checks, func(i, j int) bool { return rank(d.Checks[i].Severity) < rank(d.Checks[j].Severity) })
		d.Total = len(d.Checks)
	default: // ApplicationProfile, NetworkNeighborhood, ContainerProfile
		d.Kind = "document"
		spec, _ := o.Object["spec"].(map[string]any)
		b, err := yaml.Marshal(spec)
		if err != nil {
			return nil, ops.NewError("internal", "%s", err.Error())
		}
		if len(b) > maxDoc {
			b = append(b[:maxDoc], []byte("\n# ... truncated\n")...)
		}
		d.Doc = string(b)
		d.Total = 1
	}
	return d, nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}
