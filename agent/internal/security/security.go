// Package security holds what Kyverno, Trivy Operator and Kubescape share: the normalized shapes (image
// vulnerability summaries, config checks, policy results), the cross-cluster image aggregation, a registry the
// scanner packages plug into (image sources, report-detail providers, action handlers) and the Service that
// serves the request methods (security.images, security.reportDetail, kyverno.*, trivy.rescan, kubescape.scanNow).
package security

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/rpc"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ScanFn calls fn for every row of gvr in the cluster's store until fn returns false; false: not watched.
type ScanFn func(cluster, gvr string, fn func(*ingest.Row) bool) bool

// Config wires the Service to the agent.
type Config struct {
	// Route picks the context a mutation runs through (ops.Service.Route): the read context when allowed,
	// else the linked write context.
	Route func(ctx context.Context, cluster, verb, gvr, sub, ns, name string) (*ops.Routed, error)
	// Conn returns the read connection of a cluster.
	Conn func(cluster string) (kube.Conn, error)
	// Scan reads a cluster's watched rows (mgr.ScanKind).
	Scan ScanFn
	// Synced reports whether a kind is watched and its first list completed (mgr.KindSynced); nil: always.
	Synced func(cluster, gvr string) bool
	// Clusters lists the selected clusters.
	Clusters func() []string
	// Now defaults to time.Now.
	Now func() time.Time
}

func (c *Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// WorkloadRef identifies the workload a finding is about.
type WorkloadRef struct{ Kind, NS, Name string }

// ImageVuln is one scanner's vulnerability counts for one image of one workload in one cluster: the
// scanner-agnostic shape both Trivy VulnerabilityReports and Kubescape VulnerabilityManifestSummaries are
// normalized into (docs/integrations.md: ImageVulnSummary).
type ImageVuln struct {
	Source, Cluster, Repository, Tag     string
	Critical, High, Medium, Low, Unknown int
	Workload                             WorkloadRef
	Exposure                             int // protocol.Exposure* bits of the workload (set by Images)
}

// Image is "repository:tag".
func (v ImageVuln) Image() string { return ImageRef(v.Repository, v.Tag) }

// ImageRef joins repository and tag.
func ImageRef(repo, tag string) string {
	if tag == "" {
		return repo
	}
	return repo + ":" + tag
}

// ConfigCheck is a scanner-agnostic configuration scan summary of one resource.
type ConfigCheck struct {
	Source, Cluster             string
	Critical, High, Medium, Low int
	Subject                     WorkloadRef
}

// AggregateImages deduplicates per-workload image vulnerability records by (image, source) across workloads and
// clusters. The counts of one image are the maximum over its reports (they are the same CVE list unless the
// scans are of different ages); Workloads counts distinct (cluster, kind, namespace, name).
func AggregateImages(in []ImageVuln) []protocol.ImageVulnSummary {
	type key struct{ image, source string }
	type acc struct {
		s    protocol.ImageVulnSummary
		wl   map[string]struct{}
		clus map[string]struct{}
	}
	m := map[key]*acc{}
	for _, v := range in {
		k := key{v.Image(), v.Source}
		a := m[k]
		if a == nil {
			a = &acc{s: protocol.ImageVulnSummary{Image: k.image, Repository: v.Repository, Tag: v.Tag, Source: v.Source}, wl: map[string]struct{}{}, clus: map[string]struct{}{}}
			m[k] = a
		}
		a.s.Critical, a.s.High, a.s.Medium = max(a.s.Critical, v.Critical), max(a.s.High, v.High), max(a.s.Medium, v.Medium)
		a.s.Low, a.s.Unknown = max(a.s.Low, v.Low), max(a.s.Unknown, v.Unknown)
		a.s.Reports++
		a.s.Exposure |= v.Exposure
		a.wl[v.Cluster+"/"+v.Workload.Kind+"/"+v.Workload.NS+"/"+v.Workload.Name] = struct{}{}
		a.clus[v.Cluster] = struct{}{}
	}
	out := make([]protocol.ImageVulnSummary, 0, len(m))
	for _, a := range m {
		a.s.Workloads = len(a.wl)
		for c := range a.clus {
			a.s.Clusters = append(a.s.Clusters, c)
		}
		sort.Strings(a.s.Clusters)
		out = append(out, a.s)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Critical != b.Critical:
			return a.Critical > b.Critical
		case a.High != b.High:
			return a.High > b.High
		case a.Image != b.Image:
			return a.Image < b.Image
		}
		return a.Source < b.Source
	})
	return out
}

