//go:build e2e

package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	_ "github.com/zyno-io/v8s/agent/internal/kubescape"
	"github.com/zyno-io/v8s/agent/internal/kyverno"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/security"
	"github.com/zyno-io/v8s/agent/internal/trivy"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// TestSecurityIntegrations runs the Kyverno and Trivy modules against the e2e cluster: Kyverno CEL policy
// `require-team-label` (Audit) fails every Deployment without a team label, Trivy Operator has reports for the
// cluster's images. Kubescape's summaries are empty there and are not asserted on.
func TestSecurityIntegrations(t *testing.T) {
	path := os.Getenv("V8S_E2E_KUBECONFIG")
	if path == "" {
		t.Skip("V8S_E2E_KUBECONFIG not set")
	}
	_ = os.Setenv("KUBE_FEATURE_WatchListClient", "true")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	src := &cluster.KubeSource{Loader: kube.Loader{Path: path}}
	id := "kind-v8s-e2e"
	found := false
	for _, c := range src.Clusters() {
		found = found || c.ID == id
	}
	if !found {
		t.Skipf("context %s not in %s", id, path)
	}
	eng := view.NewEngine()
	go eng.Run(ctx)
	hub := problems.NewHub(problems.HubConfig{})
	m := cluster.NewManager(ctx, src, eng, nil)
	m.SetProblems(hub)
	defer m.Close()
	if _, err := m.Select([]string{id}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cluster ready", 60*time.Second, func() bool {
		st := m.States().States
		return len(st) == 1 && st[0].Phase == protocol.PhaseReady
	})

	// views load: Kyverno policies (mode B), policy reports and Trivy reports (curated Table mode)
	open := func(gvr string) *view.Sub {
		t.Helper()
		mod, err := m.ResolveModule(ctx, gvr, nil)
		if err != nil {
			t.Fatalf("%s: %v", gvr, err)
		}
		scope := m.OpenScope(gvr, nil)
		t.Cleanup(scope.Close)
		sub, err := eng.Subscribe(protocol.ViewQuery{GVR: gvr}, 1, protocol.Window{Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sub.Close)
		t.Logf("%s: %d columns", gvr, len(mod.Columns()))
		return sub
	}
	policies := open("policies.kyverno.io/v1/validatingpolicies")
	reports := open("wgpolicyk8s.io/v1alpha2/policyreports")
	vulns := open("aquasecurity.github.io/v1alpha1/vulnerabilityreports")
	waitFor(t, "kyverno and trivy views", 60*time.Second, func() bool {
		eng.Pump()
		return policies.Total() > 0 && reports.Total() > 0 && vulns.Total() > 0
	})
	t.Logf("policies=%d policyreports=%d vulnerabilityreports=%d", policies.Total(), reports.Total(), vulns.Total())

	// the policy mode column is computed from spec.validationActions (mode B): require-team-label audits
	var mode string
	m.ScanKind(id, "policies.kyverno.io/v1/validatingpolicies", func(r *ingest.Row) bool {
		if r.Name() == "require-team-label" {
			mode = ingest.Lookup("policies.kyverno.io/v1/validatingpolicies").StrOf(r, "mode")
			return false
		}
		return true
	})
	if mode != kyverno.ModeAudit {
		t.Fatalf("require-team-label mode = %q, want Audit", mode)
	}

	// problems: policy.fail for Deployments without a team label, vuln.critical for an image
	var fails, vulnProbs []problems.Problem
	waitFor(t, "policy.fail and vuln.critical problems", 90*time.Second, func() bool {
		fails, vulnProbs = nil, nil
		for _, p := range hub.Engine(id).Problems() {
			switch p.Class {
			case "policy.fail":
				fails = append(fails, p)
			case "vuln.critical":
				vulnProbs = append(vulnProbs, p)
			}
		}
		return len(fails) > 0 && len(vulnProbs) > 0
	})
	sawDeployment := false
	for _, p := range fails {
		sawDeployment = sawDeployment || (p.Subject.Kind == "Deployment" && p.Source == "kyverno")
	}
	if !sawDeployment {
		t.Fatalf("policy.fail: want a Deployment problem, got %+v", fails)
	}
	if !strings.Contains(vulnProbs[0].Title, "critical") || vulnProbs[0].Source != "trivy" {
		t.Fatalf("vuln.critical: %+v", vulnProbs[0])
	}
	t.Logf("policy.fail=%d vuln.critical=%d (e.g. %q)", len(fails), len(vulnProbs), vulnProbs[0].Title)

	svc := security.New(security.Config{
		Conn:     func(c string) (kube.Conn, error) { return src.Backend(c) },
		Scan:     m.ScanKind,
		Clusters: func() []string { return []string{id} },
		Route: func(ctx context.Context, c, verb, gvr, sub, ns, name string) (*ops.Routed, error) {
			be, err := src.Backend(c)
			return &ops.Routed{ContextID: c, Conn: be}, err
		},
	})
	imgs, err := svc.Images(ctx, protocol.SecurityImagesParams{})
	if err != nil || len(imgs.Images) == 0 || imgs.Images[0].Critical == 0 {
		t.Fatalf("security.images: %+v %v", imgs, err)
	}
	t.Logf("top image %s: %d critical, %d high, %d workloads", imgs.Images[0].Image, imgs.Images[0].Critical, imgs.Images[0].High, imgs.Images[0].Workloads)

	// security.reportDetail: one full VulnerabilityReport as rows
	vgvr := "aquasecurity.github.io/v1alpha1/vulnerabilityreports"
	var name, ns string
	m.ScanKind(id, vgvr, func(r *ingest.Row) bool {
		if mod := ingest.Lookup(vgvr); mod.IntOf(r, trivy.ColCritical) > 0 {
			name, ns = r.Name(), r.GetNamespace()
			return false
		}
		return true
	})
	if name == "" {
		t.Fatal("no vulnerability report with criticals")
	}
	d, err := svc.ReportDetail(ctx, protocol.ReportDetailParams{Ref: protocol.ObjectRef{Cluster: id, GVR: vgvr, Namespace: ns, Name: name}, Limit: 50})
	if err != nil || d.Kind != "vulnerabilities" || d.Total == 0 || len(d.Vulns) == 0 {
		t.Fatalf("reportDetail: %+v %v", d, err)
	}
	if d.Vulns[0].Severity != "CRITICAL" || d.Vulns[0].ID == "" || d.Vulns[0].Package == "" {
		t.Fatalf("first row should be the worst: %+v", d.Vulns[0])
	}
	t.Logf("%s/%s: %d CVEs, first %s %s in %s", ns, name, d.Total, d.Vulns[0].ID, d.Vulns[0].Severity, d.Vulns[0].Package)

	// a policy report body is fetched lazily too
	var rname, rns string
	m.ScanKind(id, "wgpolicyk8s.io/v1alpha2/policyreports", func(r *ingest.Row) bool {
		if ingest.Lookup("wgpolicyk8s.io/v1alpha2/policyreports").IntOf(r, kyverno.ColFail) > 0 {
			rname, rns = r.Name(), r.GetNamespace()
			return false
		}
		return true
	})
	pd, err := svc.ReportDetail(ctx, protocol.ReportDetailParams{Ref: protocol.ObjectRef{Cluster: id, GVR: "wgpolicyk8s.io/v1alpha2/policyreports", Namespace: rns, Name: rname}})
	if err != nil || pd.Kind != "policyResults" || len(pd.Results) == 0 || pd.Results[0].Policy == "" {
		t.Fatalf("policy report detail: %+v %v", pd, err)
	}

	// the exception template names the violated policy and resource
	tpl, err := kyverno.ExceptionTemplate(protocol.KyvernoExceptionTemplateParams{
		Policy:   protocol.ObjectRef{Cluster: id, GVR: "policies.kyverno.io/v1/validatingpolicies", Name: "require-team-label"},
		Resource: protocol.KyvernoResourceRef{Kind: "Deployment", Namespace: "payments", Name: "catalog-api"}})
	if err != nil || !strings.Contains(tpl.YAML, "require-team-label") {
		t.Fatalf("exception template: %+v %v", tpl, err)
	}
}
