package fake

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newOps(t *testing.T) (*ops.Service, *Source) {
	t.Helper()
	src := NewSource(Config{Clusters: 2, Pods: 400, RBAC: true})
	t.Cleanup(func() { src.Release("prod-us"); src.Release("prod-eu") })
	links := map[string]string{"prod-eu": "prod-eu-admin"}
	return ops.New(ops.Config{
		Conn:  func(id string) (kube.Conn, error) { return src.Backend(id) },
		Links: func(id string) (string, bool) { return links[id], true },
		Resolve: func(cl, apiVersion, kind string) (string, bool, error) {
			for _, r := range Discovery() {
				if r.Kind == kind && (r.Group == "" && apiVersion == r.Version || r.Group+"/"+r.Version == apiVersion) {
					return r.GVR(), r.Namespaced, nil
				}
			}
			return "", false, context.Canceled
		},
	}), src
}

func TestFakeWorldConsistency(t *testing.T) {
	_, src := newOps(t)
	be, _ := src.Backend("prod-us")
	w := be.(*Backend).World()
	counts := map[string]int{}
	for gvr, m := range w.objs {
		counts[gvr] = len(m)
	}
	if counts["v1/nodes"] != 150 || counts["v1/namespaces"] != 44 || counts["apps/v1/deployments"] < 400 || counts["apps/v1/replicasets"] != 800 ||
		counts["apps/v1/statefulsets"] == 0 || counts["apps/v1/daemonsets"] == 0 || counts["v1/services"] < 400 || counts["networking.k8s.io/v1/ingresses"] == 0 ||
		counts["batch/v1/cronjobs"] == 0 || counts["batch/v1/jobs"] == 0 || counts["v1/configmaps"] == 0 || counts["v1/secrets"] == 0 || counts["v1/events"] == 0 ||
		counts["kustomize.toolkit.fluxcd.io/v1/kustomizations"] == 0 || counts["helm.toolkit.fluxcd.io/v2/helmreleases"] == 0 || counts["apiextensions.k8s.io/v1/customresourcedefinitions"] != 7 {
		t.Fatalf("world: %v", counts)
	}
	// pods are owned by the ReplicaSet that belongs to their app's Deployment
	pods := be.(*Backend).c
	p := pods.pod(&pods.pods[0])
	rsName := p.OwnerReferences[0].Name
	rs, ok := w.Get("apps/v1/replicasets", p.Namespace, rsName)
	if !ok {
		t.Fatalf("pod owner ReplicaSet %s/%s does not exist", p.Namespace, rsName)
	}
	if o := rs.GetOwnerReferences(); len(o) != 1 || o[0].Kind != "Deployment" {
		t.Fatalf("replicaset owner: %+v", o)
	}
	if _, ok := w.Get("apps/v1/deployments", p.Namespace, rs.GetOwnerReferences()[0].Name); !ok {
		t.Fatal("deployment missing")
	}
}