// ---- registry ---------------------------------------------------------------------------------

// ImageSource emits the image vulnerability records of one cluster (Trivy, Kubescape).
type ImageSource func(cfg *Config, cluster string, emit func(ImageVuln))

// DetailProvider builds the body of one kind of report. Match says whether it serves the gvr; Fetch (optional)
// replaces the default GET of the referenced object (Kubescape summaries point to their manifest); Build decodes
// the fetched object into the full row set.
type DetailProvider struct {
	Match func(gvr string) bool
	Fetch func(ctx context.Context, cfg *Config, ref protocol.ObjectRef) (*unstructured.Unstructured, error)
	Build func(obj *unstructured.Unstructured) (*protocol.ReportDetail, error)
}

// HandlerFactory builds one request handler from the Service config.
type HandlerFactory func(cfg *Config) rpc.Handler

var reg = struct {
	sync.Mutex
	images    []ImageSource
	providers []DetailProvider
	handlers  map[string]HandlerFactory
}{handlers: map[string]HandlerFactory{}}

// RegisterImageSource adds an image vulnerability source (at init).
func RegisterImageSource(s ImageSource) { reg.Lock(); reg.images = append(reg.images, s); reg.Unlock() }

// RegisterDetail adds a report detail provider (at init).
func RegisterDetail(p DetailProvider) {
	reg.Lock()
	reg.providers = append(reg.providers, p)
	reg.Unlock()
}

// RegisterHandler adds a request method (at init).
func RegisterHandler(method string, f HandlerFactory) {
	reg.Lock()
	reg.handlers[method] = f
	reg.Unlock()
}

// ---- service ----------------------------------------------------------------------------------

// Service serves the security request methods.
type Service struct {
	cfg   Config
	cache *detailCache

	mu     sync.Mutex
	flight map[string]*detailCall   // report key → the fetch in flight
	sems   map[string]chan struct{} // cluster → slots of concurrent report fetches
}

// Report fetches are bounded per cluster (a few huge reports must not saturate an API server) and concurrent
// requests for the same report share one fetch.
const (
	maxDetailFetches = 4
	maxChunk         = 1000 // the largest page security.reportDetail returns whatever limit asks for
)

type detailCall struct {
	done chan struct{}
	d    *protocol.ReportDetail
	err  error
	// leaderGone: the request that led the fetch was cancelled, so err says nothing about the report.
	leaderGone bool
}

// New builds the Service.
func New(cfg Config) *Service {
	return &Service{cfg: cfg, cache: newDetailCache(16), flight: map[string]*detailCall{}, sems: map[string]chan struct{}{}}
}

// Handlers returns every request handler: security.images, security.reportDetail and whatever the scanner
// packages registered (kyverno.setMode, kyverno.exceptionTemplate, trivy.rescan, kubescape.scanNow).
func (s *Service) Handlers() map[string]rpc.Handler {
	out := map[string]rpc.Handler{
		"security.images": func(ctx context.Context, decode func(any) error) (any, error) {
			var p protocol.SecurityImagesParams
			if err := decode(&p); err != nil {
				p = protocol.SecurityImagesParams{}
			}
			return s.Images(ctx, p)
		},
		"security.reportDetail": func(ctx context.Context, decode func(any) error) (any, error) {
			var p protocol.ReportDetailParams
			if err := decode(&p); err != nil {
				return nil, ops.NewError("bad_request", "params: %s", err.Error())
			}
			return s.ReportDetail(ctx, p)
		},
	}
	reg.Lock()
	for m, f := range reg.handlers {
		out[m] = f(&s.cfg)
	}
	reg.Unlock()
	return out
}

