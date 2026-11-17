package hosted

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/hosted/config"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/version"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// apiServer is a fake Kubernetes API that records every request's method and identity headers.
type apiServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []http.Header
	meth []string
	ca   string
}

func newAPIServer(t *testing.T) *apiServer {
	a := &apiServer{}
	a.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.reqs = append(a.reqs, r.Header.Clone())
		a.meth = append(a.meth, r.Method)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"PodList","apiVersion":"v1","metadata":{},"items":[]}`))
	}))
	t.Cleanup(a.Close)
	a.ca = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.Certificate().Raw}))
	if _, err := x509.ParseCertificate(a.Certificate().Raw); err != nil {
		t.Fatal(err)
	}
	return a
}

func (a *apiServer) last() (http.Header, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.reqs) == 0 {
		return nil, ""
	}
	return a.reqs[len(a.reqs)-1], a.meth[len(a.meth)-1]
}

func (a *apiServer) methods() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.meth...)
}

var podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

func TestPassthroughSendsTheUsersCurrentToken(t *testing.T) {
	api := newAPIServer(t)
	spec := config.Cluster{ID: "c", Server: api.URL, CAData: api.ca, Strategy: config.StrategyPassthrough}
	cur := "token-one"
	var failing bool
	c := Creds{Corr: "abc123", Identity: Identity{Username: "alice"}, Token: func() (string, error) {
		if failing {
			return "", errNoClusterToken
		}
		return cur, nil
	}, Limiter: newCompositeLimiter(1000, 1000, newClusterLimiter(), nil)}
	be, err := kubeFactory(time.Now)(spec, c)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := be.Dynamic().Resource(podsGVR).Namespace("ns").List(ctx, metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	h, m := api.last()
	if m != "GET" || h.Get("Authorization") != "Bearer token-one" {
		t.Fatalf("first request: %s %q", m, h.Get("Authorization"))
	}
	if want := "v8s/" + version.Version + " (corr abc123)"; h.Get("User-Agent") != want {
		t.Fatalf("user agent %q, want %q", h.Get("User-Agent"), want)
	}
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), "impersonate") {
			t.Fatalf("passthrough must not impersonate: %s", k)
		}
	}
	// The token is re-read on every request (refresh / another session).
	cur = "token-two"
	_, _ = be.Dynamic().Resource(podsGVR).Namespace("ns").List(ctx, metav1.ListOptions{})
	if h, _ := api.last(); h.Get("Authorization") != "Bearer token-two" {
		t.Fatalf("token not refreshed per request: %q", h.Get("Authorization"))
	}
	// No usable token: the request fails with an auth-required style error and sends nothing.
	n := len(api.methods())
	failing = true
	_, err = be.Dynamic().Resource(podsGVR).Namespace("ns").List(ctx, metav1.ListOptions{})
	if err == nil || !strings.Contains(err.Error(), "getting credentials") || len(api.methods()) != n {
		t.Fatalf("expected a credentials error without a request, got %v", err)
	}
}

func TestImpersonationHeaders(t *testing.T) {
	api := newAPIServer(t)
	t.Setenv("V8S_TEST_IMP", "impersonator-token")
	spec := config.Cluster{ID: "c", Server: api.URL, CAData: api.ca, Strategy: config.StrategyImpersonation,
		Impersonation: &config.Impersonation{Token: config.SecretRef{Env: "V8S_TEST_IMP"}, Prefix: "v8s:", GroupPrefix: "v8s:"}}
	c := Creds{Corr: "deadbeef", Limiter: newCompositeLimiter(1000, 1000, newClusterLimiter(), nil),
		Identity: Identity{Username: "alice@example.com", Groups: []string{"devs", "system:masters", "SYSTEM:nodes", "ops"}}}
	be, err := kubeFactory(time.Now)(spec, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.Dynamic().Resource(podsGVR).Namespace("ns").List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	h, _ := api.last()
	if h.Get("Authorization") != "Bearer impersonator-token" {
		t.Fatalf("authorization: %q", h.Get("Authorization"))
	}
	if h.Get("Impersonate-User") != "v8s:alice@example.com" {
		t.Fatalf("Impersonate-User: %q", h.Get("Impersonate-User"))
	}
	if g := h.Values("Impersonate-Group"); strings.Join(g, ",") != "v8s:devs,v8s:ops" {
		t.Fatalf("Impersonate-Group: %v (system: groups must be dropped)", g)
	}
	var corr string
	for k, v := range h {
		if strings.HasPrefix(strings.ToLower(k), "impersonate-extra-") {
			if !strings.Contains(strings.ToLower(k), "v8s.io%2fcorrelation") {
				t.Fatalf("unexpected extra %q", k)
			}
			corr = v[0]
		}
	}
	if corr != "deadbeef" {
		t.Fatalf("correlation extra %q", corr)
	}
	if !strings.Contains(h.Get("User-Agent"), "(corr deadbeef)") {
		t.Fatalf("user agent %q", h.Get("User-Agent"))
	}
}

func TestMapImpersonationRefusesSystemIdentities(t *testing.T) {
	cfg := &config.Impersonation{Prefix: "v8s:", GroupPrefix: "v8s:"}
	for _, u := range []string{"system:admin", "System:Masters", "  system:anonymous", ""} {
		if _, err := MapImpersonation(cfg, Identity{Username: u}, "c"); !errors.Is(err, ErrSystemIdentity) {
			t.Errorf("user %q: want refusal, got %v", u, err)
		}
	}
	for _, bad := range []*config.Impersonation{nil, {Prefix: ""}, {Prefix: "system:", GroupPrefix: "v8s:"}, {Prefix: "v8s:", GroupPrefix: "system:"}} {
		if _, err := MapImpersonation(bad, Identity{Username: "alice"}, "c"); err == nil {
			t.Errorf("prefix config %+v accepted", bad)
		}
	}
	got, err := MapImpersonation(&config.Impersonation{Prefix: "v8s:", GroupPrefix: "g:", Groups: []string{"devs"}},
		Identity{Username: "alice", Groups: []string{"devs", "ops", "system:masters"}}, "corr")
	if err != nil || got.User != "v8s:alice" || strings.Join(got.Groups, ",") != "g:devs" || got.Extra["v8s.io/correlation"][0] != "corr" {
		t.Fatalf("mapping: %+v %v", got, err)
	}
	// Through the rest config builder too.
	spec := config.Cluster{ID: "c", Server: "https://c.example", Strategy: config.StrategyImpersonation,
		Impersonation: &config.Impersonation{Token: config.SecretRef{Env: "X"}, Prefix: "v8s:", GroupPrefix: "v8s:"}}
	if _, err := RestConfig(spec, Creds{Identity: Identity{Username: "system:admin"}}, time.Now); !errors.Is(err, ErrSystemIdentity) {
		t.Fatalf("RestConfig accepted a system: user: %v", err)
	}
}

func TestKioskDisablesMutations(t *testing.T) {
	api := newAPIServer(t)
	t.Setenv("V8S_TEST_KIOSK", "kiosk-token")
	spec := config.Cluster{ID: "c", Server: api.URL, CAData: api.ca, Strategy: config.StrategyKiosk, Kiosk: &config.KioskCred{Token: config.SecretRef{Env: "V8S_TEST_KIOSK"}}}
	inner, err := kubeFactory(time.Now)(spec, Creds{Limiter: newCompositeLimiter(1000, 1000, newClusterLimiter(), nil)})
	if err != nil {
		t.Fatal(err)
	}
	be := readOnlyBackend{inner}
	ctx := context.Background()
	if _, err := be.Dynamic().Resource(podsGVR).Namespace("ns").List(ctx, metav1.ListOptions{}); err != nil {
		t.Fatalf("reads must work: %v", err)
	}
	if h, _ := api.last(); h.Get("Authorization") != "Bearer kiosk-token" {
		t.Fatalf("kiosk token not sent: %q", h.Get("Authorization"))
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "x"}}}
	r := be.Dynamic().Resource(podsGVR)
	rn := r.Namespace("ns")
	errs := map[string]error{
		"create": func() error { _, e := rn.Create(ctx, obj, metav1.CreateOptions{}); return e }(),
		"update": func() error { _, e := rn.Update(ctx, obj, metav1.UpdateOptions{}); return e }(),
		"delete": rn.Delete(ctx, "x", metav1.DeleteOptions{}),
		"patch": func() error {
			_, e := rn.Patch(ctx, "x", types.MergePatchType, []byte(`{}`), metav1.PatchOptions{})
			return e
		}(),
		"apply":          func() error { _, e := rn.Apply(ctx, "x", obj, metav1.ApplyOptions{FieldManager: "v8s"}); return e }(),
		"cluster delete": r.Delete(ctx, "x", metav1.DeleteOptions{}),
	}
	for name, err := range errs {
		if !apierrors.IsForbidden(err) {
			t.Errorf("%s: want forbidden, got %v", name, err)
		}
	}
	// Access reviews say "no" for anything but reads, so the UI disables the actions.
	rev := reviewOf("delete")
	out, _ := be.Authz().Create(ctx, rev, metav1.CreateOptions{})
	if out.Status.Allowed {
		t.Error("kiosk allows delete in access reviews")
	}
	out, _ = be.Authz().Create(ctx, reviewOf("list"), metav1.CreateOptions{})
	if !out.Status.Allowed {
		t.Error("kiosk must allow reads")
	}
	// No runtime: exec, attach, logs, drain and Helm are unavailable; port-forward is off globally.
	if _, ok := kube.RuntimeOf(be); ok {
		t.Error("kiosk backend exposes a runtime (exec/port-forward)")
	}
	// Defence in depth: even the raw client of the real backend cannot write (transport is GET-only).
	if _, err := inner.Dynamic().Resource(podsGVR).Namespace("ns").Create(ctx, obj, metav1.CreateOptions{}); err == nil {
		t.Error("kiosk transport let a POST through")
	}
	for _, m := range api.methods() {
		if m != "GET" {
			t.Fatalf("a %s request reached the API server", m)
		}
	}
}
