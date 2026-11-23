// Package ops implements the objects API (get, subscribe, dry run, apply, delete, actions) and
// access checks, including linked read/write context routing and requestId dedupe.
package ops

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	fieldManager = "v8s"
	ssarTTL      = 60 * time.Second
	dedupeTTL    = 10 * time.Minute
	dedupeMax    = 10000
	maxMsg       = 300
)

// Config wires the service to the rest of the agent.
type Config struct {
	// Conn returns the connection for a kubeconfig context id (read context id == cluster id; write context id from Links).
	Conn func(contextID string) (kube.Conn, error)
	// Links returns the linked write context for a cluster ("" = none) and whether auto-switch is on.
	Links func(clusterID string) (writeCtx string, autoSwitch bool)
	// Resolve maps an object's apiVersion+kind to its resource (plural, "v1/pods" form) and scope.
	Resolve func(clusterID, apiVersion, kind string) (gvr string, namespaced bool, err error)
	// RawConn returns the connection of exactly this context, ignoring elevation (Conn follows it: a cluster id
	// resolves to its write context while elevated). Default: Conn.
	RawConn func(contextID string) (kube.Conn, error)
	// Elevated returns the write context a cluster currently runs as ("" = not elevated, the default). While
	// elevated, every context of the cluster is already the write context: no read-context review, no confirmation.
	Elevated func(clusterID string) (writeCtx string)
	// Now is used for the SSAR cache and the dedupe table; default time.Now.
	Now func() time.Time
}

// Service is the objects API.
type Service struct {
	cfg Config

	mu    sync.Mutex
	ssar  map[string]ssarEntry
	dd    map[string]*ddEntry
	links map[string]string // "read\x00write" -> "" (same cluster) or the mismatch message

	lastSweep time.Time
}

type ssarEntry struct {
	allowed bool
	reason  string
	exp     time.Time
}

// New builds a Service.
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Links == nil {
		cfg.Links = func(string) (string, bool) { return "", false }
	}
	if cfg.Elevated == nil {
		cfg.Elevated = func(string) string { return "" }
	}
	return &Service{cfg: cfg, ssar: map[string]ssarEntry{}, dd: map[string]*ddEntry{}, links: map[string]string{}}
}

// Error is the protocol-level failure returned by the Service; the wire layer turns it into Response.Error.
type Error struct{ Body protocol.ErrorBody }

func (e *Error) Error() string { return e.Body.Code + ": " + e.Body.Message }

func newErr(code, format string, a ...any) *Error {
	return &Error{Body: protocol.ErrorBody{Code: code, Message: trunc(fmt.Sprintf(format, a...), maxMsg)}}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}

// mapErr converts a client error into a protocol Error. viaWrite marks calls made on the write context.
func mapErr(err error, viaWrite bool) *Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return e
	}
	msg := err.Error()
	if st, ok := err.(apierrors.APIStatus); ok {
		msg = st.Status().Message
		if msg == "" {
			msg = err.Error()
		}
	}
	switch {
	case apierrors.IsNotFound(err):
		return newErr("not_found", "%s", msg)
	case apierrors.IsConflict(err), apierrors.IsAlreadyExists(err):
		return newErr("conflict", "%s", msg)
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return newErr("invalid", "%s", msg)
	case apierrors.IsUnauthorized(err):
		if viaWrite {
			return newErr("write_unavailable", "%s", msg)
		}
		return newErr("forbidden", "%s", msg)
	case apierrors.IsForbidden(err):
		// Admission, quota and PodSecurity rejections are 403s too: only mapW (which asks the write context
		// itself) may call a 403 write_unavailable.
		return newErr("forbidden", "%s", msg)
	}
	return newErr("internal", "%s", msg)
}

func parseGVR(s string) (schema.GroupVersionResource, error) {
	parts := strings.Split(s, "/")
	for _, p := range parts {
		if p == "" {
			return schema.GroupVersionResource{}, newErr("bad_request", "invalid gvr %q", s)
		}
	}
	switch len(parts) {
	case 2:
		return schema.GroupVersionResource{Version: parts[0], Resource: parts[1]}, nil
	case 3:
		return schema.GroupVersionResource{Group: parts[0], Version: parts[1], Resource: parts[2]}, nil
	}
	return schema.GroupVersionResource{}, newErr("bad_request", "invalid gvr %q", s)
}

func gvrString(g schema.GroupVersionResource) string {
	if g.Group == "" {
		return g.Version + "/" + g.Resource
	}
	return g.Group + "/" + g.Version + "/" + g.Resource
}

