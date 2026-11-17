package trivy

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/problems/ptest"
	"github.com/zyno-io/v8s/agent/internal/security"
	"github.com/zyno-io/v8s/agent/internal/security/sectest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	vulnGVR   = Group + "/" + Version + "/vulnerabilityreports"
	secretGVR = Group + "/" + Version + "/exposedsecretreports"
	cfgGVR    = Group + "/" + Version + "/configauditreports"
	rbacGVR   = Group + "/" + Version + "/rbacassessmentreports"
)

func load(t *testing.T, gvr, file string) []*ingest.Row {
	t.Helper()
	b, err := os.ReadFile("../security/testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := ingest.DecodeTable(b)
	if err != nil {
		t.Fatal(err)
	}
	m := ingest.Lookup(gvr)
	var out []*ingest.Row
	for i := range tbl.Rows {
		out = append(out, m.RowFromTable(tbl.Cols, &tbl.Rows[i]))
	}
	return out
}

// Table-mode report counts: a recorded Table response of a real Trivy Operator install becomes rows with the
// repository, tag, severity counts (priority-1 printer columns) and the workload from the labels; no
// vulnerability array is involved.
func TestVulnerabilityReportTableCounts(t *testing.T) {
	rows := load(t, vulnGVR, "trivy_vuln_table.json")
	m := ingest.Lookup(vulnGVR)
	if len(rows) != 2 {
		t.Fatalf("rows %d", len(rows))
	}
	r := rows[0]
	if m.StrOf(r, ColRepository) == "" || m.StrOf(r, ColTag) == "" || m.StrOf(r, ColScanner) != "Trivy" {
		t.Fatalf("repository/tag/scanner: %q %q %q", m.StrOf(r, ColRepository), m.StrOf(r, ColTag), m.StrOf(r, ColScanner))
	}
	if m.IntOf(r, ColCritical) <= 0 || m.IntOf(r, ColHigh) <= 0 || m.IntOf(r, ColUnknown) <= 0 {
		t.Fatalf("counts %d %d %d", m.IntOf(r, ColCritical), m.IntOf(r, ColHigh), m.IntOf(r, ColUnknown))
	}
	if m.StrOf(r, ColKind) != "ReplicaSet" || m.StrOf(r, ColWorkload) == "" || m.StrOf(r, ColContainer) == "" {
		t.Fatalf("labels: %q %q %q", m.StrOf(r, ColKind), m.StrOf(r, ColWorkload), m.StrOf(r, ColContainer))
	}
	if r.Tone() != ingest.ToneDanger {
		t.Fatalf("tone %d", r.Tone())
	}
	if w := subjectOf(m, r); w.Kind != "Deployment" || w.NS != "flux-system" {
		t.Fatalf("a ReplicaSet rolls up to its Deployment: %+v", w)
	}
}

func rbacRow(t *testing.T) *ingest.Row { return load(t, rbacGVR, "trivy_rbac_table.json")[0] }

func TestRBACSubjectFallsBackToOwner(t *testing.T) {
	m := ingest.Lookup(rbacGVR)
	r := rbacRow(t)
	w := subjectOf(m, r)
	if w.Name == "" || w.Name == r.Name() && r.OwnerName().Value() != "" && w.Name != r.OwnerName().Value() {
		t.Fatalf("rbac subject %+v owner %q", w, r.OwnerName().Value())
	}
}

func vulnRow(gvr, name, ns, kind, wl, container, repo, tag string, c, h, m, l, u int64, age time.Time) *ingest.Row {
	mod := ingest.Lookup(gvr)
	tr := &ingest.TableRow{UID: "uid-" + name, Name: name, Namespace: ns, RV: "1", Created: age.UnixMilli(),
		LabelsRaw: []byte(`{"` + LabelKind + `":"` + kind + `","` + LabelName + `":"` + wl + `","` + LabelContainer + `":"` + container + `"}`)}
	cols := []ingest.TableCol{{Name: "Name"}, {Name: "Repository"}, {Name: "Tag"}, {Name: "Scanner"}, {Name: "Age", Type: "date"},
		{Name: "Critical", Type: "integer", Priority: 1}, {Name: "High", Type: "integer", Priority: 1}, {Name: "Medium", Type: "integer", Priority: 1},
		{Name: "Low", Type: "integer", Priority: 1}, {Name: "Unknown", Type: "integer", Priority: 1}}
	tr.Cells = []any{name, repo, tag, "Trivy", "1h", float64(c), float64(h), float64(m), float64(l), float64(u)}
	return mod.RowFromTable(cols, tr)
}

func TestVulnCriticalAggregatesPerImage(t *testing.T) {
	h := ptest.New(t, vulnGVR)
	now := h.Clk.Now()
	// the same image in three workloads (two namespaces) is one problem; a different image another
	h.Put(vulnGVR, vulnRow(vulnGVR, "rs-a-app", "n1", "ReplicaSet", "web-abc123", "app", "library/nginx", "1.25", 4, 31, 16, 4, 164, now.Add(-2*time.Hour)))
	h.Put(vulnGVR, vulnRow(vulnGVR, "rs-b-app", "n1", "ReplicaSet", "api-def456", "app", "library/nginx", "1.25", 4, 31, 16, 4, 164, now.Add(-time.Hour)))
	h.Put(vulnGVR, vulnRow(vulnGVR, "rs-c-app", "n2", "StatefulSet", "db", "app", "library/nginx", "1.25", 4, 30, 16, 4, 164, now.Add(-time.Hour)))
	h.Put(vulnGVR, vulnRow(vulnGVR, "rs-d-app", "n2", "StatefulSet", "cache", "app", "library/redis", "7", 1, 2, 0, 0, 0, now))
	h.Put(vulnGVR, vulnRow(vulnGVR, "rs-e-app", "n2", "StatefulSet", "clean", "app", "library/alpine", "3", 0, 5, 0, 0, 0, now))
	h.Pass()
	ps := h.ByClass("vuln.critical")
	if len(ps) != 2 {
		t.Fatalf("want 2 image problems, got %+v", ps)
	}
	var nginx, redis = ps[0], ps[1]
	if nginx.Subject.Name != "library/nginx:1.25" {
		nginx, redis = redis, nginx
	}
	if nginx.Title != "library/nginx:1.25 · 4 critical, 31 high · 3 workloads" || nginx.Count != 4 || nginx.Source != "trivy" {
		t.Fatalf("nginx: %+v", nginx)
	}
	if nginx.Since != now.Add(-2*time.Hour).UnixMilli() {
		t.Fatalf("since is the oldest report: %d", nginx.Since)
	}
	if redis.Title != "library/redis:7 · 1 critical, 2 high · 1 workload" {
		t.Fatalf("redis: %+v", redis)
	}
	if len(h.ByClass("vuln.high")) != 0 {
		t.Fatal("vuln.high is off by default")
	}
	// enable vuln.high: the alpine image (5 high, 0 critical) appears too
	h.Set.ProblemPrefs = map[string]any{"classes": map[string]any{"vuln.high": true}}
	h.Hub.SettingsChanged(h.Set)
	h.Pass()
	if n := len(h.ByClass("vuln.high")); n != 3 {
		t.Fatalf("vuln.high: %d", n)
	}
	// deleting one of the three nginx workloads keeps the problem, with two workloads
	h.Del(vulnGVR, vulnRow(vulnGVR, "rs-a-app", "n1", "ReplicaSet", "web-abc123", "app", "library/nginx", "1.25", 4, 31, 16, 4, 164, now))
	h.Pass()
	for _, p := range h.ByClass("vuln.critical") {
		if p.Subject.Name == "library/nginx:1.25" && p.Title != "library/nginx:1.25 · 4 critical, 31 high · 2 workloads" {
			t.Fatalf("after delete: %q", p.Title)
		}
	}
}

func TestSecretAndConfigClasses(t *testing.T) {
	h := ptest.New(t, secretGVR, cfgGVR)
	now := h.Clk.Now()
	sec := ingest.Lookup(secretGVR)
	cols := []ingest.TableCol{{Name: "Name"}, {Name: "Repository"}, {Name: "Tag"}, {Name: "Scanner"}, {Name: "Age", Type: "date"}, {Name: "Critical", Type: "integer", Priority: 1},
		{Name: "High", Type: "integer", Priority: 1}, {Name: "Medium", Type: "integer", Priority: 1}, {Name: "Low", Type: "integer", Priority: 1}}
	h.Put(secretGVR, sec.RowFromTable(cols, &ingest.TableRow{UID: "s1", Name: "rs-x-app", Namespace: "n", RV: "1", Created: now.UnixMilli(),
		LabelsRaw: []byte(`{"` + LabelKind + `":"ReplicaSet","` + LabelName + `":"x-abc","` + LabelContainer + `":"app"}`),
		Cells:     []any{"rs-x-app", "r", "t", "Trivy", "1h", float64(1), float64(0), float64(0), float64(0)}}))
	cfg := ingest.Lookup(cfgGVR)
	ccols := []ingest.TableCol{{Name: "Name"}, {Name: "Scanner"}, {Name: "Age", Type: "date"}, {Name: "Critical", Type: "integer", Priority: 1}, {Name: "High", Type: "integer", Priority: 1},
		{Name: "Medium", Type: "integer", Priority: 1}, {Name: "Low", Type: "integer", Priority: 1}}
	h.Put(cfgGVR, cfg.RowFromTable(ccols, &ingest.TableRow{UID: "c1", Name: "rs-x", Namespace: "n", RV: "1", Created: now.UnixMilli(),
		LabelsRaw: []byte(`{"` + LabelKind + `":"ReplicaSet","` + LabelName + `":"x-abc"}`), Cells: []any{"rs-x", "Trivy", "1h", float64(2), float64(3), float64(0), float64(0)}}))
	h.Pass()
	if ps := h.ByClass("secret.exposed"); len(ps) != 1 || ps[0].Subject.Kind != "Deployment" || ps[0].Subject.Name != "x" || ps[0].Count != 1 {
		t.Fatalf("secret.exposed: %+v", ps)
	}
	// docs/integrations.md: misconfig.critical is on by default, misconfig.high is off
	if ps := h.ByClass("misconfig.critical"); len(ps) != 1 || ps[0].Count != 2 {
		t.Fatalf("misconfig.critical must be on by default: %+v", ps)
	}
	if len(h.ByClass("misconfig.high")) != 0 {
		t.Fatal("misconfig.high is off by default")
	}
	h.Set.ProblemPrefs = map[string]any{"classes": map[string]any{"misconfig.critical": true, "misconfig.high": true}}
	h.Hub.SettingsChanged(h.Set)
	h.Pass()
	if ps := h.ByClass("misconfig.critical"); len(ps) != 1 || ps[0].Count != 2 {
		t.Fatalf("misconfig.critical: %+v", ps)
	}
	if ps := h.ByClass("misconfig.high"); len(ps) != 1 || ps[0].Count != 3 {
		t.Fatalf("misconfig.high: %+v", ps)
	}
}

func TestGatedKindsAreNotBaseline(t *testing.T) {
	for _, gvr := range []string{rbacGVR, Group + "/" + Version + "/clusterconfigauditreports", Group + "/" + Version + "/clustercompliancereports"} {
		m := ingest.Lookup(gvr)
		if m == nil || m.Tier0 || m.GateClass == "" || !m.Optional {
			t.Errorf("%s: want a gated optional module: %+v", gvr, m)
		}
	}
	for _, gvr := range []string{vulnGVR, secretGVR, cfgGVR} {
		if m := ingest.Lookup(gvr); m == nil || !m.Tier0 || !m.Optional {
			t.Errorf("%s: want a tier-0 optional module", gvr)
		}
	}
}

func fullReport(kind string, report map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": Group + "/v1alpha1", "kind": kind,
		"metadata": map[string]any{"name": "r", "namespace": "n", "resourceVersion": "9"}, "report": report}}
}

