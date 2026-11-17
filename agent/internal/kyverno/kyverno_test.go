package kyverno

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems/ptest"
	"github.com/zyno-io/v8s/agent/internal/security/sectest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
)

const (
	polrGVR = GroupWG + "/v1alpha2/policyreports"
	vpolGVR = GroupCEL + "/v1/validatingpolicies"
	cpolGVR = GroupLegacy + "/v1/clusterpolicies"
)

func TestPolicyReportTableCounts(t *testing.T) {
	b, err := os.ReadFile("../security/testdata/policyreport_table.json")
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := ingest.DecodeTable(b)
	if err != nil {
		t.Fatal(err)
	}
	m := ingest.Lookup(polrGVR)
	r := m.RowFromTable(tbl.Cols, &tbl.Rows[0])
	if m.StrOf(r, ColScopeKind) != "Deployment" || m.StrOf(r, ColScopeName) == "" || m.StrOf(r, ColScopeName) == r.Name() {
		t.Fatalf("scope: %q %q (report name %q is the resource uid and must not be the subject)", m.StrOf(r, ColScopeKind), m.StrOf(r, ColScopeName), r.Name())
	}
	if m.IntOf(r, ColFail) != 1 || m.IntOf(r, ColPass) != 0 || m.IntOf(r, ColError) != 0 {
		t.Fatalf("counts pass=%d fail=%d error=%d", m.IntOf(r, ColPass), m.IntOf(r, ColFail), m.IntOf(r, ColError))
	}
	if r.Tone() != ingest.ToneDanger {
		t.Fatalf("tone %d", r.Tone())
	}
}

func report(name, ns, kind, wl string, pass, fail, warn, errc int, created time.Time) *ingest.Row {
	m := ingest.Lookup(polrGVR)
	cols := []ingest.TableCol{{Name: "Name"}, {Name: "Kind"}, {Name: "Name"}, {Name: "Pass", Type: "integer"}, {Name: "Fail", Type: "integer"}, {Name: "Warn", Type: "integer"},
		{Name: "Error", Type: "integer"}, {Name: "Skip", Type: "integer"}, {Name: "Age", Type: "date"}}
	return m.RowFromTable(cols, &ingest.TableRow{UID: "u-" + name, Name: name, Namespace: ns, RV: "1", Created: created.UnixMilli(),
		Cells: []any{name, kind, wl, float64(pass), float64(fail), float64(warn), float64(errc), float64(0), "1h"}})
}

func TestPolicyFailAndErrorPerResource(t *testing.T) {
	h := ptest.New(t, polrGVR)
	now := h.Clk.Now()
	h.Put(polrGVR, report("11111111-uid", "payments", "Deployment", "catalog-api", 0, 3, 0, 0, now.Add(-time.Hour)))
	h.Put(polrGVR, report("22222222-uid", "payments", "Deployment", "payments-api", 4, 0, 0, 0, now))
	h.Put(polrGVR, report("33333333-uid", "search", "StatefulSet", "es", 0, 0, 0, 2, now))
	h.Pass()
	fails := h.ByClass("policy.fail")
	if len(fails) != 1 || fails[0].Subject.Name != "catalog-api" || fails[0].Subject.NS != "payments" || fails[0].Count != 3 || fails[0].Title != "Deployment catalog-api: 3 policy failures" {
		t.Fatalf("policy.fail: %+v", fails)
	}
	if fails[0].Since != now.Add(-time.Hour).UnixMilli() || fails[0].Source != "kyverno" || fails[0].Severity.String() != "warning" {
		t.Fatalf("policy.fail evidence: %+v", fails[0])
	}
	errs := h.ByClass("policy.error")
	if len(errs) != 1 || errs[0].Subject.Name != "es" || errs[0].Count != 2 {
		t.Fatalf("policy.error: %+v", errs)
	}
	// the same resource recreated by GitOps gets a new report name (uid): same problem id, so an ack sticks
	id := fails[0].ID
	h.Del(polrGVR, report("11111111-uid", "payments", "Deployment", "catalog-api", 0, 3, 0, 0, now))
	h.Put(polrGVR, report("99999999-uid", "payments", "Deployment", "catalog-api", 0, 3, 0, 0, now))
	h.Pass()
	if fails = h.ByClass("policy.fail"); len(fails) != 1 || fails[0].ID != id {
		t.Fatalf("id changed across report recreation: %+v", fails)
	}
}

