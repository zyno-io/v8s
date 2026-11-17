// Package cluster manages which clusters are selected: it starts and stops per-cluster watches
// (reflector -> projecting ListerWatcher -> store), attaches stores to the view engine, tracks
// watch lifetimes (reference counts, linger, budget), discovery and cluster states.
package cluster

import (
	"context"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/disco"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/client-go/kubernetes"
)

// Backend is the connection to one kubeconfig context (defined in package kube so the fake source can implement it).
type Backend = kube.Backend

// Source provides clusters and their backends: kubeconfig contexts, or the fake generator.
type Source interface {
	Mode() string
	// Clusters lists every context (read contexts and potential write contexts alike).
	Clusters() []protocol.Cluster
	// DefaultSelection is the remembered selection reported by clusters.list when nothing else is known.
	DefaultSelection() []string
	// Backend opens (or returns the cached) backend for a context id.
	Backend(contextID string) (Backend, error)
	// Release frees per-context resources once the cluster has been deselected.
	Release(contextID string)
}

// KubeSource serves kubeconfig contexts.
type KubeSource struct {
	Loader kube.Loader

	mu       sync.Mutex
	backends map[string]*kubeBackend
}

func (k *KubeSource) Mode() string { return "desktop" }

func (k *KubeSource) Clusters() []protocol.Cluster {
	cs, err := k.Loader.Clusters()
	if err != nil || cs == nil {
		return []protocol.Cluster{}
	}
	return cs
}

func (k *KubeSource) DefaultSelection() []string { return []string{} }

func (k *KubeSource) Backend(id string) (Backend, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if b := k.backends[id]; b != nil {
		return b, nil
	}
	cfg, err := k.Loader.RestConfig(id)
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
	if k.backends == nil {
		k.backends = map[string]*kubeBackend{}
	}
	b := &kubeBackend{ClientConn: conn, cs: cs}
	k.backends[id] = b
	return b, nil
}

func (k *KubeSource) Release(id string) {
	k.mu.Lock()
	delete(k.backends, id)
	k.mu.Unlock()
}

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