func TestBuildVulnerabilityDetail(t *testing.T) {
	var vulns []any
	for _, v := range []struct{ id, sev string }{{"CVE-2", "MEDIUM"}, {"CVE-1", "CRITICAL"}, {"CVE-3", "HIGH"}, {"CVE-0", "CRITICAL"}} {
		vulns = append(vulns, map[string]any{"vulnerabilityID": v.id, "severity": v.sev, "resource": "openssl", "installedVersion": "1", "fixedVersion": "2", "title": "t", "primaryLink": "https://x/" + v.id})
	}
	d, err := BuildDetail(fullReport("VulnerabilityReport", map[string]any{"vulnerabilities": vulns, "summary": map[string]any{"criticalCount": int64(2), "highCount": int64(1)}}))
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != "vulnerabilities" || d.Total != 4 || d.Vulns[0].ID != "CVE-0" || d.Vulns[1].ID != "CVE-1" || d.Vulns[2].Severity != "HIGH" || d.Vulns[0].Link != "https://x/CVE-0" {
		t.Fatalf("%+v", d)
	}
	if d.Summary["critical"] != 2 || d.Summary["high"] != 1 {
		t.Fatalf("summary %v", d.Summary)
	}
	c, _ := BuildDetail(fullReport("ConfigAuditReport", map[string]any{"checks": []any{
		map[string]any{"checkID": "A", "severity": "LOW", "success": false, "messages": []any{"m"}},
		map[string]any{"checkID": "B", "severity": "HIGH", "success": true},
		map[string]any{"checkID": "C", "severity": "CRITICAL", "success": false}}}))
	if c.Kind != "checks" || c.Total != 2 || c.Checks[0].ID != "C" {
		t.Fatalf("checks: %+v", c)
	}
}

