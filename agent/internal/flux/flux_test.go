package flux

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func project(t *testing.T, js string) (*Object, *ingest.Row) {
	t.Helper()
	o, err := Decode([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := meta.Accessor(o); err != nil {
		t.Fatal(err)
	}
	return o.(*Object), Project(o.(*Object))
}

func cells(r *ingest.Row) map[string]string {
	keys := []string{"ready", "suspended", "revision", "source", "interval", "lastReconcile", "message", "requestedAt", "lastHandled", "dependsOn"}
	m := map[string]string{}
	for i, k := range keys {
		m[k] = r.StrCell(i)
	}
	return m
}

const gitRepo = `{"apiVersion":"source.toolkit.fluxcd.io/v1beta2","kind":"GitRepository",
"metadata":{"name":"flux-system","namespace":"flux-system","uid":"u1","resourceVersion":"5","creationTimestamp":"2026-01-01T00:00:00Z",
"annotations":{"reconcile.fluxcd.io/requestedAt":"2026-01-02T00:00:00Z","other":"x"},"managedFields":[{"manager":"x"}]},
"spec":{"interval":"1m","url":"ssh://git@x/y","ref":{"branch":"main"}},
"status":{"conditions":[{"type":"Ready","status":"True","reason":"Succeeded","message":"stored artifact","lastTransitionTime":"2026-01-02T00:00:00Z"}],
"artifact":{"revision":"main@sha1:abcdef0123456789abcdef0123456789abcdef01"},"lastHandledReconcileAt":"2026-01-01T12:00:00.5Z"}}`

func TestResolverAnyVersion(t *testing.T) {
	m := ingest.ResolveDiscovered(GroupSource, "v1beta2", "gitrepositories", "GitRepository", true)
	if m == nil || m.GVR != "source.toolkit.fluxcd.io/v1beta2/gitrepositories" || !m.Curated || m.NCells != 10 {
		t.Fatalf("%+v", m)
	}
	for _, g := range []string{"source.toolkit.fluxcd.io/v1/gitrepositories", "kustomize.toolkit.fluxcd.io/v1/kustomizations", "helm.toolkit.fluxcd.io/v2/helmreleases"} {
		if ingest.Lookup(g) == nil {
			t.Fatalf("not registered: %s", g)
		}
	}
	for _, k := range [][3]string{{GroupNotify, "Provider", "providers"}, {GroupImage, "ImagePolicy", "imagepolicies"}, {GroupSource, "ExternalArtifact", "externalartifacts"}} {
		if ingest.ResolveDiscovered(k[0], "v1", k[2], k[1], true) != nil {
			t.Fatalf("%s should be generic", k[1])
		}
	}
	o, err := m.DecodeJSON([]byte(gitRepo))
	if err != nil {
		t.Fatal(err)
	}
	row := m.ProjectObj(o)
	if row == nil || row.Name() != "flux-system" {
		t.Fatal("projection through module")
	}
	// column keys and cell layout
	want := []string{"cluster", "name", "namespace", "ready", "suspended", "revision", "source", "interval", "lastReconcile", "message", "requestedAt", "lastHandled", "dependsOn", "statusTone", "createdAt"}
	for i, c := range m.Cols {
		if c.Key != want[i] {
			t.Fatalf("col %d %s", i, c.Key)
		}
	}
	for i, k := range want[3:13] {
		if m.Col(k).Idx != i {
			t.Fatalf("%s idx %d", k, m.Col(k).Idx)
		}
	}
}

func TestProjectGit(t *testing.T) {
	o, r := project(t, gitRepo)
	if _, ok := o.Annotations["other"]; ok {
		t.Fatal("only requestedAt annotation kept")
	}
	c := cells(r)
	if c["ready"] != "True · Succeeded" || c["revision"] != "main@sha1:abcdef0" || c["interval"] != "1m" || c["requestedAt"] != "2026-01-02T00:00:00Z" {
		t.Fatalf("%v", c)
	}
	if r.Cell(CellLastReconcile).N != time.Date(2026, 1, 1, 12, 0, 0, 500e6, time.UTC).UnixMilli() {
		t.Fatalf("lastReconcile %d", r.Cell(CellLastReconcile).N)
	}
	if r.Tone() != ingest.ToneSuccess || r.CreatedMs() == 0 {
		t.Fatal("tone/created")
	}
}

func TestProjectKinds(t *testing.T) {
	cases := []struct {
		name, js string
		ready    string
		tone     uint8
		revision string
		source   string
		depends  string
		susp     string
	}{
		{"ks ready", `{"kind":"Kustomization","metadata":{"name":"a","namespace":"n"},"spec":{"interval":"5m","sourceRef":{"kind":"GitRepository","name":"g","namespace":"flux-system"},"dependsOn":[{"name":"infra"},{"name":"x","namespace":"o"}]},
			"status":{"conditions":[{"type":"Ready","status":"True","reason":"ReconciliationSucceeded"}],"lastAppliedRevision":"main@sha1:0123456789abcdef0123456789abcdef01234567"}}`,
			"True · ReconciliationSucceeded", ingest.ToneSuccess, "main@sha1:0123456", "GitRepository/flux-system/g", "n/infra,o/x", ""},
		{"ks same ns source", `{"kind":"Kustomization","metadata":{"name":"a","namespace":"n"},"spec":{"sourceRef":{"kind":"GitRepository","name":"g","namespace":"n"}},"status":{}}`,
			"Unknown", ingest.ToneWarning, "", "GitRepository/g", "", ""},
		{"ks not ready", `{"kind":"Kustomization","metadata":{"name":"a","namespace":"n"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"False","reason":"BuildFailed","message":"boom"}]}}`,
			"False · BuildFailed", ingest.ToneDanger, "", "", "", ""},
		{"dependency not ready", `{"kind":"Kustomization","metadata":{"name":"a","namespace":"n"},"status":{"conditions":[{"type":"Ready","status":"False","reason":"DependencyNotReady"}]}}`,
			"False · DependencyNotReady", ingest.ToneWarning, "", "", "", ""},
		{"progressing", `{"kind":"Kustomization","metadata":{"name":"a","namespace":"n"},"status":{"conditions":[{"type":"Ready","status":"False","reason":"Whatever"},{"type":"Reconciling","status":"True"}]}}`,
			"False · Whatever", ingest.ToneWarning, "", "", "", ""},
		{"stalled", `{"kind":"Kustomization","metadata":{"name":"a","namespace":"n"},"status":{"conditions":[{"type":"Ready","status":"False","reason":"X"},{"type":"Stalled","status":"True"}]}}`,
			"False · X", ingest.ToneDanger, "", "", "", ""},
		{"suspended neutral", `{"kind":"Kustomization","metadata":{"name":"a","namespace":"n"},"spec":{"suspend":true},"status":{"conditions":[{"type":"Ready","status":"False","reason":"X"}]}}`,
			"False · X", ingest.ToneNeutral, "", "", "", "true"},
		{"no reason", `{"kind":"HelmRepository","metadata":{"name":"a","namespace":"n"},"status":{"conditions":[{"type":"Ready","status":"True"}],"artifact":{"revision":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`,
			"True", ingest.ToneSuccess, "sha256:aaaaaaa", "", "", ""},
		{"hr chart sourceRef + history", `{"kind":"HelmRelease","metadata":{"name":"a","namespace":"n"},"spec":{"interval":"10m","chart":{"spec":{"chart":"c","sourceRef":{"kind":"HelmRepository","name":"bitnami","namespace":"flux-system"}}},"dependsOn":[{"name":"redis"}]},
			"status":{"conditions":[{"type":"Ready","status":"True","reason":"UpgradeSucceeded"}],"history":[{"chartVersion":"19.0.1"},{"chartVersion":"18"}]}}`,
			"True · UpgradeSucceeded", ingest.ToneSuccess, "19.0.1", "HelmRepository/flux-system/bitnami", "n/redis", ""},
		{"hr chartRef", `{"kind":"HelmRelease","metadata":{"name":"a","namespace":"n"},"spec":{"chartRef":{"kind":"OCIRepository","name":"o"}},"status":{"lastAppliedRevision":"1.2.3"}}`,
			"Unknown", ingest.ToneWarning, "1.2.3", "OCIRepository/o", "", ""},
		{"bucket", `{"kind":"Bucket","metadata":{"name":"b","namespace":"n"},"spec":{"interval":"1m"},"status":{"conditions":[{"type":"Ready","status":"False","reason":"ArtifactFailed"}]}}`,
			"False · ArtifactFailed", ingest.ToneDanger, "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, r := project(t, c.js)
			m := cells(r)
			if m["ready"] != c.ready || r.Tone() != c.tone || m["revision"] != c.revision || m["source"] != c.source || m["dependsOn"] != c.depends || m["suspended"] != c.susp {
				t.Fatalf("%v tone=%d", m, r.Tone())
			}
		})
	}
}

func TestMessageTruncation(t *testing.T) {
	msg := strings.Repeat("é", 400)
	js, _ := json.Marshal(map[string]any{"kind": "Kustomization", "metadata": map[string]any{"name": "a"}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "message": msg}}}})
	_, r := project(t, string(js))
	if m := r.StrCell(CellMessage); len(m) > 244 || !strings.HasSuffix(m, "...") {
		t.Fatalf("%d", len(m))
	}
}