func resource(dyn dynamic.Interface, gvr schema.GroupVersionResource, ns string) dynamic.ResourceInterface {
	if ns == "" {
		return dyn.Resource(gvr)
	}
	return dyn.Resource(gvr).Namespace(ns)
}

// rawConn is conn for exactly this context: the review cache is keyed by context id, so the connection and the key
// must not come from two different reads of the elevation state.
func (s *Service) rawConn(contextID string) (kube.Conn, error) {
	if s.cfg.RawConn == nil {
		return s.conn(contextID)
	}
	c, err := s.cfg.RawConn(contextID)
	if err != nil {
		return nil, newErr("internal", "context %q unavailable: %s", contextID, err.Error())
	}
	if c == nil {
		return nil, newErr("internal", "context %q unavailable", contextID)
	}
	return c, nil
}

func (s *Service) conn(contextID string) (kube.Conn, error) {
	if s.cfg.Conn == nil {
		return nil, newErr("internal", "no connection provider")
	}
	c, err := s.cfg.Conn(contextID)
	if err != nil {
		return nil, newErr("internal", "context %q unavailable: %s", contextID, err.Error())
	}
	if c == nil {
		return nil, newErr("internal", "context %q unavailable", contextID)
	}
	return c, nil
}

// ---- access review with cache ----------------------------------------------------------

func (s *Service) review(ctx context.Context, contextID string, c kube.Conn, verb string, gvr schema.GroupVersionResource, ns, name string) (bool, string, error) {
	return s.reviewSub(ctx, contextID, c, verb, gvr, "", ns, name)
}

func (s *Service) reviewSub(ctx context.Context, contextID string, c kube.Conn, verb string, gvr schema.GroupVersionResource, sub, ns, name string) (bool, string, error) {
	key := strings.Join([]string{contextID, verb, gvrString(gvr), sub, ns, name}, "\x00")
	now := s.cfg.Now()
	s.mu.Lock()
	if e, ok := s.ssar[key]; ok && now.Before(e.exp) {
		s.mu.Unlock()
		return e.allowed, e.reason, nil
	}
	s.mu.Unlock()

	rev := &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authorizationv1.ResourceAttributes{
			Verb: verb, Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Subresource: sub, Namespace: ns, Name: name,
		},
	}}
	out, err := c.Authz().Create(ctx, rev, metav1.CreateOptions{})
	if err != nil {
		return false, "", err
	}
	e := ssarEntry{allowed: out.Status.Allowed, reason: out.Status.Reason, exp: now.Add(ssarTTL)}
	s.mu.Lock()
	if len(s.ssar) > 1000 {
		for k, v := range s.ssar {
			if !now.Before(v.exp) {
				delete(s.ssar, k)
			}
		}
	}
	s.ssar[key] = e
	s.mu.Unlock()
	return e.allowed, e.reason, nil
}

// route picks the context a mutation runs on.
func (s *Service) route(ctx context.Context, cluster, verb string, gvr schema.GroupVersionResource, ns, name string) (string, kube.Conn, bool, error) {
	return s.routeSub(ctx, cluster, verb, gvr, "", ns, name)
}

// Routed is the context a streamed or long-running operation runs through.
type Routed struct {
	ContextID string
	Conn      kube.Conn
	ViaWrite  bool
}

// Route picks the context for verb on resource[/sub] (gvr as "v1/pods", sub as "exec"): the read context
// when allowed, else the linked write context (same rules as object.action), else forbidden. Streams (exec,
// node shell, port-forward), drain and Helm actions all route through it.
func (s *Service) Route(ctx context.Context, cluster, verb, gvr, sub, ns, name string) (*Routed, error) {
	g, err := parseGVR(gvr)
	if err != nil {
		return nil, err
	}
	cid, c, viaWrite, err := s.routeSub(ctx, cluster, verb, g, sub, ns, name)
	if err != nil {
		return nil, err
	}
	return &Routed{ContextID: cid, Conn: c, ViaWrite: viaWrite}, nil
}

// MapError converts a client-go error into the protocol error of an operation that ran on the write
// context (viaWrite) or the read context.
func MapError(err error, viaWrite bool) error { return mapErr(err, viaWrite) }

