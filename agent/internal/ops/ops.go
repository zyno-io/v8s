// Package ops implements the objects API (get, subscribe, dry run, apply, delete, actions) and
// access checks, including linked read/write context routing and requestId dedupe.
package ops

import (
	"context"
	"fmt"
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
	// Now is used for the SSAR cache and the dedupe table; default time.Now.
	Now func() time.Time
}

// Service is the objects API.
type Service struct {
	cfg Config

	mu   sync.Mutex
	ssar map[string]ssarEntry
	dd   map[string]*ddEntry

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
	return &Service{cfg: cfg, ssar: map[string]ssarEntry{}, dd: map[string]*ddEntry{}}
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
	case apierrors.IsUnauthorized(err), apierrors.IsForbidden(err):
		if viaWrite {
			return newErr("write_unavailable", "%s", msg)
		}
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
	key := strings.Join([]string{contextID, verb, gvrString(gvr), ns, name}, "\x00")
	now := s.cfg.Now()
	s.mu.Lock()
	if e, ok := s.ssar[key]; ok && now.Before(e.exp) {
		s.mu.Unlock()
		return e.allowed, e.reason, nil
	}
	s.mu.Unlock()

	rev := &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authorizationv1.ResourceAttributes{
			Verb: verb, Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Namespace: ns, Name: name,
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
	rc, err := s.conn(cluster)
	if err != nil {
		return "", nil, false, err
	}
	allowed, reason, rerr := s.review(ctx, cluster, rc, verb, gvr, ns, name)
	if rerr != nil || allowed {
		return cluster, rc, false, nil
	}
	wctx, auto := s.cfg.Links(cluster)
	if wctx != "" && auto {
		wc, err := s.conn(wctx)
		if err != nil {
			return "", nil, false, err
		}
		return wctx, wc, true, nil
	}
	if reason == "" {
		reason = "denied"
	}
	return "", nil, false, newErr("forbidden", "not allowed to %s %s: %s", verb, gvrString(gvr), reason)
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
		return protocol.AccessResult{Reason: "unable to check: " + trunc(err.Error(), 100)}
	}
	rc, err := s.conn(ck.Cluster)
	if err != nil {
		return unable(err)
	}
	allowed, reason, err := s.review(ctx, ck.Cluster, rc, ck.Verb, gvr, ck.Namespace, ck.Name)
	if err != nil {
		return unable(err)
	}
	if allowed {
		return protocol.AccessResult{Allowed: true}
	}
	if wctx, _ := s.cfg.Links(ck.Cluster); wctx != "" {
		if wc, err := s.conn(wctx); err == nil {
			if ok, _, err := s.review(ctx, wctx, wc, ck.Verb, gvr, ck.Namespace, ck.Name); err == nil && ok {
				return protocol.AccessResult{Allowed: true, ViaWrite: true}
			}
		}
	}
	if reason == "" {
		reason = "denied"
	}
	return protocol.AccessResult{Reason: reason}
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

	r, err := fn()
	s.mu.Lock()
	e.res, e.err = r, err
	e.exp = s.cfg.Now().Add(dedupeTTL)
	if ctx.Err() != nil && s.dd[key] == e {
		delete(s.dd, key) // canceled calls are not replayed
	}
	s.mu.Unlock()
	close(e.done)
	return r, err
}
