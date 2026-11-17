package hosted

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/disco"
	"github.com/zyno-io/v8s/agent/internal/hosted/config"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/version"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	authzv1 "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
)

// Creds is what a cluster backend needs from the signed-in user's engine.
type Creds struct {
	// Token returns the user's current cluster ID token (oidc-passthrough); it is called on every request.
	Token func() (string, error)
	// Identity is the mapped user (impersonation).
	Identity Identity
	// Corr is the correlation id (an HMAC of a session id, never the session id).
	Corr string
	// Limiter is the (user, cluster) read limiter with the shared per-cluster ceiling.
	Limiter flowcontrol.RateLimiter
}

// ClusterFactory builds the backend of one cluster for one user. Tests inject fake clusters.
type ClusterFactory func(spec config.Cluster, c Creds) (cluster.Backend, error)

// ---- impersonation mapping --------------------------------------------------------------------

// ErrSystemIdentity is returned when a mapped user name would start with "system:".
var ErrSystemIdentity = errors.New("refusing to impersonate a system: identity")

func isSystem(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "system:")
}

// Impersonation is the identity v8s asks an impersonator credential to act as.
type Impersonation struct {
	User   string
	Groups []string
	Extra  map[string][]string
}

// MapImpersonation builds the impersonation identity: the user name carries the mandatory prefix; any user or
// group starting with "system:" is hard-denied (a system user is refused, system groups are dropped, never
// forwarded); groups are optionally restricted to the cluster's allowlist and given the group prefix.
func MapImpersonation(cfg *config.Impersonation, id Identity, corr string) (*Impersonation, error) {
	if cfg == nil || cfg.Prefix == "" || isSystem(cfg.Prefix) || isSystem(cfg.GroupPrefix) {
		return nil, errors.New("impersonation prefix is required and must not start with system:")
	}
	if strings.TrimSpace(id.Username) == "" || isSystem(id.Username) {
		return nil, ErrSystemIdentity
	}
	allow := map[string]bool{}
	for _, g := range cfg.Groups {
		allow[g] = true
	}
	out := &Impersonation{User: cfg.Prefix + id.Username, Groups: []string{}, Extra: map[string][]string{"v8s.io/correlation": {corr}}}
	for _, g := range id.Groups {
		if g == "" || isSystem(g) || (len(allow) > 0 && !allow[g]) {
			continue
		}
		out.Groups = append(out.Groups, cfg.GroupPrefix+g)
	}
	if isSystem(out.User) {
		return nil, ErrSystemIdentity
	}
	return out, nil
}

// ---- credentials from files/env -----------------------------------------------------------------

// fileToken reads a secret and re-reads it at most every 30 s (projected tokens rotate).
type fileToken struct {
	ref config.SecretRef
	now func() time.Time

	mu  sync.Mutex
	val string
	at  time.Time
}

func (f *fileToken) get() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.val != "" && f.now().Sub(f.at) < 30*time.Second {
		return f.val, nil
	}
	v, err := f.ref.Resolve()
	if err != nil {
		if f.val != "" {
			return f.val, nil // keep serving the last good value
		}
		return "", fmt.Errorf("getting credentials: %w", err)
	}
	f.val, f.at = v, f.now()
	return v, nil
}

// ---- transports -----------------------------------------------------------------------------------

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// bearerRT sets Authorization from a token source on every request.
func bearerRT(next http.RoundTripper, tok func() (string, error)) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t, err := tok()
		if err != nil {
			return nil, err
		}
		r2 := r.Clone(r.Context())
		r2.Header.Set("Authorization", "Bearer "+t)
		return next.RoundTrip(r2)
	})
}

// readOnlyRT rejects every request that is not a plain read. (Exec upgrades are blocked at the runtime layer.)
func readOnlyRT(next http.RoundTripper) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			return next.RoundTrip(r)
		}
		return nil, errors.New("blocked: shared read-only identity cannot change anything")
	})
}

// ---- real backend --------------------------------------------------------------------------------

type kubeBackend struct {
	*kube.ClientConn
	cs *kubernetes.Clientset
}

func (b *kubeBackend) API(gvr string, m *ingest.Module) (ingest.API, error) {
	g, v, res, err := ingest.ParseGVR(gvr)
	if err != nil {
		return nil, err
	}
	return ingest.APIFor(b.cs, m, g, v, res)
}

func (b *kubeBackend) Discover(ctx context.Context) ([]disco.Resource, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return disco.Fetch(ctx, b.cs.CoreV1().RESTClient())
}

