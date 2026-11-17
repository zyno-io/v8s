// Package cluster manages which clusters are selected: it starts and stops per-cluster pod ingest
// (reflector -> projecting ListerWatcher -> store), attaches stores to the view engine, and reports
// cluster states.
package cluster

import (
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// Source provides clusters and their pod APIs: kubeconfig contexts, or the fake generator.
type Source interface {
	Mode() string
	Clusters() []protocol.Cluster
	// DefaultSelection is the remembered selection reported by clusters.list.
	DefaultSelection() []string
	// PodAPI opens a pod list/watch API for a cluster id.
	PodAPI(id string) (ingest.PodAPI, error)
	// Release frees per-cluster resources once the cluster has been deselected.
	Release(id string)
}

// KubeSource serves kubeconfig contexts.
type KubeSource struct {
	Loader kube.Loader
}

func (k KubeSource) Mode() string { return "desktop" }

func (k KubeSource) Clusters() []protocol.Cluster {
	cs, err := k.Loader.Clusters()
	if err != nil || cs == nil {
		return []protocol.Cluster{}
	}
	return cs
}

func (k KubeSource) DefaultSelection() []string { return []string{} }

func (k KubeSource) PodAPI(id string) (ingest.PodAPI, error) {
	cs, err := k.Loader.Clientset(id)
	if err != nil {
		return nil, err
	}
	return cs.CoreV1().Pods(""), nil
}

func (k KubeSource) Release(string) {}