// MapRouted is mapW for callers outside the package (streams, drain): err came from a call of verb on
// gvr[/sub] made through the routed context, so a 403 through an expired write context becomes
// write_unavailable instead of a plain forbidden.
func (s *Service) MapRouted(ctx context.Context, err error, r *Routed, verb, gvr, sub, ns, name string) error {
	if err == nil {
		return nil
	}
	g, perr := parseGVR(gvr)
	if r == nil || perr != nil {
		return mapErr(err, r != nil && r.ViaWrite)
	}
	return s.mapW(ctx, err, r.ViaWrite, wsite{cid: r.ContextID, verb: verb, sub: sub, ns: ns, name: name, gvr: g})
}

// NewError builds a protocol error.
func NewError(code, format string, a ...any) error { return newErr(code, format, a...) }

type useWriteKey struct{}

// WithUseWrite marks ctx so routing goes straight to the linked write context (when a link exists).
func WithUseWrite(ctx context.Context, use bool) context.Context {
	if !use {
		return ctx
	}
	return context.WithValue(ctx, useWriteKey{}, true)
}

func useWrite(ctx context.Context) bool { v, _ := ctx.Value(useWriteKey{}).(bool); return v }

var nsGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

// checkLink verifies once per (read, write) pair that both contexts reach the same cluster by comparing the
// kube-system namespace uid. A context that cannot read it is not blocked (nothing to compare).
func (s *Service) checkLink(ctx context.Context, cluster, wctx string, rc, wc kube.Conn) error {
	key := cluster + "\x00" + wctx
	s.mu.Lock()
	if bad, ok := s.links[key]; ok {
		s.mu.Unlock()
		if bad != "" {
			return newErr("link_mismatch", "%s", bad)
		}
		return nil
	}
	s.mu.Unlock()
	// Two independent round trips (often through a gateway): run them together.
	var ru, wu string
	var rerr, werr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		wu, werr = kubeSystemUID(ctx, wc)
	}()
	ru, rerr = kubeSystemUID(ctx, rc)
	wg.Wait()
	if rerr != nil || werr != nil {
		return nil
	}
	msg := ""
	if ru != wu {
		msg = fmt.Sprintf("the write context %q does not reach the same cluster as %q (kube-system namespace uids differ); relink it in the cluster selector", wctx, cluster)
	}
	s.mu.Lock()
	s.links[key] = msg
	s.mu.Unlock()
	if msg != "" {
		return newErr("link_mismatch", "%s", msg)
	}
	return nil
}

func kubeSystemUID(ctx context.Context, c kube.Conn) (string, error) {
	o, err := c.Dynamic().Resource(nsGVR).Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return string(o.GetUID()), nil
}

func (s *Service) routeSub(ctx context.Context, cluster, verb string, gvr schema.GroupVersionResource, sub, ns, name string) (string, kube.Conn, bool, error) {
	if ectx := s.cfg.Elevated(cluster); ectx != "" {
		c, err := s.rawConn(ectx)
		if err != nil {
			return "", nil, false, err
		}
		return ectx, c, true, nil // viaWrite: a 401 is write_unavailable
	}
	rc, err := s.rawConn(cluster)
	if err != nil {
		return "", nil, false, err
	}
	wctx, auto := s.cfg.Links(cluster)
	toWrite := func() (string, kube.Conn, bool, error) {
		wc, err := s.conn(wctx)
		if err != nil {
			return "", nil, false, err
		}
		if err := s.checkLink(ctx, cluster, wctx, rc, wc); err != nil {
			return "", nil, false, err
		}
		return wctx, wc, true, nil
	}
	if wctx != "" && useWrite(ctx) {
		return toWrite()
	}
	allowed, reason, rerr := s.reviewSub(ctx, cluster, rc, verb, gvr, sub, ns, name)
	// Allowed, or the read context cannot answer and there is no write context: the API server decides. With a
	// write context, a read context that cannot even run the access review (a gateway's read-only target refusing
	// the POST) is treated like a refusal, so the mutation goes to the write context instead of failing there.
	if allowed || (rerr != nil && (wctx == "" || !escalates(verb, sub, rerr))) {
		return cluster, rc, false, nil
	}
	if rerr != nil {
		reason = checkCause(rerr)
	}
	if wctx != "" && auto {
		return toWrite()
	}
	if reason == "" {
		reason = "denied"
	}
	res := gvrString(gvr)
	if sub != "" {
		res += "/" + sub
	}
	if wctx != "" {
		return "", nil, false, newErr("write_confirm", "not allowed to %s %s on the read context (%s); the linked write context %q can be used after confirmation", verb, res, reason, wctx)
	}
	return "", nil, false, newErr("forbidden", "not allowed to %s %s: %s", verb, res, reason)
}

