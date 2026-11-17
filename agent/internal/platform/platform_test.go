package platform

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/problems/ptest"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	gvrAPIService = "apiregistration.k8s.io/v1/apiservices"
	gvrValidating = "admissionregistration.k8s.io/v1/validatingwebhookconfigurations"
	gvrMutating   = "admissionregistration.k8s.io/v1/mutatingwebhookconfigurations"
	gvrCert       = "cert-manager.io/v1/certificates"
	gvrHTTPRoute  = "gateway.networking.k8s.io/v1/httproutes"
	gvrGRPCRoute  = "gateway.networking.k8s.io/v1/grpcroutes"
	gvrSlices     = "discovery.k8s.io/v1/endpointslices"
)

func rowOf(t *testing.T, dec func([]byte) (runtime.Object, error), js string) *ingest.Row {
	t.Helper()
	o, err := dec([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	return o.(*Obj).row
}

func slice(name, ns, svc string, ready, total int64) *ingest.Row {
	r := ingest.NewRow("uid-"+name, name, "1", ns, 0, false, 4)
	r.PutS(svc)
	r.PutS("IPv4")
	r.PutN(ready)
	r.PutN(total)
	return r
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func TestAPIService(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		want         int
	}{{"unavailable", "False", 1}, {"available", "True", 0}, {"unknown", "Unknown", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			h := ptest.New(t, gvrAPIService)
			js := fmt.Sprintf(`{"kind":"APIService","metadata":{"name":"v1beta1.metrics.k8s.io","uid":"u1","resourceVersion":"1","creationTimestamp":%q},
				"spec":{"service":{"namespace":"kube-system","name":"metrics-server"}},
				"status":{"conditions":[{"type":"Available","status":%q,"reason":"MissingEndpoints","message":"endpoints for service/metrics-server have no addresses","lastTransitionTime":%q}]}}`,
				ts(h.Clk.Now().Add(-time.Hour)), tc.status, ts(h.Clk.Now().Add(-10*time.Minute)))
			h.Put(gvrAPIService, rowOf(t, decodeAPIService, js))
			h.Pass()
			ps := h.ByClass("apiservice.unavailable")
			if len(ps) != tc.want {
				t.Fatalf("got %d problems: %+v", len(ps), ps)
			}
			if tc.want == 1 && (ps[0].Since != h.Clk.Now().Add(-10*time.Minute).UnixMilli() || !strings.Contains(ps[0].Title, "MissingEndpoints")) {
				t.Fatalf("%+v", ps[0])
			}
		})
	}
}

func webhookJSON(kind, name, policy, ns, svc string) string {
	fp := ""
	if policy != "" {
		fp = fmt.Sprintf(`"failurePolicy":%q,`, policy)
	}
	return fmt.Sprintf(`{"kind":%q,"metadata":{"name":%q,"uid":"u-%s","resourceVersion":"1"},"webhooks":[{"name":"x.example.com",%s"clientConfig":{"service":{"namespace":%q,"name":%q}}}]}`,
		kind, name, name, fp, ns, svc)
}

func TestWebhookFailingJoinsEndpointSlices(t *testing.T) {
	for _, tc := range []struct {
		name, gvr, kind, policy string
		want                    int
	}{
		{"validating default policy is Fail", gvrValidating, "ValidatingWebhookConfiguration", "", 1},
		{"mutating Fail", gvrMutating, "MutatingWebhookConfiguration", "Fail", 1},
		{"Ignore never blocks", gvrValidating, "ValidatingWebhookConfiguration", "Ignore", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := ptest.New(t, gvrValidating, gvrMutating, gvrSlices)
			h.Put(tc.gvr, rowOf(t, decodeWebhooks, webhookJSON(tc.kind, "ingress-admission", tc.policy, "ingress", "admission")))
			h.Pass()
			if n := len(h.ByClass("webhook.failing")); n != 0 {
				t.Fatalf("raised before the 30 s grace: %d", n)
			}
			h.Advance(31 * time.Second)
			if n := len(h.ByClass("webhook.failing")); n != tc.want {
				t.Fatalf("got %d want %d", n, tc.want)
			}
			if tc.want == 0 {
				return
			}
			p := h.ByClass("webhook.failing")[0]
			if p.Severity.String() != "critical" || p.Family != "platform" {
				t.Fatalf("%+v", p)
			}
			// a ready endpoint appears: the configuration is re-evaluated through the slice trigger and clears after the hold
			h.Put(gvrSlices, slice("admission-abc", "ingress", "admission", 1, 1))
			h.Pass()
			h.Advance(31 * time.Second)
			if n := len(h.ByClass("webhook.failing")); n != 0 {
				t.Fatalf("still raised with a ready endpoint: %d", n)
			}
			// ... and back when the endpoint goes unready
			h.Put(gvrSlices, slice("admission-abc", "ingress", "admission", 0, 1))
			h.Pass()
			h.Advance(31 * time.Second)
			if n := len(h.ByClass("webhook.failing")); n != 1 {
				t.Fatalf("not raised again: %d", n)
			}
		})
	}
}