// Images answers security.images.
func (s *Service) Images(ctx context.Context, p protocol.SecurityImagesParams) (protocol.SecurityImagesResult, error) {
	clusters := p.Clusters
	if len(clusters) == 0 && s.cfg.Clusters != nil {
		clusters = s.cfg.Clusters()
	}
	reg.Lock()
	srcs := append([]ImageSource(nil), reg.images...)
	reg.Unlock()
	var all []ImageVuln
	for _, c := range clusters {
		from := len(all)
		for _, src := range srcs {
			src(&s.cfg, c, func(v ImageVuln) { all = append(all, v) })
		}
		if len(all) == from {
			continue
		}
		ex, known := ExposureOf(s.cfg.Scan, s.cfg.Synced, c)
		for i := from; i < len(all); i++ {
			if known {
				all[i].Exposure = ex[all[i].Workload]
			} else {
				all[i].Exposure = protocol.ExposureUnknown
			}
		}
	}
	res := protocol.SecurityImagesResult{Images: AggregateImages(all)}
	if res.Images == nil {
		res.Images = []protocol.ImageVulnSummary{}
	}
	return res, nil
}

const defaultChunk = 300

// ReportDetail answers security.reportDetail: the decoded report is cached by resourceVersion, and rows are
// returned in chunks.
func (s *Service) ReportDetail(ctx context.Context, p protocol.ReportDetailParams) (*protocol.ReportDetail, error) {
	ref := p.Ref
	if ref.Cluster == "" || ref.GVR == "" || ref.Name == "" {
		return nil, ops.NewError("bad_request", "ref (cluster, gvr, name) is required")
	}
	reg.Lock()
	var prov *DetailProvider
	for i := range reg.providers {
		if reg.providers[i].Match(ref.GVR) {
			prov = &reg.providers[i]
			break
		}
	}
	reg.Unlock()
	if prov == nil {
		return nil, ops.NewError("bad_request", "no report detail for %s", ref.GVR)
	}
	key := strings.Join([]string{ref.Cluster, ref.GVR, ref.Namespace, ref.Name}, "\x00")
	rv := s.rowRV(ref)
	full, err := s.detail(ctx, key, rv, ref, prov)
	if err != nil {
		return nil, err
	}
	return chunk(full, p.Offset, p.Limit), nil
}

// detail returns the decoded report: from the cache, from a fetch another request already has in flight, or from
// a fetch of its own (at most maxDetailFetches per cluster at a time).
func (s *Service) detail(ctx context.Context, key, rv string, ref protocol.ObjectRef, prov *DetailProvider) (*protocol.ReportDetail, error) {
	for {
		if d := s.cache.get(key, rv, s.cfg.now()); d != nil {
			return d, nil
		}
		s.mu.Lock()
		call := s.flight[key]
		if call == nil {
			call = &detailCall{done: make(chan struct{})}
			s.flight[key] = call
			s.mu.Unlock()
			call.d, call.err = s.fetchDetail(ctx, key, rv, ref, prov)
			call.leaderGone = call.err != nil && ctx.Err() != nil
			s.mu.Lock()
			delete(s.flight, key)
			s.mu.Unlock()
			close(call.done)
			return call.d, call.err
		}
		s.mu.Unlock()
		select {
		case <-call.done:
			if call.err == nil {
				return call.d, nil
			}
			if call.leaderGone && ctx.Err() == nil {
				continue // the request that led the fetch was cancelled, not this one: fetch again
			}
			return nil, call.err
		case <-ctx.Done():
			return nil, ops.MapError(ctx.Err(), false)
		}
	}
}

func (s *Service) fetchDetail(ctx context.Context, key, rv string, ref protocol.ObjectRef, prov *DetailProvider) (*protocol.ReportDetail, error) {
	s.mu.Lock()
	sem := s.sems[ref.Cluster]
	if sem == nil {
		sem = make(chan struct{}, maxDetailFetches)
		s.sems[ref.Cluster] = sem
	}
	s.mu.Unlock()
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return nil, ops.MapError(ctx.Err(), false)
	}
	var obj *unstructured.Unstructured
	var err error
	if prov.Fetch != nil {
		obj, err = prov.Fetch(ctx, &s.cfg, ref)
	} else {
		obj, err = s.get(ctx, ref)
	}
	if err != nil {
		return nil, ops.MapError(err, false)
	}
	d, err := prov.Build(obj)
	if err != nil {
		return nil, ops.NewError("internal", "decode report: %s", err.Error())
	}
	if d.ResourceVersion == "" {
		d.ResourceVersion = obj.GetResourceVersion()
	}
	s.cache.put(key, rv, d, s.cfg.now())
	return d, nil
}