// wsite describes the call a mutation made, so a 403 through the write context can be told apart.
type wsite struct {
	cid, verb, sub, ns, name string
	gvr                      schema.GroupVersionResource
}

// mapW maps the error of a call made on a routed context. A 401 on the write context, or a 403 that the write
// context's own access review also denies (or cannot answer), is write_unavailable; any other API rejection
// (admission, quota, PodSecurity, validation) keeps its own code and the server's message.
func (s *Service) mapW(ctx context.Context, err error, viaWrite bool, w wsite) *Error {
	e := mapErr(err, viaWrite)
	if viaWrite && e.Body.Code == "forbidden" && apierrors.IsForbidden(err) {
		wc, cerr := s.conn(w.cid)
		if cerr == nil {
			ok, _, rerr := s.reviewSub(ctx, w.cid, wc, w.verb, w.gvr, w.sub, w.ns, w.name)
			if rerr == nil && ok {
				return e
			}
		}
		return newErr("write_unavailable", "%s", e.Body.Message)
	}
	return e
}

// CheckLink verifies that wctx reaches the same cluster as cluster's own (read) context, the check elevation runs
// before it switches. It compares the raw read context, never the elevated connection.
func (s *Service) CheckLink(ctx context.Context, cluster, wctx string) error {
	if wctx == "" {
		return newErr("bad_request", "cluster %q has no linked write context", cluster)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rc, err := s.rawConn(cluster)
	if err != nil {
		return err
	}
	wc, err := s.rawConn(wctx)
	if err != nil {
		return err
	}
	return s.checkLink(ctx, cluster, wctx, rc, wc)
}

// Access answers access.check, in input order.
func (s *Service) Access(ctx context.Context, checks []protocol.AccessCheck) []protocol.AccessResult {
	out := make([]protocol.AccessResult, len(checks))
	for i, ck := range checks {
		out[i] = s.access(ctx, ck)
	}
	return out
}

func (s *Service) access(ctx context.Context, ck protocol.AccessCheck) protocol.AccessResult {
	gvr, err := parseGVR(ck.GVR)
	if err != nil {
		return protocol.AccessResult{Reason: "unable to check: invalid gvr"}
	}
	unable := func(err error) protocol.AccessResult {
		return protocol.AccessResult{Reason: "unable to check: " + checkCause(err)}
	}
	if ectx := s.cfg.Elevated(ck.Cluster); ectx != "" {
		ec, err := s.rawConn(ectx)
		if err != nil {
			return unable(err)
		}
		ok, reason, rerr := s.review(ctx, ectx, ec, ck.Verb, gvr, ck.Namespace, ck.Name)
		switch {
		case rerr != nil:
			return unable(rerr)
		case ok:
			return protocol.AccessResult{Allowed: true}
		case reason == "":
			reason = "denied"
		}
		return protocol.AccessResult{Reason: reason}
	}
	rc, err := s.rawConn(ck.Cluster)
	if err != nil {
		return unable(err)
	}
	allowed, reason, rerr := s.review(ctx, ck.Cluster, rc, ck.Verb, gvr, ck.Namespace, ck.Name)
	if rerr == nil && allowed {
		return protocol.AccessResult{Allowed: true}
	}
	wctx, auto := s.cfg.Links(ck.Cluster)
	if wctx == "" || (rerr != nil && !escalates(ck.Verb, "", rerr)) {
		if rerr != nil {
			return unable(rerr)
		}
		if reason == "" {
			reason = "denied"
		}
		return protocol.AccessResult{Reason: reason}
	}
	// The read context refused, or could not run the review (a gateway's read-only target may refuse the POST):
	// ask the linked write context.
	viaWrite := func(note string) protocol.AccessResult {
		if !auto { // autoSwitchWrite off: the mutation needs the user's confirmation first
			if note == "" {
				note = "allowed through the linked write context after confirmation"
			}
			return protocol.AccessResult{NeedsConfirm: true, Reason: note}
		}
		return protocol.AccessResult{Allowed: true, ViaWrite: true, Reason: note}
	}
	wc, werr := s.conn(wctx)
	var ok bool
	var wreason string
	if werr == nil {
		ok, wreason, werr = s.review(ctx, wctx, wc, ck.Verb, gvr, ck.Namespace, ck.Name)
	}
	switch {
	case werr == nil && ok:
		return viaWrite("")
	case werr != nil:
		// The write context cannot answer either: typically a gateway target whose elevated session is not
		// active. Offer the action through it; the mutation itself reports write_unavailable if it still fails.
		return viaWrite(fmt.Sprintf("runs through the write context %s, which could not be checked (is its elevated session active?)", wctx))
	}
	// Both refuse: the read context's reason when it answered, else the write context's.
	if rerr == nil {
		wreason = reason
	}
	if wreason == "" {
		wreason = "denied"
	}
	if rerr != nil {
		wreason += " (the read context could not check: " + checkCause(rerr) + ")"
	}
	return protocol.AccessResult{Reason: wreason}
}

// escalates reports whether a failed read-context access review sends a request to the linked write context, as a
// refusal would. Never for reads (logs, uid checks: the read context serves them, and the server decides), and never
// for a transient failure (timeout, cancellation, 429/5xx): a blip must not run a write as the elevated identity.
// A port-forward (create pods/portforward) counts as a read here: it changes nothing, and Hubble's relay connection
// and the background problems monitor depend on it working through the read context.
// What remains is a read context that cannot run the review at all, e.g. a gateway's read-only target refusing
// the POST (EOF, connection reset, 401/403/405 from the gateway).
func escalates(verb, sub string, err error) bool {
	switch {
	case verb == "get", verb == "list", verb == "watch", sub == "portforward":
		return false
	}
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &ne) && ne.Timeout(),
		apierrors.IsTooManyRequests(err), apierrors.IsServerTimeout(err), apierrors.IsTimeout(err),
		apierrors.IsServiceUnavailable(err), apierrors.IsInternalError(err):
		return false
	}
	return true
}