func certJSON(name string, ready string, notAfter time.Time) string {
	na := ""
	if !notAfter.IsZero() {
		na = fmt.Sprintf(`"notAfter":%q,`, ts(notAfter))
	}
	return fmt.Sprintf(`{"kind":"Certificate","metadata":{"name":%q,"namespace":"web","uid":"u-%s","resourceVersion":"1"},"spec":{"issuerRef":{"kind":"ClusterIssuer","name":"le"}},
		"status":{%s"conditions":[{"type":"Ready","status":%q,"reason":"Issuing","message":"waiting","lastTransitionTime":"2026-11-17T11:00:00Z"}]}}`, name, name, na, ready)
}

func TestCertificates(t *testing.T) {
	h := ptest.New(t, gvrCert)
	now := h.Clk.Now()
	h.Put(gvrCert, rowOf(t, decodeCert, certJSON("healthy", "True", now.Add(60*24*time.Hour))))
	h.Put(gvrCert, rowOf(t, decodeCert, certJSON("soon", "True", now.Add(5*24*time.Hour))))
	h.Put(gvrCert, rowOf(t, decodeCert, certJSON("later", "True", now.Add(20*24*time.Hour))))
	h.Put(gvrCert, rowOf(t, decodeCert, certJSON("expired", "True", now.Add(-24*time.Hour))))
	h.Put(gvrCert, rowOf(t, decodeCert, certJSON("broken", "False", time.Time{})))
	h.Pass()
	got := map[string]string{}
	for _, p := range h.ByClass("cert.expiring") {
		got[p.Subject.Name] = p.Severity.String()
	}
	want := map[string]string{"soon": "warning", "expired": "error", "broken": "warning"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		// broken is past its 5 minute grace (transition at 11:00, now 12:00)
		t.Fatalf("got %v want %v", got, want)
	}
	// "later" enters the 14 day window after 6 days: the deadline heap wakes it, no event needed
	h.Advance(6*24*time.Hour + time.Minute)
	found := false
	for _, p := range h.ByClass("cert.expiring") {
		found = found || p.Subject.Name == "later"
	}
	if !found {
		t.Fatalf("later not raised after the window opened: %+v", h.ByClass("cert.expiring"))
	}
}

func TestCertGateNeedsCertManager(t *testing.T) {
	h := ptest.New(t)
	h.E.SetDiscovery(map[string]bool{})
	if h.E.ClassEnabled("cert.expiring") {
		t.Fatal("cert.expiring enabled without cert-manager")
	}
	h.E.SetDiscovery(map[string]bool{GRCertificates: true})
	if !h.E.ClassEnabled("cert.expiring") {
		t.Fatal("cert.expiring not enabled with cert-manager present")
	}
}

func routeJSON(kind, name string, accepted, resolved string) string {
	return fmt.Sprintf(`{"kind":%q,"metadata":{"name":%q,"namespace":"web","uid":"u-%s","resourceVersion":"1"},"status":{"parents":[{"parentRef":{"name":"gw","namespace":"infra"},
		"conditions":[{"type":"Accepted","status":%q,"reason":"NoMatchingParent","message":"no listener","lastTransitionTime":"2026-11-17T11:00:00Z"},
		{"type":"ResolvedRefs","status":%q,"reason":"BackendNotFound","message":"svc missing","lastTransitionTime":"2026-11-17T11:00:00Z"}]}]}}`, kind, name, name, accepted, resolved)
}