// reportDetail through the Service: a large report is read in chunks, the decoded report cached.
func TestReportDetailChunked(t *testing.T) {
	var vulns []any
	for i := 0; i < 700; i++ {
		vulns = append(vulns, map[string]any{"vulnerabilityID": "CVE-" + string(rune('A'+i%26)), "severity": "HIGH"})
	}
	obj := fullReport("VulnerabilityReport", map[string]any{"vulnerabilities": vulns})
	obj.SetName("big")
	obj.SetNamespace("n")
	gvrObj := schema.GroupVersionResource{Group: Group, Version: "v1alpha1", Resource: "vulnerabilityreports"}
	env := sectest.New(map[schema.GroupVersionResource]string{gvrObj: "VulnerabilityReportList"}, obj)
	svc := security.New(env.Cfg)
	h := svc.Handlers()["security.reportDetail"]
	call := func(off int) *protocol.ReportDetail {
		res, err := h(context.Background(), func(v any) error {
			*(v.(*protocol.ReportDetailParams)) = protocol.ReportDetailParams{Ref: protocol.ObjectRef{Cluster: "c", GVR: vulnGVR, Namespace: "n", Name: "big"}, Offset: off, Limit: 300}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return res.(*protocol.ReportDetail)
	}
	a, b, c := call(0), call(300), call(600)
	if a.Total != 700 || len(a.Vulns) != 300 || len(b.Vulns) != 300 || len(c.Vulns) != 100 || c.Offset != 600 {
		t.Fatalf("chunks %d %d %d total %d", len(a.Vulns), len(b.Vulns), len(c.Vulns), a.Total)
	}
	if env.Gets != 1 {
		t.Fatalf("report fetched %d times for 3 chunks", env.Gets)
	}
}

// trivy.rescan deletes the report (and refuses anything that is not a Trivy report).
func TestRescanDeletesReport(t *testing.T) {
	obj := fullReport("VulnerabilityReport", map[string]any{})
	obj.SetName("rep")
	obj.SetNamespace("n")
	gvrObj := schema.GroupVersionResource{Group: Group, Version: "v1alpha1", Resource: "vulnerabilityreports"}
	env := sectest.New(map[schema.GroupVersionResource]string{gvrObj: "VulnerabilityReportList"}, obj)
	res, err := Rescan(context.Background(), &env.Cfg, protocol.TrivyRescanParams{Ref: protocol.ObjectRef{Cluster: "c", GVR: vulnGVR, Namespace: "n", Name: "rep"}})
	if err != nil || res.UsedContext != "c" {
		t.Fatalf("%v %v", res, err)
	}
	if _, err := env.Dyn.Resource(gvrObj).Namespace("n").Get(context.Background(), "rep", metav1.GetOptions{}); err == nil {
		t.Fatal("the report still exists")
	}
	if _, err := Rescan(context.Background(), &env.Cfg, protocol.TrivyRescanParams{Ref: protocol.ObjectRef{Cluster: "c", GVR: "apps/v1/deployments", Namespace: "n", Name: "x"}}); err == nil {
		t.Fatal("rescan of a Deployment must be refused")
	}
}

// Only kinds the operator regenerates may be deleted: a ClusterComplianceReport is configuration.
func TestRescanRefusesNonRegeneratedKinds(t *testing.T) {
	obj := fullReport("ClusterComplianceReport", map[string]any{})
	obj.SetName("nsa")
	gvrObj := schema.GroupVersionResource{Group: Group, Version: "v1alpha1", Resource: "clustercompliancereports"}
	env := sectest.New(map[schema.GroupVersionResource]string{gvrObj: "ClusterComplianceReportList"}, obj)
	for _, res := range []string{"clustercompliancereports", "somethingreports"} {
		_, err := Rescan(context.Background(), &env.Cfg, protocol.TrivyRescanParams{Ref: protocol.ObjectRef{Cluster: "c", GVR: Group + "/v1alpha1/" + res, Name: "nsa"}})
		if err == nil || !strings.Contains(err.Error(), "bad_request") {
			t.Fatalf("%s: %v", res, err)
		}
	}
	if _, err := env.Dyn.Resource(gvrObj).Namespace("n").Get(context.Background(), "nsa", metav1.GetOptions{}); err != nil {
		t.Fatalf("the compliance report was deleted: %v", err)
	}
}

var _ = kube.Conn(nil)
var _ = ops.NewError
