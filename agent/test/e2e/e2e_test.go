//go:build e2e

// Package e2e runs the agent's real code paths against a real cluster. It is skipped unless
// V8S_E2E_KUBECONFIG points at a kubeconfig; the first context in it is used.
//
//	V8S_E2E_KUBECONFIG=/path/kubeconfig go test -tags e2e ./test/e2e/ -v
//
// The cluster needs a Deployment in namespace "payments" (V8S_E2E_DEPLOYMENT picks which; default: the first).
package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRealCluster(t *testing.T) {
	path := os.Getenv("V8S_E2E_KUBECONFIG")
	if path == "" {
		t.Skip("V8S_E2E_KUBECONFIG not set")
	}
	_ = os.Setenv("KUBE_FEATURE_WatchListClient", "true")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	src := &cluster.KubeSource{Loader: kube.Loader{Path: path}}
	cs := src.Clusters()
	if len(cs) == 0 {
		t.Fatal("no contexts in the kubeconfig")
	}
	id := cs[0].ID
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := cluster.NewManager(ctx, src, eng, nil)
	defer m.Close()
	svc := ops.New(ops.Config{Conn: func(id string) (kube.Conn, error) { return src.Backend(id) }, Links: m.Links, Resolve: m.Resolve})

	if _, err := m.Select([]string{id}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cluster ready", 60*time.Second, func() bool {
		st := m.States().States
		return len(st) == 1 && st[0].Phase == protocol.PhaseReady
	})

	open := func(gvr string) *view.Sub {
		t.Helper()
		mod, err := m.ResolveModule(ctx, gvr, nil)
		if err != nil {
			t.Fatalf("%s: %v", gvr, err)
		}
		scope := m.OpenScope(gvr, nil)
		t.Cleanup(scope.Close)
		sub, err := eng.Subscribe(protocol.ViewQuery{GVR: gvr}, 1, protocol.Window{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sub.Close)
		t.Logf("%s: columns %d", gvr, len(mod.Columns()))
		return sub
	}
	pods := open("v1/pods")
	deps := open("apps/v1/deployments")
	waitFor(t, "pods and deployments", 60*time.Second, func() bool { eng.Pump(); return pods.Total() > 0 && deps.Total() > 0 })
	t.Logf("pods=%d deployments=%d", pods.Total(), deps.Total())

	// a CRD view: the first non-built-in kind the cluster serves, else the CRD kind itself (also Table mode)
	crdGVR := "apiextensions.k8s.io/v1/customresourcedefinitions"
	for _, k := range m.Kinds() {
		if !k.Curated && k.Group != "" && !strings.HasSuffix(k.Group, ".k8s.io") && k.Group != "apps" && k.Group != "batch" {
			crdGVR = k.GVR
			break
		}
	}
	crds := open(crdGVR)
	waitFor(t, "CRD view synced", 30*time.Second, func() bool {
		eng.Pump()
		return crds.Total() > 0 || crdGVR != "apiextensions.k8s.io/v1/customresourcedefinitions"
	})
	t.Logf("crd view %s: %d rows", crdGVR, crds.Total())

	// Secrets are served in Table mode: columns from the server, values never transferred.
	secrets := open("v1/secrets")
	waitFor(t, "secrets synced", 30*time.Second, func() bool { eng.Pump(); return secrets.Total() > 0 })
	t.Logf("secrets: %d rows", secrets.Total())

	// find a deployment in payments
	name := os.Getenv("V8S_E2E_DEPLOYMENT")
	if name == "" {
		list, err := listDeployments(ctx, src, id)
		if err != nil || len(list) == 0 {
			t.Fatalf("no deployments in payments: %v", err)
		}
		name = list[0]
	}
	ref := protocol.ObjectRef{Cluster: id, GVR: "apps/v1/deployments", Namespace: "payments", Name: name}
	obj, err := svc.Get(ctx, protocol.ObjectGetParams{Cluster: id, GVR: ref.GVR, Namespace: ref.Namespace, Name: ref.Name})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(obj.YAML, "kind: Deployment") || obj.UID == "" || strings.Contains(obj.YAML, "managedFields") {
		t.Fatalf("object.get: %.200s", obj.YAML)
	}
	t.Logf("got %s uid=%s rv=%s managedBy=%d gitops=%v", name, obj.UID, obj.ResourceVersion, len(obj.ManagedBy), obj.GitOps)

	edited := strings.Replace(obj.YAML, "  labels:", "  labels:\n    v8s-e2e: dryrun", 1)
	if edited == obj.YAML {
		edited = strings.Replace(obj.YAML, "metadata:", "metadata:\n  labels:\n    v8s-e2e: dryrun", 1)
	}
	dr, err := svc.DryRun(ctx, protocol.ObjectDryRunParams{Ref: ref, YAML: edited, Mode: "update"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("dry run: ok=%v warnings=%v err=%v conflicts=%v", dr.OK, dr.Warnings, dr.Error, dr.Conflicts)
	if !dr.OK {
		t.Fatalf("dry run rejected: %+v", dr.Error)
	}

	cur := replicasOf(t, obj.YAML)
	scale := func(n int, id string) {
		res, err := svc.Action(ctx, protocol.ObjectActionParams{RequestID: id, Ref: ref, Action: "scale", Params: map[string]any{"replicas": n}})
		if err != nil {
			t.Fatalf("scale to %d: %v", n, err)
		}
		t.Logf("scale %d via %s rv=%s", n, res.UsedContext, res.ResourceVersion)
	}
	scale(cur+1, "e2e-up")
	defer scale(cur, "e2e-restore-deferred")
	got, _ := svc.Get(ctx, protocol.ObjectGetParams{Cluster: id, GVR: ref.GVR, Namespace: ref.Namespace, Name: ref.Name})
	if replicasOf(t, got.YAML) != cur+1 {
		t.Fatalf("replicas after scale up: %d", replicasOf(t, got.YAML))
	}
	scale(cur, "e2e-down")
	got, _ = svc.Get(ctx, protocol.ObjectGetParams{Cluster: id, GVR: ref.GVR, Namespace: ref.Namespace, Name: ref.Name})
	if replicasOf(t, got.YAML) != cur {
		t.Fatalf("replicas after scale back: %d", replicasOf(t, got.YAML))
	}
}