func TestGatewayRoutes(t *testing.T) {
	h := ptest.New(t, gvrHTTPRoute, gvrGRPCRoute)
	h.Put(gvrHTTPRoute, rowOf(t, decodeRoute, routeJSON("HTTPRoute", "bad-accept", "False", "True")))
	h.Put(gvrHTTPRoute, rowOf(t, decodeRoute, routeJSON("HTTPRoute", "bad-refs", "True", "False")))
	h.Put(gvrHTTPRoute, rowOf(t, decodeRoute, routeJSON("HTTPRoute", "fine", "True", "True")))
	h.Put(gvrGRPCRoute, rowOf(t, decodeRoute, routeJSON("GRPCRoute", "grpc-bad", "False", "False")))
	h.Pass()
	got := map[string]bool{}
	for _, p := range h.ByClass("gateway.route-unaccepted") {
		got[p.Subject.Name] = true
	}
	if len(got) != 3 || !got["bad-accept"] || !got["bad-refs"] || !got["grpc-bad"] {
		t.Fatalf("%v", got)
	}
}

func TestMissingOptionalKindDoesNotBlockDetector(t *testing.T) {
	// GRPCRoute is not served: gateway.route-unaccepted still runs over the HTTPRoutes.
	h := ptest.New(t, gvrHTTPRoute)
	h.E.SetDiscovery(map[string]bool{GRHTTPRoutes: true})
	h.Put(gvrHTTPRoute, rowOf(t, decodeRoute, routeJSON("HTTPRoute", "bad", "False", "True")))
	h.Pass()
	if n := len(h.ByClass("gateway.route-unaccepted")); n != 1 {
		t.Fatalf("got %d", n)
	}
}

// The wording and severity of an expiring certificate follow the clock without any event: urgent at 3 days, expired at notAfter.
func TestCertExpiryDeadlines(t *testing.T) {
	h := ptest.New(t, gvrCert)
	now := h.Clk.Now()
	h.Put(gvrCert, rowOf(t, decodeCert, certJSON("c", "True", now.Add(4*24*time.Hour))))
	h.Pass()
	one := func() problems.Problem {
		t.Helper()
		ps := h.ByClass("cert.expiring")
		if len(ps) != 1 {
			t.Fatalf("%+v", ps)
		}
		return ps[0]
	}
	if p := one(); p.Severity.String() != "warning" {
		t.Fatalf("4 days out: %+v", p)
	}
	h.Advance(25 * time.Hour) // 3 days left minus an hour: urgent
	if p := one(); p.Severity.String() != "error" || !strings.Contains(p.Title, "expires in 2 days") {
		t.Fatalf("urgent window: %+v", p)
	}
	h.Advance(3 * 24 * time.Hour) // past notAfter
	if p := one(); !strings.Contains(p.Title, "expired") {
		t.Fatalf("expired title not refreshed: %+v", p)
	}
}

// The title counts days, so it is re-rendered every day inside the window and after expiry, without any event.
func TestCertTitleRefreshesDaily(t *testing.T) {
	h := ptest.New(t, gvrCert)
	h.Put(gvrCert, rowOf(t, decodeCert, certJSON("c", "True", h.Clk.Now().Add(13*24*time.Hour+12*time.Hour))))
	h.Pass()
	title := func() string {
		t.Helper()
		ps := h.ByClass("cert.expiring")
		if len(ps) != 1 {
			t.Fatalf("%+v", ps)
		}
		return ps[0].Title
	}
	day := 0
	for _, tc := range []struct {
		day  int
		want string
	}{{0, "expires in 13 days"}, {1, "expires in 12 days"}, {2, "expires in 11 days"}, {7, "expires in 6 days"}, {17, "expired 3 days ago"}, {20, "expired 6 days ago"}} {
		for ; day < tc.day; day++ {
			h.Advance(24 * time.Hour)
		}
		if got := title(); !strings.Contains(got, tc.want) {
			t.Fatalf("day %d: title %q, want %q", tc.day, got, tc.want)
		}
	}
}