func jsonPolicy(kind, apiVersion, body string) []byte {
	return []byte(`{"kind":"` + kind + `","apiVersion":"` + apiVersion + `","metadata":{"name":"p","uid":"u","resourceVersion":"1","creationTimestamp":"2026-11-17T08:00:00Z"},` + body + `}`)
}

func TestPolicyModes(t *testing.T) {
	cases := []struct {
		name, kind, av, body string
		mode, ready          string
		legacy               bool
	}{
		{"cel audit", "ValidatingPolicy", GroupCEL + "/v1", `"spec":{"validationActions":["Audit"],"validations":[{},{}]},"status":{"conditionStatus":{"ready":true}}`, ModeAudit, "True", false},
		{"cel enforce", "ValidatingPolicy", GroupCEL + "/v1", `"spec":{"validationActions":["Deny","Audit"]}`, ModeEnforce, "", false},
		{"cel warn is audit", "ValidatingPolicy", GroupCEL + "/v1", `"spec":{"validationActions":["Warn"]}`, ModeAudit, "", false},
		{"legacy mixed", "ClusterPolicy", GroupLegacy + "/v1", `"spec":{"validationFailureAction":"Enforce","rules":[{"name":"a","validate":{"failureAction":"Enforce"}},{"name":"b","validate":{"failureAction":"Audit"}}]},"status":{"ready":true}`, ModeMixed, "True", true},
		{"legacy per-rule overrides spec", "ClusterPolicy", GroupLegacy + "/v1", `"spec":{"validationFailureAction":"Enforce","rules":[{"name":"a","validate":{"failureAction":"Audit"}}]}`, ModeAudit, "", true},
		{"legacy spec-level fallback", "Policy", GroupLegacy + "/v1", `"spec":{"validationFailureAction":"Enforce","rules":[{"name":"a","validate":{}}]}`, ModeEnforce, "", true},
		{"mutating has no mode", "MutatingPolicy", GroupCEL + "/v1", `"spec":{}`, "", "", false},
		{"not ready", "ValidatingPolicy", GroupCEL + "/v1", `"status":{"conditionStatus":{"ready":false,"message":"nope","conditions":[{"type":"X","status":"False","message":"broken","lastTransitionTime":"2026-11-17T08:10:00Z"}]}}`, "", "False", false},
	}
	for _, c := range cases {
		o, err := Decode(jsonPolicy(c.kind, c.av, c.body))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		p := o.(*Policy)
		if p.Mode != c.mode || p.Ready != c.ready || p.Legacy != c.legacy {
			t.Errorf("%s: mode=%q ready=%q legacy=%v", c.name, p.Mode, p.Ready, p.Legacy)
		}
	}
	// the legacy flag reaches the row as "read-only"; the not-ready policy carries Facts with the condition time
	o, _ := Decode(jsonPolicy("ClusterPolicy", GroupLegacy+"/v1", `"spec":{},"status":{"ready":false,"conditions":[{"type":"Ready","status":"False","message":"m","lastTransitionTime":"2026-11-17T08:10:00Z"}]}`))
	r := Project(o.(*Policy))
	m := ingest.Lookup(cpolGVR)
	if m.StrOf(r, "readOnly") != "read-only" || m.StrOf(r, "ready") != "False" {
		t.Fatalf("row: %q %q", m.StrOf(r, "readOnly"), m.StrOf(r, "ready"))
	}
	f, _ := r.Facts().(*PolicyFacts)
	if f == nil || f.Ready.Since != time.Date(2026, 11, 17, 8, 10, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("facts %+v", f)
	}
}

func TestPolicyNotReadyWaitsForGrace(t *testing.T) {
	h := ptest.New(t, vpolGVR)
	since := h.Clk.Now().Add(-time.Minute)
	body := `"status":{"conditionStatus":{"ready":false,"message":"webhook not configured","conditions":[{"type":"WebhookConfigured","status":"False","message":"webhook not configured","lastTransitionTime":"` + since.UTC().Format(time.RFC3339) + `"}]}}`
	o, _ := Decode(jsonPolicy("ValidatingPolicy", GroupCEL+"/v1", body))
	h.Put(vpolGVR, Project(o.(*Policy)))
	h.Pass()
	if n := len(h.ByClass("policy.not-ready")); n != 0 {
		t.Fatalf("raised inside the 2 minute grace: %d", n)
	}
	h.Advance(90 * time.Second)
	ps := h.ByClass("policy.not-ready")
	if len(ps) != 1 || ps[0].Subject.Kind != "ValidatingPolicy" || ps[0].Message != "webhook not configured" {
		t.Fatalf("%+v", ps)
	}
	// recovering clears it after the hysteresis
	ok, _ := Decode(jsonPolicy("ValidatingPolicy", GroupCEL+"/v1", `"status":{"conditionStatus":{"ready":true}}`))
	h.Put(vpolGVR, Project(ok.(*Policy)))
	h.Pass()
	h.Advance(31 * time.Second)
	if n := len(h.ByClass("policy.not-ready")); n != 0 {
		t.Fatalf("still raised: %d", n)
	}
}

