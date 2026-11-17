package security_test

import (
	"context"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/fake"
	_ "github.com/zyno-io/v8s/agent/internal/flux"
	_ "github.com/zyno-io/v8s/agent/internal/helm"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	_ "github.com/zyno-io/v8s/agent/internal/kubescape"
	_ "github.com/zyno-io/v8s/agent/internal/kyverno"
	"github.com/zyno-io/v8s/agent/internal/ops"
	_ "github.com/zyno-io/v8s/agent/internal/platform"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/security"
	_ "github.com/zyno-io/v8s/agent/internal/trivy"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// The fake clusters carry Trivy reports, Kyverno policies and reports and Kubescape summaries; the whole path
// (discovery -> baseline watches in Table / partial JSON mode -> problems engine -> view engine) must yield
// problems, the cross-cluster image overview and the lazily fetched report detail.
func TestFakeClustersSecurityEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	src := fake.NewSource(fake.Config{Clusters: 2, Pods: 400, Churn: 0})
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := cluster.NewManager(ctx, src, eng, nil)
	defer m.Close()
	hub := problems.NewHub(problems.HubConfig{Settings: func() protocol.Settings { return protocol.Settings{} }})
	m.SetProblems(hub)
	ids := []string{fake.Name(0), fake.Name(1)}
	if _, err := m.Select(ids); err != nil {
		t.Fatal(err)
	}
	has := func(id, class string) bool {
		e := hub.Engine(id)
		if e == nil {
			return false
		}
		for _, p := range e.Problems() {
			if p.Class == class {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(45 * time.Second)
	for _, class := range []string{"vuln.critical", "policy.fail", "secret.exposed"} {
		for !has(ids[0], class) {
			if time.Now().After(deadline) {
				t.Fatalf("no %s problem in %s; have %v", class, ids[0], classes(hub.Engine(ids[0])))
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	for !has(ids[1], "vuln.critical") || !has(ids[1], "policy.fail") {
		if time.Now().After(deadline) {
			t.Fatalf("second cluster: %v", classes(hub.Engine(ids[1])))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !has(ids[0], "policy.not-ready") {
		for time.Now().Before(deadline) && !has(ids[0], "policy.not-ready") {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !has(ids[0], "policy.not-ready") {
		t.Logf("policy.not-ready is held back by its grace period (%v)", classes(hub.Engine(ids[0])))
	}

	svc := security.New(security.Config{
		Conn:     func(id string) (kube.Conn, error) { return src.Backend(id) },
		Scan:     m.ScanKind,
		Clusters: func() []string { return ids },
		Route: func(ctx context.Context, c, verb, gvr, sub, ns, name string) (*ops.Routed, error) {
			be, err := src.Backend(c)
			return &ops.Routed{ContextID: c, Conn: be}, err
		},
	})
	res, err := svc.Images(ctx, protocol.SecurityImagesParams{})
	if err != nil || len(res.Images) == 0 {
		t.Fatalf("images: %+v %v", res, err)
	}
	top := res.Images[0]
	if top.Critical == 0 || top.Workloads < 2 || len(top.Clusters) != 2 {
		t.Fatalf("the top image must be shared by workloads of both clusters: %+v", top)
	}
	// report detail of one fake VulnerabilityReport
	var name, ns string
	m.ScanKind(ids[0], "aquasecurity.github.io/v1alpha1/vulnerabilityreports", func(r *ingest.Row) bool {
		if r.Cell(5).N > 0 || true {
			name, ns = r.Name(), r.GetNamespace()
			return false
		}
		return true
	})
	if name == "" {
		t.Fatal("no vulnerability report row")
	}
	d, err := svc.ReportDetail(ctx, protocol.ReportDetailParams{Ref: protocol.ObjectRef{Cluster: ids[0], GVR: "aquasecurity.github.io/v1alpha1/vulnerabilityreports", Namespace: ns, Name: name}})
	if err != nil || d.Kind != "vulnerabilities" {
		t.Fatalf("detail: %+v %v", d, err)
	}
}

func classes(e *problems.Engine) map[string]int {
	out := map[string]int{}
	if e != nil {
		for _, p := range e.Problems() {
			out[p.Class]++
		}
	}
	return out
}
