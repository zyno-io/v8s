package helm

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"helm.sh/helm/v4/pkg/action"
	chartutil "helm.sh/helm/v4/pkg/chart/common/util"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// Config wires the Service to its environment; the package imports neither wire, cluster, fake nor ops.
type Config struct {
	// Runtime returns the clientset and rest config of a context (the read context for reads).
	Runtime func(contextID string) (kube.Runtime, error)
	// Route resolves the context an action runs through (ops.Service.Route for the secrets write).
	Route func(ctx context.Context, cluster, verb, gvr, sub, ns, name string) (contextID string, viaWrite bool, err error)
	// NewActionConfig builds the Helm action configuration for a context and release namespace.
	// Nil: built from Runtime(contextID).RESTConfig().
	NewActionConfig func(ctx context.Context, contextID, namespace string) (*action.Configuration, error)
	Now             func() time.Time
	CacheBytes      int64
	Log             *slog.Logger
}

// ActionError is a protocol error a Service call returns; the wire layer unwraps it into the response.
type ActionError struct{ Body protocol.ErrorBody }

func (e *ActionError) Error() string { return e.Body.Code + ": " + e.Body.Message }

// ErrorBody lets callers map the error without importing this package's type.
func (e *ActionError) ErrorBody() protocol.ErrorBody { return e.Body }

func actionErr(code, format string, a ...any) *ActionError {
	return &ActionError{Body: protocol.ErrorBody{Code: code, Message: fmt.Sprintf(format, a...)}}
}

// Service reads releases (lazily, cached) and prepares the Helm actions.
type Service struct {
	cfg   Config
	cache *Cache
	lim   *limiter
}

// New creates a Service.
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Service{cfg: cfg, cache: NewCache(cfg.CacheBytes), lim: newLimiter(MaxConcurrentFetches)}
}

// Cache exposes the decoded-release cache (tests, metrics).
func (s *Service) Cache() *Cache { return s.cache }

func secretName(name string, rev int) string {
	return "sh.helm.release.v1." + name + ".v" + strconv.Itoa(rev)
}

// metaSource reads Secret metadata of one namespace through the read context.
func (s *Service) metaSource(rt kube.Runtime, ns string) MetaSource {
	if rt.RESTConfig() != nil {
		if c := rt.Clientset().CoreV1().RESTClient(); c != nil {
			return NewNamespacedRESTMetaSource(c, ns)
		}
	}
	return NewNamespacedTypedMetaSource(rt.Clientset(), ns)
}