func TestAdmissionDeniedFromEvents(t *testing.T) {
	h := ptest.New(t, "v1/events")
	now := h.Clk.Now()
	ev := func(name, reason string, last time.Time) *ingest.Row {
		return ingest.Lookup("v1/events").ProjectObj(&corev1.Event{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "payments", UID: types.UID("u-" + name), ResourceVersion: "1"},
			Type:       "Warning", Reason: reason, Message: "resource Deployment/payments/api was blocked: team label required", Count: 2,
			InvolvedObject: corev1.ObjectReference{Kind: "Deployment", Name: "api"}, LastTimestamp: metav1.NewTime(last)})
	}
	h.Put("v1/events", ev("e1", "PolicyViolation", now.Add(-10*time.Minute)))
	h.Put("v1/events", ev("e2", "BackOff", now))
	// an audit-mode / background finding carries the same reason but was not blocked
	h.Put("v1/events", ingest.Lookup("v1/events").ProjectObj(&corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "e3", Namespace: "payments", UID: "e3", ResourceVersion: "1"},
		Type: "Warning", Reason: "PolicyViolation", Message: "policy require-labels/check-team fail: validation error: team label required", Count: 7,
		InvolvedObject: corev1.ObjectReference{Kind: "Deployment", Name: "audited"}, LastTimestamp: metav1.NewTime(now)}))
	h.Pass()
	ps := h.ByClass("policy.admission-denied")
	if len(ps) != 1 || ps[0].Subject.Name != "api" || ps[0].Count != 2 || ps[0].Subject.NS != "payments" {
		t.Fatalf("%+v", ps)
	}
	h.Advance(51 * time.Minute) // older than an hour
	h.Advance(31 * time.Second)
	if n := len(h.ByClass("policy.admission-denied")); n != 0 {
		t.Fatalf("denial older than 1 h still listed: %d", n)
	}
}

func policyObj(kind, name, actions string) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": GroupCEL + "/v1", "kind": kind, "metadata": map[string]any{"name": name, "uid": "pol-uid", "resourceVersion": "3"},
		"spec": map[string]any{"validationActions": []any{actions}}}}
	return o
}

func TestSetMode(t *testing.T) {
	vp := schema.GroupVersionResource{Group: GroupCEL, Version: "v1", Resource: "validatingpolicies"}
	cp := schema.GroupVersionResource{Group: GroupLegacy, Version: "v1", Resource: "clusterpolicies"}
	env := sectest.New(map[schema.GroupVersionResource]string{vp: "ValidatingPolicyList", cp: "ClusterPolicyList"}, policyObj("ValidatingPolicy", "require-team-label", "Audit"))
	ctx := context.Background()
	ref := protocol.ObjectRef{Cluster: "c", GVR: vpolGVR, Name: "require-team-label"}
	res, err := SetMode(ctx, &env.Cfg, protocol.KyvernoSetModeParams{Ref: ref, Mode: ModeEnforce})
	if err != nil || res.Mode != ModeEnforce {
		t.Fatalf("%v %v", res, err)
	}
	got, _ := env.Dyn.Resource(vp).Get(ctx, "require-team-label", metav1.GetOptions{})
	acts, _, _ := nestedStrings(got.Object, "spec", "validationActions")
	if len(acts) != 1 || acts[0] != "Deny" {
		t.Fatalf("validationActions %v", acts)
	}
	if _, err := SetMode(ctx, &env.Cfg, protocol.KyvernoSetModeParams{Ref: ref, Mode: ModeAudit}); err != nil {
		t.Fatal(err)
	}
	got, _ = env.Dyn.Resource(vp).Get(ctx, "require-team-label", metav1.GetOptions{})
	if acts, _, _ = nestedStrings(got.Object, "spec", "validationActions"); acts[0] != "Audit" {
		t.Fatalf("back to audit: %v", acts)
	}
	// legacy policies are read-only; a rule cannot be targeted on a CEL policy; a replaced object is refused
	_, err = SetMode(ctx, &env.Cfg, protocol.KyvernoSetModeParams{Ref: protocol.ObjectRef{Cluster: "c", GVR: cpolGVR, Name: "x"}, Mode: ModeEnforce})
	if e, ok := err.(interface{ Error() string }); !ok || !strings.HasPrefix(e.Error(), "bad_request") {
		t.Fatalf("legacy policies must be refused as bad_request (docs/protocol.md): %v", err)
	}
	if _, err = SetMode(ctx, &env.Cfg, protocol.KyvernoSetModeParams{Ref: ref, Mode: ModeEnforce, Rule: "r"}); err == nil {
		t.Fatal("rule on a CEL policy must be refused")
	}
	ref.UID = "other-uid"
	if _, err = SetMode(ctx, &env.Cfg, protocol.KyvernoSetModeParams{Ref: ref, Mode: ModeEnforce}); err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("uid precondition: %v", err)
	}
	if _, err = SetMode(ctx, &env.Cfg, protocol.KyvernoSetModeParams{Ref: protocol.ObjectRef{Cluster: "c", GVR: vpolGVR, Name: "require-team-label"}, Mode: "Nope"}); err == nil {
		t.Fatal("bad mode")
	}
}