func TestFakeActionsAndApply(t *testing.T) {
	svc, src := newOps(t)
	ctx := context.Background()
	be, _ := src.Backend("prod-us")
	w := be.(*Backend).World()
	cj := protocol.ObjectRef{Cluster: "prod-us", GVR: "batch/v1/cronjobs", Namespace: "team-5", Name: "report-1"}
	res, err := svc.Action(ctx, protocol.ObjectActionParams{RequestID: "1", Ref: cj, Action: "trigger"})
	if err != nil || !strings.HasPrefix(res.Name, "report-1-manual-") {
		t.Fatalf("trigger: %+v %v", res, err)
	}
	if _, ok := w.Get("batch/v1/jobs", "team-5", res.Name); !ok {
		t.Fatal("triggered job missing")
	}
	if _, err := svc.Action(ctx, protocol.ObjectActionParams{RequestID: "2", Ref: cj, Action: "suspend"}); err != nil {
		t.Fatal(err)
	}
	if o, _ := w.Get("batch/v1/cronjobs", "team-5", "report-1"); o.Object["spec"].(map[string]any)["suspend"] != true {
		t.Fatal("suspend not applied")
	}
	node := protocol.ObjectRef{Cluster: "prod-us", GVR: "v1/nodes", Name: "node-3"}
	if _, err := svc.Action(ctx, protocol.ObjectActionParams{RequestID: "3", Ref: node, Action: "cordon"}); err != nil {
		t.Fatal(err)
	}
	if o, _ := w.Get("v1/nodes", "", "node-3"); o.Object["spec"].(map[string]any)["unschedulable"] != true {
		t.Fatal("cordon not applied")
	}
	dep := protocol.ObjectRef{Cluster: "prod-us", GVR: "apps/v1/deployments", Namespace: "team-2", Name: "svc-2"}
	if _, err := svc.Action(ctx, protocol.ObjectActionParams{RequestID: "4", Ref: dep, Action: "rollback", Params: map[string]any{"revision": 1}}); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if o, _ := w.Get("apps/v1/deployments", "team-2", "svc-2"); !strings.Contains(o.GetResourceVersion(), "") || !strings.Contains(string(mustJSON(o.Object["spec"])), ":0.2.0") {
		t.Fatalf("rollback did not restore the old template: %s", mustJSON(o.Object["spec"]))
	}
	// create mode: multi-document SSA, dry run first
	yaml := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: made\n  namespace: team-1\ndata:\n  a: b\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: made2\n  namespace: team-1\n"
	cref := protocol.ObjectRef{Cluster: "prod-us", GVR: "v1/configmaps", Namespace: "team-1"}
	if dr, err := svc.DryRun(ctx, protocol.ObjectDryRunParams{Ref: cref, YAML: yaml, Mode: "create"}); err != nil || !dr.OK {
		t.Fatalf("dry run: %+v %v", dr, err)
	}
	if _, ok := w.Get("v1/configmaps", "team-1", "made"); ok {
		t.Fatal("dry run must not persist")
	}
	ar, err := svc.Apply(ctx, protocol.ObjectApplyParams{RequestID: "5", Ref: cref, YAML: yaml, Mode: "create"})
	if err != nil || len(ar.Applied) != 2 {
		t.Fatalf("create: %+v %v", ar, err)
	}
	if _, ok := w.Get("v1/configmaps", "team-1", "made2"); !ok {
		t.Fatal("created object missing")
	}
	// pods work too: delete and get
	if _, err := svc.Delete(ctx, protocol.ObjectDeleteParams{RequestID: "6", Ref: protocol.ObjectRef{Cluster: "prod-us", GVR: "v1/configmaps", Namespace: "team-1", Name: "made"}}); err != nil {
		t.Fatal(err)
	}
	pc := be.(*Backend).c
	p := pc.pod(&pc.pods[0])
	if _, err := svc.Get(ctx, protocol.ObjectGetParams{Cluster: "prod-us", GVR: "v1/pods", Namespace: p.Namespace, Name: p.Name}); err != nil {
		t.Fatalf("pod get: %v", err)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestFakeSecretsForbiddenForReadContext(t *testing.T) {
	_, src := newOps(t)
	list := func(id string) error {
		be, err := src.Backend(id)
		if err != nil {
			t.Fatal(err)
		}
		api, err := be.(*Backend).API("v1/secrets", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = api.List(context.Background(), metav1.ListOptions{})
		return err
	}
	if err := list("prod-eu"); !apierrors.IsForbidden(err) {
		t.Fatalf("read context list secrets: %v", err)
	}
	if err := list("prod-eu-admin"); err != nil {
		t.Fatalf("admin list secrets: %v", err)
	}
	if be, _ := src.Backend("prod-eu-admin"); !be.(*Backend).Admin() {
		t.Fatal("Admin()")
	}
}