// checkCause is the readable cause of a failed access review: the transport error without the request URL
// (`Post "https://gateway/…/selfsubjectaccessreviews": EOF` → `EOF`), truncated.
func checkCause(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		err = ue.Err
	}
	return trunc(err.Error(), 120)
}

// mutationTimeout bounds a mutation that no longer follows its caller's cancellation.
const mutationTimeout = 60 * time.Second

// detach returns a context that keeps ctx's values but not its cancellation: a connection that drops
// mid-request must neither abort the mutation half way nor make a retry repeat it.
func detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), mutationTimeout)
}

// ---- dedupe ----------------------------------------------------------------------------

type ddEntry struct {
	done chan struct{}
	res  any
	err  error
	exp  time.Time
}

func dedupe[T any](ctx context.Context, s *Service, method, requestID string, fn func() (T, error)) (T, error) {
	var zero T
	if requestID == "" {
		return zero, newErr("bad_request", "requestId is required")
	}
	key := method + "\x00" + requestID
	now := s.cfg.Now()
	s.mu.Lock()
	if e, ok := s.dd[key]; ok {
		select {
		case <-e.done:
			if now.Before(e.exp) {
				s.mu.Unlock()
				r, _ := e.res.(T)
				return r, e.err
			}
		default:
			s.mu.Unlock()
			select {
			case <-e.done:
				r, _ := e.res.(T)
				return r, e.err
			case <-ctx.Done():
				return zero, newErr("internal", "request canceled: %s", ctx.Err().Error())
			}
		}
	}
	// insert; evict expired, then enforce the hard cap
	if len(s.dd) >= dedupeMax || now.Sub(s.lastSweep) >= time.Minute {
		s.lastSweep = now
		for k, e := range s.dd {
			select {
			case <-e.done:
				if !now.Before(e.exp) {
					delete(s.dd, k)
				}
			default:
			}
		}
	}
	if len(s.dd) >= dedupeMax {
		var oldestK string
		var oldest time.Time
		for k, e := range s.dd {
			select {
			case <-e.done:
				if oldestK == "" || e.exp.Before(oldest) {
					oldestK, oldest = k, e.exp
				}
			default:
			}
		}
		if oldestK == "" {
			s.mu.Unlock()
			return zero, newErr("internal", "too many requests in flight")
		}
		delete(s.dd, oldestK)
	}
	e := &ddEntry{done: make(chan struct{})}
	s.dd[key] = e
	s.mu.Unlock()

	r, err := fn() // fn runs detached from the caller's cancellation (see detach): its outcome is always recorded
	s.mu.Lock()
	e.res, e.err = r, err
	e.exp = s.cfg.Now().Add(dedupeTTL)
	s.mu.Unlock()
	close(e.done)
	return r, err
}