func TestExceptionTemplate(t *testing.T) {
	cel, err := ExceptionTemplate(protocol.KyvernoExceptionTemplateParams{
		Policy: protocol.ObjectRef{Cluster: "c", GVR: vpolGVR, Name: "require-team-label"}, Rule: "validation",
		Resource: protocol.KyvernoResourceRef{Kind: "Deployment", Namespace: "payments", Name: "catalog-api"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"apiVersion: policies.kyverno.io/v1", "kind: PolicyException", "namespace: payments", "name: require-team-label", "kind: ValidatingPolicy",
		"name: require-team-label-deployment-catalog-api"} {
		if !strings.Contains(cel.YAML, want) {
			t.Errorf("CEL exception lacks %q:\n%s", want, cel.YAML)
		}
	}
	if cel.Kind != "PolicyException" || cel.APIVersion != "policies.kyverno.io/v1" {
		t.Errorf("result kind/apiVersion: %+v", cel)
	}
	var doc struct {
		Spec struct {
			MatchConditions []struct{ Expression string } `json:"matchConditions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal([]byte(cel.YAML), &doc); err != nil || len(doc.Spec.MatchConditions) != 1 ||
		doc.Spec.MatchConditions[0].Expression != `object.kind == "Deployment" && object.metadata.name == "catalog-api" && object.metadata.namespace == "payments"` {
		t.Errorf("match condition: %+v %v", doc, err)
	}
	leg, err := ExceptionTemplate(protocol.KyvernoExceptionTemplateParams{
		Policy: protocol.ObjectRef{Cluster: "c", GVR: cpolGVR, Name: "restrict-registries"}, Rule: "validate-registries",
		Resource: protocol.KyvernoResourceRef{Kind: "Pod", Namespace: "dev", Name: "tool"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"apiVersion: kyverno.io/v2", "policyName: restrict-registries", "- validate-registries", "- Pod", "- tool", "- dev"} {
		if !strings.Contains(leg.YAML, want) {
			t.Errorf("legacy exception lacks %q:\n%s", want, leg.YAML)
		}
	}
	if _, err := ExceptionTemplate(protocol.KyvernoExceptionTemplateParams{Policy: protocol.ObjectRef{GVR: "apps/v1/deployments", Name: "x"}, Resource: protocol.KyvernoResourceRef{Kind: "Pod", Name: "p"}}); err == nil {
		t.Fatal("not a policy")
	}
}

func TestBuildDetailSortsFailuresFirst(t *testing.T) {
	o := &unstructured.Unstructured{Object: map[string]any{"kind": "PolicyReport", "metadata": map[string]any{"name": "r", "resourceVersion": "4"},
		"scope":   map[string]any{"kind": "Deployment", "name": "api"},
		"summary": map[string]any{"pass": int64(1), "fail": int64(1)},
		"results": []any{
			map[string]any{"policy": "b", "rule": "r", "result": "pass"},
			map[string]any{"policy": "a", "rule": "r", "result": "fail", "message": "no team label", "severity": "medium", "timestamp": map[string]any{"seconds": int64(100)}}}}}
	d, err := BuildDetail(o)
	if err != nil || d.Total != 2 || d.Results[0].Result != "fail" || d.Results[0].Timestamp != 100000 || d.Title != "Deployment api" || d.Summary["fail"] != 1 {
		t.Fatalf("%+v %v", d, err)
	}
}