func TestPlanAction(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 123, time.UTC)
	tok := "2026-01-02T03:04:05.000000123Z"
	cases := []struct {
		action, kind string
		hasSource    bool
		patch, token string
		sourceFirst  bool
		bad          bool
	}{
		{"flux.suspend", "Kustomization", false, `{"spec":{"suspend":true}}`, "", false, false},
		{"flux.resume", "HelmRelease", false, `{"spec":{"suspend":false}}`, "", false, false},
		{"flux.reconcile", "GitRepository", false, `{"metadata":{"annotations":{"reconcile.fluxcd.io/requestedAt":"` + tok + `"}}}`, tok, false, false},
		{"flux.force", "HelmRelease", false, `{"metadata":{"annotations":{"reconcile.fluxcd.io/forceAt":"` + tok + `","reconcile.fluxcd.io/requestedAt":"` + tok + `"}}}`, tok, false, false},
		{"flux.reset", "HelmRelease", false, `{"metadata":{"annotations":{"reconcile.fluxcd.io/requestedAt":"` + tok + `","reconcile.fluxcd.io/resetAt":"` + tok + `"}}}`, tok, false, false},
		{"flux.reconcileWithSource", "Kustomization", true, `{"metadata":{"annotations":{"reconcile.fluxcd.io/requestedAt":"` + tok + `"}}}`, tok, true, false},
		{"flux.reconcileWithSource", "Kustomization", false, "", "", false, true},
		{"flux.reconcileWithSource", "GitRepository", true, "", "", false, true},
		{"flux.force", "Kustomization", false, "", "", false, true},
		{"flux.reset", "GitRepository", false, "", "", false, true},
		{"flux.explode", "Kustomization", false, "", "", false, true},
		{"scale", "Kustomization", false, "", "", false, true},
	}
	for _, c := range cases {
		p, err := PlanAction(c.action, c.kind, now, c.hasSource)
		if (err != nil) != c.bad {
			t.Fatalf("%v: %v", c, err)
		}
		if c.bad {
			continue
		}
		if string(p.Patch) != c.patch || p.Token != c.token || p.SourceFirst != c.sourceFirst {
			t.Fatalf("%v: %s %s %v", c, p.Patch, p.Token, p.SourceFirst)
		}
	}
}