// RestConfig builds the rest.Config of one cluster for one user and strategy. Exported for tests.
func RestConfig(spec config.Cluster, c Creds, now func() time.Time) (*rest.Config, error) {
	ca, err := spec.CAPEM()
	if err != nil {
		return nil, fmt.Errorf("cluster %s: CA: %w", spec.ID, err)
	}
	cfg := &rest.Config{Host: spec.Server, TLSClientConfig: rest.TLSClientConfig{CAData: ca}}
	cfg = kube.Tune(cfg)
	cfg.UserAgent = fmt.Sprintf("v8s/%s (corr %s)", version.Version, c.Corr)
	cfg.RateLimiter = c.Limiter
	switch spec.Strategy {
	case config.StrategyPassthrough:
		tok := c.Token
		cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper { return bearerRT(rt, tok) }
	case config.StrategyImpersonation:
		imp, err := MapImpersonation(spec.Impersonation, c.Identity, c.Corr)
		if err != nil {
			return nil, err
		}
		ft := &fileToken{ref: spec.Impersonation.Token, now: now}
		cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper { return bearerRT(rt, ft.get) }
		// client-go percent-encodes the extra key (Impersonate-Extra-v8s.io%2Fcorrelation).
		cfg.Impersonate = rest.ImpersonationConfig{UserName: imp.User, Groups: imp.Groups, Extra: imp.Extra}
	case config.StrategyKiosk:
		ft := &fileToken{ref: spec.Kiosk.Token, now: now}
		cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper { return readOnlyRT(bearerRT(rt, ft.get)) }
	default:
		return nil, fmt.Errorf("cluster %s: unknown strategy %q", spec.ID, spec.Strategy)
	}
	return cfg, nil
}

func kubeFactory(now func() time.Time) ClusterFactory {
	return func(spec config.Cluster, c Creds) (cluster.Backend, error) {
		cfg, err := RestConfig(spec, c, now)
		if err != nil {
			return nil, err
		}
		cs, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return nil, err
		}
		conn, err := kube.NewConnFromConfig(cfg)
		if err != nil {
			return nil, err
		}
		return &kubeBackend{ClientConn: conn, cs: cs}, nil
	}
}

// ---- kiosk: read-only wrapper -------------------------------------------------------------------

var errReadOnly = errors.New("shared read-only identity: changes are disabled")

// readOnlyBackend disables every mutation, shell and port-forward for a kiosk cluster regardless of what the
// credential allows: the dynamic client refuses writes, access reviews answer "no" for anything but reads, and
// the backend does not expose a Runtime (so exec, attach, logs, drain and Helm are unavailable).
type readOnlyBackend struct{ inner cluster.Backend }

func (b readOnlyBackend) Dynamic() dynamic.Interface { return roDynamic{b.inner.Dynamic()} }
func (b readOnlyBackend) Authz() authzv1.SelfSubjectAccessReviewInterface {
	return roAuthz{}
}
func (b readOnlyBackend) API(gvr string, m *ingest.Module) (ingest.API, error) {
	return b.inner.API(gvr, m)
}
func (b readOnlyBackend) Discover(ctx context.Context) ([]disco.Resource, error) {
	return b.inner.Discover(ctx)
}

type roDynamic struct{ dynamic.Interface }

func (d roDynamic) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return roResource{d.Interface.Resource(gvr), gvr}
}

type roResource struct {
	dynamic.NamespaceableResourceInterface
	gvr schema.GroupVersionResource
}

func (r roResource) Namespace(ns string) dynamic.ResourceInterface {
	return roNS{r.NamespaceableResourceInterface.Namespace(ns), r.gvr}
}

func forbid(gvr schema.GroupVersionResource, name string) error {
	return apierrors.NewForbidden(gvr.GroupResource(), name, errReadOnly)
}

func (r roResource) Create(context.Context, *unstructured.Unstructured, metav1.CreateOptions, ...string) (*unstructured.Unstructured, error) {
	return nil, forbid(r.gvr, "")
}
func (r roResource) Update(context.Context, *unstructured.Unstructured, metav1.UpdateOptions, ...string) (*unstructured.Unstructured, error) {
	return nil, forbid(r.gvr, "")
}
func (r roResource) UpdateStatus(context.Context, *unstructured.Unstructured, metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return nil, forbid(r.gvr, "")
}
func (r roResource) Delete(context.Context, string, metav1.DeleteOptions, ...string) error {
	return forbid(r.gvr, "")
}
func (r roResource) DeleteCollection(context.Context, metav1.DeleteOptions, metav1.ListOptions) error {
	return forbid(r.gvr, "")
}
func (r roResource) Patch(_ context.Context, name string, _ types.PatchType, _ []byte, _ metav1.PatchOptions, _ ...string) (*unstructured.Unstructured, error) {
	return nil, forbid(r.gvr, name)
}
func (r roResource) Apply(_ context.Context, name string, _ *unstructured.Unstructured, _ metav1.ApplyOptions, _ ...string) (*unstructured.Unstructured, error) {
	return nil, forbid(r.gvr, name)
}
func (r roResource) ApplyStatus(_ context.Context, name string, _ *unstructured.Unstructured, _ metav1.ApplyOptions) (*unstructured.Unstructured, error) {
	return nil, forbid(r.gvr, name)
}

type roNS struct {
	dynamic.ResourceInterface
	gvr schema.GroupVersionResource
}

