// Package kube loads kubeconfig contexts and builds client-go configs tuned for the agent.
package kube

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/zyno-io/v8s/agent/internal/disco"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/version"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	authv1 "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	protobufContentType = "application/vnd.kubernetes.protobuf"
	QPS                 = 20
	Burst               = 40
)

// Loader reads kubeconfig contexts. An empty Path uses client-go's standard loading rules
// ($KUBECONFIG merged, then ~/.kube/config).
type Loader struct {
	Path string
}

func (l Loader) rules() *clientcmd.ClientConfigLoadingRules {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if l.Path != "" {
		rules.ExplicitPath = l.Path
	}
	return rules
}

// Clusters lists the kubeconfig contexts; the cluster id is the context name.
func (l Loader) Clusters() ([]protocol.Cluster, error) {
	raw, err := l.rules().Load()
	if err != nil {
		return nil, err
	}
	out := make([]protocol.Cluster, 0, len(raw.Contexts))
	for name, c := range raw.Contexts {
		server := ""
		if cl := raw.Clusters[c.Cluster]; cl != nil {
			server = cl.Server
		}
		out = append(out, protocol.Cluster{ID: name, Name: name, Server: server, Groups: []string{}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// RestConfig builds the rest.Config for one context: exec plugins run through client-go,
// protobuf is requested for built-in types, QPS 20 / burst 40.
func (l Loader) RestConfig(contextName string) (*rest.Config, error) {
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(l.rules(), &clientcmd.ConfigOverrides{CurrentContext: contextName})
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("context %q: %w", contextName, err)
	}
	return Tune(cfg), nil
}

// Tune applies the agent's client settings.
func Tune(cfg *rest.Config) *rest.Config {
	cfg = rest.CopyConfig(cfg)
	cfg.ContentType = protobufContentType
	cfg.AcceptContentTypes = protobufContentType + ",application/json"
	cfg.UserAgent = "v8s/" + version.Version
	cfg.QPS = QPS
	cfg.Burst = Burst
	return cfg
}

// Clientset builds a typed clientset for one context.
func (l Loader) Clientset(contextName string) (*kubernetes.Clientset, error) {
	cfg, err := l.RestConfig(contextName)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// Conn is what mutating operations need from one kubeconfig context: dynamic access to any resource
// and SelfSubjectAccessReviews. The real implementation wraps client-go; fake mode and tests supply
// in-memory ones.
type Conn interface {
	Dynamic() dynamic.Interface
	Authz() authv1.SelfSubjectAccessReviewInterface
}

// ClientConn is the real Conn built from one rest.Config.
type ClientConn struct {
	dyn   dynamic.Interface
	authz authv1.SelfSubjectAccessReviewInterface
}

func (c *ClientConn) Dynamic() dynamic.Interface                     { return c.dyn }
func (c *ClientConn) Authz() authv1.SelfSubjectAccessReviewInterface { return c.authz }

// NewConn builds the dynamic and authorization clients for a context. Mutations use JSON, not protobuf
// (unstructured has no protobuf encoding).
func (l Loader) NewConn(contextName string) (*ClientConn, error) {
	cfg, err := l.RestConfig(contextName)
	if err != nil {
		return nil, err
	}
	return NewConnFromConfig(cfg)
}

// NewConnFromConfig builds a Conn from an already tuned config.
func NewConnFromConfig(cfg *rest.Config) (*ClientConn, error) {
	jc := rest.CopyConfig(cfg)
	jc.ContentType = "application/json"
	jc.AcceptContentTypes = "application/json"
	jc.WarningHandlerWithContext = sinkHandler{}
	dyn, err := dynamic.NewForConfig(jc)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &ClientConn{dyn: dyn, authz: cs.AuthorizationV1().SelfSubjectAccessReviews()}, nil
}

// WarningSink collects API server warning headers for the requests made with its context.
type WarningSink struct {
	mu   sync.Mutex
	list []string
}

type sinkKey struct{}

// WithWarningSink returns a context whose requests report warning headers to the returned sink
// (real connections only).
func WithWarningSink(ctx context.Context) (context.Context, *WarningSink) {
	s := &WarningSink{}
	return context.WithValue(ctx, sinkKey{}, s), s
}

// Warnings returns the collected warnings.
func (s *WarningSink) Warnings() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.list...)
}

// Add records a warning (also used by in-memory connections).
func (s *WarningSink) Add(w string) {
	s.mu.Lock()
	s.list = append(s.list, w)
	s.mu.Unlock()
}

type sinkHandler struct{}

func (sinkHandler) HandleWarningHeaderWithContext(ctx context.Context, code int, agent string, text string) {
	if s, ok := ctx.Value(sinkKey{}).(*WarningSink); ok && code == 299 {
		s.Add(text)
	}
}

// Backend is the connection to one kubeconfig context: list/watch sources for the ingest path,
// discovery, and the Conn that mutating operations use.
type Backend interface {
	Conn
	// API opens the list/watch source for a GVR. m is the kind's module when one is registered
	// (curated or already-known generic), nil for a generic kind seen for the first time.
	API(gvr string, m *ingest.Module) (ingest.API, error)
	// Discover lists the resources the API server serves.
	Discover(ctx context.Context) ([]disco.Resource, error)
}
