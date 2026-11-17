// Package kube loads kubeconfig contexts and builds client-go configs tuned for the agent.
package kube

import (
	"fmt"
	"sort"

	"github.com/zyno-io/v8s/agent/internal/version"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/client-go/kubernetes"
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