// revisions lists the revisions of one release from labels (ascending). Never reads payloads.
func (s *Service) revisions(ctx context.Context, rt kube.Runtime, ns, name string) ([]revision, error) {
	items, _, err := listAll(ctx, s.metaSource(rt, ns), "owner=helm,name="+name)
	if err != nil {
		return nil, err
	}
	var out []revision
	for i := range items {
		if r, ok := revisionOf(&items[i]); ok && r.ns == ns && r.name == name {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func (s *Service) validate(cluster, ns, name string) error {
	if cluster == "" || ns == "" || name == "" {
		return actionErr("bad_request", "cluster, namespace and name are required")
	}
	return nil
}

// Release returns one decoded revision (the latest when p.Revision is 0) with the revision history.
func (s *Service) Release(ctx context.Context, p protocol.HelmReleaseParams) (protocol.HelmRelease, error) {
	if err := s.validate(p.Cluster, p.Namespace, p.Name); err != nil {
		return protocol.HelmRelease{}, err
	}
	rt, err := s.cfg.Runtime(p.Cluster)
	if err != nil {
		return protocol.HelmRelease{}, err
	}
	revs, err := s.revisions(ctx, rt, p.Namespace, p.Name)
	if err != nil {
		return protocol.HelmRelease{}, err
	}
	if len(revs) == 0 {
		return protocol.HelmRelease{}, actionErr("not_found", "release %s/%s not found", p.Namespace, p.Name)
	}
	target := revs[len(revs)-1]
	if p.Revision != 0 {
		found := false
		for _, r := range revs {
			if r.version == p.Revision {
				target, found = r, true
			}
		}
		if !found {
			return protocol.HelmRelease{}, actionErr("not_found", "release %s/%s has no revision %d", p.Namespace, p.Name, p.Revision)
		}
	}
	rel, ok := s.cache.Get(CacheKey(target.uid, target.rv))
	if !ok {
		if rel, err = s.fetch(ctx, rt, p.Cluster, target); err != nil {
			return protocol.HelmRelease{}, err
		}
	}
	rel.Status = firstNonEmpty(target.status, rel.Status)
	if rel.Updated == 0 {
		rel.Updated = target.updatedMs
	}
	if target.fluxName != "" {
		rel.ManagedBy = &protocol.GitOps{Kind: "HelmRelease", Name: target.fluxName, Namespace: target.fluxNS}
	}
	rel.History = make([]protocol.HelmRevision, 0, len(revs))
	for _, r := range revs {
		h := protocol.HelmRevision{Revision: r.version, Status: r.status, Updated: r.updatedMs}
		if c, ok := s.cache.Get(CacheKey(r.uid, r.rv)); ok {
			h.Chart = c.Chart
			if c.ChartVersion != "" {
				h.Chart += "-" + c.ChartVersion
			}
			h.AppVersion, h.Description = c.AppVersion, c.Description
		}
		rel.History = append(rel.History, h)
	}
	return rel, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// fetch gets one release Secret (through the per-cluster limiter), decodes and projects it, and caches it.
func (s *Service) fetch(ctx context.Context, rt kube.Runtime, cluster string, r revision) (protocol.HelmRelease, error) {
	release, err := s.lim.acquire(ctx, cluster)
	if err != nil {
		return protocol.HelmRelease{}, err
	}
	sec, err := rt.Clientset().CoreV1().Secrets(r.ns).Get(ctx, secretName(r.name, r.version), metav1.GetOptions{})
	release()
	if err != nil {
		if apierrors.IsNotFound(err) {
			return protocol.HelmRelease{}, actionErr("not_found", "release %s/%s has no revision %d", r.ns, r.name, r.version)
		}
		return protocol.HelmRelease{}, err
	}
	rel, err := DecodeRelease(sec.Data[ReleaseDataKey])
	if err != nil {
		return protocol.HelmRelease{}, actionErr("internal", "decode release %s/%s v%d: %v", r.ns, r.name, r.version, err)
	}
	out := Project(rel, sec.Namespace)
	if out.Revision == 0 {
		out.Revision = r.version
	}
	s.cache.Put(CacheKey(string(sec.UID), sec.ResourceVersion), out)
	return out, nil
}

// Project turns a decoded release into the wire type (no history, no managedBy).
func Project(rel *releasev1.Release, ns string) protocol.HelmRelease {
	out := protocol.HelmRelease{Name: rel.Name, Namespace: firstNonEmpty(rel.Namespace, ns), Revision: rel.Version,
		Manifest: rel.Manifest, Hooks: []protocol.HelmHook{}}
	if rel.Info != nil {
		out.Status = string(rel.Info.Status)
		out.Description = rel.Info.Description
		out.Notes = rel.Info.Notes
		if !rel.Info.LastDeployed.IsZero() {
			out.Updated = rel.Info.LastDeployed.UnixMilli()
		}
	}
	if rel.Chart != nil {
		if rel.Chart.Metadata != nil {
			out.Chart, out.ChartVersion, out.AppVersion = rel.Chart.Metadata.Name, rel.Chart.Metadata.Version, rel.Chart.Metadata.AppVersion
		}
		if vals, err := chartutil.CoalesceValues(rel.Chart, copyMap(rel.Config)); err == nil {
			out.ComputedValues = toYAML(vals)
		}
	} else {
		out.ComputedValues = toYAML(rel.Config)
	}
	out.Values = toYAML(rel.Config)
	for _, h := range rel.Hooks {
		ph := protocol.HelmHook{Name: h.Name, Kind: h.Kind, Path: h.Path, Weight: h.Weight, Events: []string{}}
		for _, e := range h.Events {
			ph.Events = append(ph.Events, string(e))
		}
		out.Hooks = append(out.Hooks, ph)
	}
	return out
}

func copyMap(m map[string]any) map[string]any {
	b, err := yaml.Marshal(m)
	out := map[string]any{}
	if err == nil {
		_ = yaml.Unmarshal(b, &out)
	}
	return out
}

func toYAML(v any) string {
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return ""
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// Diff returns unified diffs of values and manifest between two revisions ("" when identical).
func (s *Service) Diff(ctx context.Context, p protocol.HelmDiffParams) (protocol.HelmDiffResult, error) {
	ref := p.Ref
	if p.FromRevision <= 0 || p.ToRevision <= 0 {
		return protocol.HelmDiffResult{}, actionErr("bad_request", "fromRevision and toRevision are required")
	}
	from, err := s.Release(ctx, protocol.HelmReleaseParams{Cluster: ref.Cluster, Namespace: ref.Namespace, Name: ref.Name, Revision: p.FromRevision})
	if err != nil {
		return protocol.HelmDiffResult{}, err
	}
	to, err := s.Release(ctx, protocol.HelmReleaseParams{Cluster: ref.Cluster, Namespace: ref.Namespace, Name: ref.Name, Revision: p.ToRevision})
	if err != nil {
		return protocol.HelmDiffResult{}, err
	}
	fromName, toName := "revision "+strconv.Itoa(p.FromRevision), "revision "+strconv.Itoa(p.ToRevision)
	return protocol.HelmDiffResult{
		Values:   unified(from.Values, to.Values, fromName, toName),
		Manifest: unified(from.Manifest, to.Manifest, fromName, toName),
	}, nil
}

func unified(a, b, fromName, toName string) string {
	if a == b {
		return ""
	}
	out, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(a), B: difflib.SplitLines(b), FromFile: fromName, ToFile: toName, Context: 3,
	})
	if err != nil {
		return ""
	}
	return out
}