func (r roNS) Create(context.Context, *unstructured.Unstructured, metav1.CreateOptions, ...string) (*unstructured.Unstructured, error) {
	return nil, forbid(r.gvr, "")
}
func (r roNS) Update(context.Context, *unstructured.Unstructured, metav1.UpdateOptions, ...string) (*unstructured.Unstructured, error) {
	return nil, forbid(r.gvr, "")
}
func (r roNS) UpdateStatus(context.Context, *unstructured.Unstructured, metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return nil, forbid(r.gvr, "")
}
func (r roNS) Delete(context.Context, string, metav1.DeleteOptions, ...string) error {
	return forbid(r.gvr, "")
}
func (r roNS) DeleteCollection(context.Context, metav1.DeleteOptions, metav1.ListOptions) error {
	return forbid(r.gvr, "")
}
func (r roNS) Patch(_ context.Context, name string, _ types.PatchType, _ []byte, _ metav1.PatchOptions, _ ...string) (*unstructured.Unstructured, error) {
	return nil, forbid(r.gvr, name)
}
func (r roNS) Apply(_ context.Context, name string, _ *unstructured.Unstructured, _ metav1.ApplyOptions, _ ...string) (*unstructured.Unstructured, error) {
	return nil, forbid(r.gvr, name)
}
func (r roNS) ApplyStatus(_ context.Context, name string, _ *unstructured.Unstructured, _ metav1.ApplyOptions) (*unstructured.Unstructured, error) {
	return nil, forbid(r.gvr, name)
}

type roAuthz struct{}

func (roAuthz) Create(_ context.Context, r *authv1.SelfSubjectAccessReview, _ metav1.CreateOptions) (*authv1.SelfSubjectAccessReview, error) {
	out := r.DeepCopy()
	allowed := false
	if ra := r.Spec.ResourceAttributes; ra != nil {
		switch ra.Verb {
		case "get", "list", "watch":
			allowed = true
		}
	}
	out.Status = authv1.SubjectAccessReviewStatus{Allowed: allowed, Reason: "shared read-only identity (kiosk)"}
	return out, nil
}

// ---- the per-user cluster source -------------------------------------------------------------------

// userSource is a user's cluster.Source: the admin-configured clusters filtered to the user's IdP groups.
type userSource struct {
	srv      *Server
	ue       *userEngine
	visible  []config.Cluster
	mu       sync.Mutex
	backends map[string]cluster.Backend
}

var _ cluster.Source = (*userSource)(nil)

func (u *userSource) Mode() string { return "hosted" }

func (u *userSource) Clusters() []protocol.Cluster {
	out := make([]protocol.Cluster, 0, len(u.visible))
	for _, c := range u.visible {
		g := append([]string{}, c.DisplayGroups...)
		out = append(out, protocol.Cluster{ID: c.ID, Name: c.Name, Server: c.Server, Groups: g})
	}
	return out
}

func (u *userSource) DefaultSelection() []string { return []string{} }

// Backend opens a cluster the user may see. It is only called when the cluster is selected (access is checked
// by the endpoint then); an id outside the user's groups is refused here, so one user can never reach a
// cluster only another user is mapped to.
func (u *userSource) Backend(id string) (cluster.Backend, error) {
	var spec *config.Cluster
	for i := range u.visible {
		if u.visible[i].ID == id {
			spec = &u.visible[i]
			break
		}
	}
	if spec == nil {
		return nil, fmt.Errorf("unknown cluster %q", id)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if b := u.backends[id]; b != nil {
		return b, nil
	}
	creds := Creds{Token: u.ue.clusterToken, Identity: u.ue.ident, Corr: u.ue.corr,
		Limiter: newCompositeLimiter(u.srv.cfg.Limits.ReadQPS, u.srv.cfg.Limits.ReadBurst, u.srv.limiters.get(id), u.srv.clock.Now)}
	b, err := u.srv.factory(*spec, creds)
	if err != nil {
		return nil, err
	}
	if spec.Strategy == config.StrategyKiosk {
		b = readOnlyBackend{b}
	}
	if u.backends == nil {
		u.backends = map[string]cluster.Backend{}
	}
	u.backends[id] = b
	return b, nil
}

func (u *userSource) Release(id string) {
	u.mu.Lock()
	delete(u.backends, id)
	u.mu.Unlock()
}

// visibleClusters filters the configured clusters to the identity's groups (adminGroups see everything).
func visibleClusters(cfg *config.Config, id Identity) []config.Cluster {
	have := map[string]bool{}
	for _, g := range id.Groups {
		have[g] = true
	}
	admin := false
	for _, g := range cfg.AdminGroups {
		admin = admin || have[g]
	}
	var out []config.Cluster
	for _, c := range cfg.Clusters {
		ok := admin
		for _, g := range c.Groups {
			ok = ok || g == "*" || have[g]
		}
		if ok {
			out = append(out, c)
		}
	}
	return out
}