func (s *Service) get(ctx context.Context, ref protocol.ObjectRef) (*unstructured.Unstructured, error) {
	return GetObject(ctx, &s.cfg, ref.Cluster, ref.GVR, ref.Namespace, ref.Name)
}

// rowRV finds the resourceVersion of the report's row in the Table watch ("" when it is not watched).
func (s *Service) rowRV(ref protocol.ObjectRef) string {
	if s.cfg.Scan == nil {
		return ""
	}
	rv := ""
	s.cfg.Scan(ref.Cluster, ref.GVR, func(r *ingest.Row) bool {
		if r.Name() == ref.Name && r.GetNamespace() == ref.Namespace {
			rv = r.ResourceVersion()
			return false
		}
		return true
	})
	return rv
}

func chunk(d *protocol.ReportDetail, off, limit int) *protocol.ReportDetail {
	if limit <= 0 {
		limit = defaultChunk
	}
	limit = min(limit, maxChunk)
	if off < 0 {
		off = 0
	}
	out := *d
	out.Offset = off
	cut := func(n int) (int, int) {
		lo := min(off, n)
		return lo, min(n, lo+limit)
	}
	lo, hi := cut(len(d.Vulns))
	out.Vulns = d.Vulns[lo:hi]
	lo, hi = cut(len(d.Checks))
	out.Checks = d.Checks[lo:hi]
	lo, hi = cut(len(d.Secrets))
	out.Secrets = d.Secrets[lo:hi]
	lo, hi = cut(len(d.Results))
	out.Results = d.Results[lo:hi]
	if len(out.Vulns) == 0 {
		out.Vulns = nil
	}
	if len(out.Checks) == 0 {
		out.Checks = nil
	}
	if len(out.Secrets) == 0 {
		out.Secrets = nil
	}
	if len(out.Results) == 0 {
		out.Results = nil
	}
	return &out
}

// GetObject reads one object through the cluster's read connection.
func GetObject(ctx context.Context, cfg *Config, cluster, gvr, ns, name string) (*unstructured.Unstructured, error) {
	if cfg.Conn == nil {
		return nil, fmt.Errorf("no connection provider")
	}
	c, err := cfg.Conn(cluster)
	if err != nil {
		return nil, err
	}
	g, err := ParseGVR(gvr)
	if err != nil {
		return nil, err
	}
	ri := c.Dynamic().Resource(g)
	if ns != "" {
		return ri.Namespace(ns).Get(ctx, name, getOpts)
	}
	return ri.Get(ctx, name, getOpts)
}

// ---- detail cache -----------------------------------------------------------------------------

type cacheEntry struct {
	rv  string
	d   *protocol.ReportDetail
	at  time.Time
	key string
}

type detailCache struct {
	mu  sync.Mutex
	max int
	m   map[string]*cacheEntry
}

func newDetailCache(max int) *detailCache { return &detailCache{max: max, m: map[string]*cacheEntry{}} }

// ttlNoRV bounds how long an entry without a known resourceVersion (an unwatched kind) serves chunk requests.
const ttlNoRV = 30 * time.Second

func (c *detailCache) get(key, rv string, now time.Time) *protocol.ReportDetail {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[key]
	if e == nil {
		return nil
	}
	if rv != "" {
		if e.rv != rv {
			delete(c.m, key)
			return nil
		}
	} else if now.Sub(e.at) > ttlNoRV {
		delete(c.m, key)
		return nil
	}
	e.at = now
	return e.d
}

func (c *detailCache) put(key, rv string, d *protocol.ReportDetail, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.max {
		var oldest *cacheEntry
		for _, e := range c.m {
			if oldest == nil || e.at.Before(oldest.at) {
				oldest = e
			}
		}
		if oldest != nil {
			delete(c.m, oldest.key)
		}
	}
	c.m[key] = &cacheEntry{rv: rv, d: d, at: now, key: key}
}