func TestIsFluxGroupAndKinds(t *testing.T) {
	if !IsFluxGroup(GroupHelm) || !IsFluxGroup(GroupImage) || IsFluxGroup("apps") {
		t.Fatal("groups")
	}
	if len(Kinds()) != 7 {
		t.Fatal("kinds")
	}
	if k, ok := KindOf(GroupNotify, "alerts"); !ok || k != "Alert" {
		t.Fatal("kindOf")
	}
}

func obj(js string) *unstructured.Unstructured {
	var m map[string]any
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		panic(err)
	}
	return &unstructured.Unstructured{Object: m}
}

func TestSourceOf(t *testing.T) {
	av, k, ns, n, ok := SourceOf(obj(`{"metadata":{"namespace":"x"},"spec":{"chart":{"spec":{"sourceRef":{"kind":"HelmRepository","name":"b"}}}}}`))
	if !ok || av != "source.toolkit.fluxcd.io/v1" || k != "HelmRepository" || ns != "x" || n != "b" {
		t.Fatal(av, k, ns, n)
	}
	if _, _, _, _, ok := SourceOf(obj(`{"spec":{}}`)); ok {
		t.Fatal("no source")
	}
}

func podRow(ns, name, status string, tone uint8) *ingest.Row {
	r := ingest.NewRow("u", name, "1", ns, 0, false, 5)
	r.PutN(1)
	r.PutN(1)
	r.PutS(status)
	r.PutN(0)
	r.PutS("node")
	r.SetTone(tone)
	return r
}

func TestInventory(t *testing.T) {
	ks := obj(`{"status":{"lastAppliedRevision":"main@sha1:abc","inventory":{"entries":[
		{"id":"team-1_web_apps_Deployment","v":"v1"},
		{"id":"team-1_web_apps_Deployment_extra","v":"v1"},
		{"id":"team-1_p1__Pod","v":"v1"},
		{"id":"team-1_p2__Pod","v":"v1"},
		{"id":"_team-1__Namespace","v":"v1"},
		{"id":"team-1_cm_under_score__ConfigMap","v":"v1"},
		{"id":"weird","v":"v1"}]}}}`)
	scans := map[string]int{}
	s := New(Config{
		Get: func(_ context.Context, cl, gvr, ns, name string) (*unstructured.Unstructured, error) {
			if cl != "c1" || gvr != "kustomize.toolkit.fluxcd.io/v1/kustomizations" || ns != "flux-system" || name != "apps" {
				t.Fatal("bad get", cl, gvr, ns, name)
			}
			return ks, nil
		},
		Scan: func(cl, gvr string, fn func(*ingest.Row) bool) bool {
			scans[gvr]++
			if gvr != "v1/pods" {
				return false
			}
			for _, r := range []*ingest.Row{podRow("team-1", "p1", "Running", ingest.ToneSuccess), podRow("team-1", "p2", "CrashLoopBackOff", ingest.ToneDanger), podRow("team-9", "p1", "Pending", ingest.ToneWarning)} {
				if !fn(r) {
					break
				}
			}
			return true
		},
		Resolve: func(_, apiVersion, kind string) (string, bool, error) {
			switch kind {
			case "Pod":
				return "v1/pods", true, nil
			case "Deployment":
				return "apps/v1/deployments", true, nil
			case "Namespace":
				return "v1/namespaces", false, nil
			}
			return "", false, errors.New("unknown")
		},
	})
	res, err := s.Inventory(context.Background(), protocol.FluxInventoryParams{Ref: protocol.ObjectRef{Cluster: "c1", GVR: "kustomize.toolkit.fluxcd.io/v1/kustomizations", Namespace: "flux-system", Name: "apps"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision != "main@sha1:abc" || len(res.Entries) != 7 {
		t.Fatalf("%+v", res)
	}
	if scans["v1/pods"] != 1 || scans["apps/v1/deployments"] != 1 || scans["v1/namespaces"] != 1 {
		t.Fatalf("one scan per gvr: %v", scans)
	}
	by := map[string]protocol.FluxInventoryEntry{}
	prev := ""
	for _, e := range res.Entries {
		by[e.ID] = e
		if e.ID < prev {
			t.Fatal("not sorted")
		}
		prev = e.ID
	}
	p2 := by["team-1_p2__Pod"]
	if !p2.Live || p2.Tone != int(ingest.ToneDanger) || p2.Status != "CrashLoopBackOff" || p2.GVR != "v1/pods" || p2.Group != "" || p2.Name != "p2" || p2.Namespace != "team-1" {
		t.Fatalf("%+v", p2)
	}
	if d := by["team-1_web_apps_Deployment"]; d.Live || d.GVR != "apps/v1/deployments" || d.Group != "apps" || d.Version != "v1" {
		t.Fatalf("%+v", d)
	}
	if n := by["_team-1__Namespace"]; n.Namespace != "" || n.Name != "team-1" || n.Kind != "Namespace" {
		t.Fatalf("%+v", n)
	}
	if c := by["team-1_cm_under_score__ConfigMap"]; c.Name != "cm_under_score" || c.Kind != "ConfigMap" || c.Group != "" {
		t.Fatalf("%+v", c)
	}
	if w := by["weird"]; w.Name != "weird" || w.Live {
		t.Fatalf("%+v", w)
	}
}

func TestInventoryGetError(t *testing.T) {
	boom := errors.New("boom")
	s := New(Config{Get: func(context.Context, string, string, string, string) (*unstructured.Unstructured, error) {
		return nil, boom
	}})
	if _, err := s.Inventory(context.Background(), protocol.FluxInventoryParams{}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

func TestGraph(t *testing.T) {
	mk := func(js string) *ingest.Row {
		_, r := project(t, js)
		return r
	}
	rows := map[string][]*ingest.Row{
		"kustomize.toolkit.fluxcd.io/v1/kustomizations": {
			mk(`{"kind":"Kustomization","metadata":{"name":"infra","namespace":"flux-system"},"spec":{"sourceRef":{"kind":"GitRepository","name":"flux-system"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`),
			mk(`{"kind":"Kustomization","metadata":{"name":"apps","namespace":"flux-system"},"spec":{"suspend":true,"sourceRef":{"kind":"GitRepository","name":"flux-system"},"dependsOn":[{"name":"infra"},{"name":"missing"}]}}`),
		},
		"source.toolkit.fluxcd.io/v1beta2/gitrepositories": {
			mk(`{"kind":"GitRepository","metadata":{"name":"flux-system","namespace":"flux-system"}}`),
		},
		"helm.toolkit.fluxcd.io/v2/helmreleases": {
			mk(`{"kind":"HelmRelease","metadata":{"name":"hr","namespace":"t"},"spec":{"chart":{"spec":{"sourceRef":{"kind":"HelmRepository","name":"unwatched","namespace":"flux-system"}}}}}`),
		},
	}
	s := New(Config{
		Resolve: func(_, apiVersion, kind string) (string, bool, error) {
			for _, k := range Kinds() {
				if k.Kind != kind {
					continue
				}
				if kind == "GitRepository" && apiVersion == "source.toolkit.fluxcd.io/v1beta2" {
					return "source.toolkit.fluxcd.io/v1beta2/gitrepositories", true, nil
				}
				if kind != "GitRepository" && apiVersion == k.APIVersion() && kind != "HelmChart" && kind != "OCIRepository" {
					return k.GVR(), true, nil
				}
			}
			return "", false, errors.New("not served")
		},
		Scan: func(_, gvr string, fn func(*ingest.Row) bool) bool {
			for _, r := range rows[gvr] {
				fn(r)
			}
			return rows[gvr] != nil
		},
	})
	g, err := s.Graph(context.Background(), protocol.FluxGraphParams{Cluster: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 4 {
		t.Fatalf("%+v", g.Nodes)
	}
	var apps protocol.FluxGraphNode
	for _, n := range g.Nodes {
		if n.ID == "Kustomization/flux-system/apps" {
			apps = n
		}
	}
	if !apps.Suspended || apps.Tone != 0 || apps.GVR == "" || apps.Name != "apps" {
		t.Fatalf("%+v", apps)
	}
	want := []protocol.FluxGraphEdge{
		{From: "Kustomization/flux-system/apps", To: "GitRepository/flux-system/flux-system", Type: "sourceRef"},
		{From: "Kustomization/flux-system/apps", To: "Kustomization/flux-system/infra", Type: "dependsOn"},
		{From: "Kustomization/flux-system/infra", To: "GitRepository/flux-system/flux-system", Type: "sourceRef"},
	}
	if len(g.Edges) != len(want) {
		t.Fatalf("%+v", g.Edges)
	}
	for i := range want {
		if g.Edges[i] != want[i] {
			t.Fatalf("%d: %+v", i, g.Edges[i])
		}
	}
}

func TestDemoObjects(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	objs := DemoObjects("c1", now)
	if len(objs) < 28 {
		t.Fatalf("%d objects", len(objs))
	}
	seen := map[string]bool{}
	tones := map[uint8]int{}
	for _, u := range objs {
		key := u.GetKind() + "/" + u.GetNamespace() + "/" + u.GetName()
		if seen[key] {
			t.Fatalf("duplicate %s", key)
		}
		seen[key] = true
		ct := u.GetCreationTimestamp()
		if ct.IsZero() || u.GetNamespace() == "" {
			t.Fatalf("%s meta", key)
		}
		b, err := json.Marshal(u.Object)
		if err != nil {
			t.Fatal(err)
		}
		_ = u.DeepCopy()
		o, r := project(t, string(b))
		if o.Name != u.GetName() || r.StrCell(CellReady) == "" {
			t.Fatalf("%s: %q", key, r.StrCell(CellReady))
		}
		tones[r.Tone()]++
	}
	for _, tn := range []uint8{ingest.ToneSuccess, ingest.ToneWarning, ingest.ToneDanger, ingest.ToneNeutral} {
		if tones[tn] == 0 {
			t.Fatalf("no object with tone %d", tn)
		}
	}
	ks := func() *unstructured.Unstructured {
		for _, u := range objs {
			if u.GetKind() == "Kustomization" && u.GetName() == "apps" {
				return u
			}
		}
		return nil
	}()
	if e, _, _ := unstructured.NestedSlice(ks.Object, "status", "inventory", "entries"); len(e) != 12 {
		t.Fatalf("inventory %d", len(e))
	}
}

func TestFakeReconcile(t *testing.T) {
	hr := obj(`{"kind":"HelmRelease","metadata":{"name":"a","annotations":{"reconcile.fluxcd.io/requestedAt":"T1","reconcile.fluxcd.io/forceAt":"T1"}},
		"status":{"lastHandledReconcileAt":"T0","failures":3,"installFailures":1,"conditions":[{"type":"Ready","status":"False","message":"bad","lastTransitionTime":"x"}]}}`)
	tok, ch := FakeReconcile(hr)
	if !ch || tok != "T1" {
		t.Fatal(tok, ch)
	}
	h, _, _ := unstructured.NestedString(hr.Object, "status", "lastHandledReconcileAt")
	conds, _, _ := unstructured.NestedSlice(hr.Object, "status", "conditions")
	c := conds[0].(map[string]any)
	if h != "T1" || c["message"] != "Reconciliation finished" || c["lastTransitionTime"] != "T1" {
		t.Fatalf("%v %v", h, c)
	}
	if _, f, _ := unstructured.NestedFieldNoCopy(hr.Object, "status", "failures"); f {
		t.Fatal("failures kept")
	}
	if _, ch := FakeReconcile(hr); ch {
		t.Fatal("idempotent")
	}
	// plain reconcile keeps failures
	hr2 := obj(`{"kind":"HelmRelease","metadata":{"name":"a","annotations":{"reconcile.fluxcd.io/requestedAt":"T2"}},"status":{"failures":3}}`)
	if _, ch := FakeReconcile(hr2); !ch {
		t.Fatal("changed")
	}
	if _, f, _ := unstructured.NestedFieldNoCopy(hr2.Object, "status", "failures"); !f {
		t.Fatal("failures cleared without force")
	}
	if _, ch := FakeReconcile(obj(`{"kind":"Kustomization","metadata":{"name":"a"}}`)); ch {
		t.Fatal("no request")
	}
}
