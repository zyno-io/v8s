// Package flux is the Flux module of the agent: curated mode B (typed partial JSON) projections of the
// Flux CRDs, pure action planning, the inventory and dependency graph reads, and fake-world helpers.
package flux

import (
	"github.com/zyno-io/v8s/agent/internal/ingest"
)

const (
	GroupSource    = "source.toolkit.fluxcd.io"
	GroupKustomize = "kustomize.toolkit.fluxcd.io"
	GroupHelm      = "helm.toolkit.fluxcd.io"
	GroupNotify    = "notification.toolkit.fluxcd.io"
	GroupImage     = "image.toolkit.fluxcd.io"
)

// KindInfo describes one Flux kind: its group, kind, plural resource and preferred (newest known) version.
type KindInfo struct {
	Group, Kind, Resource, Version string
	Short                          []string
}

// GVR is "<group>/<version>/<resource>" at the preferred version.
func (k KindInfo) GVR() string { return k.Group + "/" + k.Version + "/" + k.Resource }

// APIVersion is "<group>/<version>" at the preferred version.
func (k KindInfo) APIVersion() string { return k.Group + "/" + k.Version }

// curated kinds get a dedicated module (mode B).
var curated = []KindInfo{
	{GroupSource, "GitRepository", "gitrepositories", "v1", []string{"gitrepo"}},
	{GroupSource, "OCIRepository", "ocirepositories", "v1", []string{"ocirepo"}},
	{GroupSource, "HelmRepository", "helmrepositories", "v1", []string{"helmrepo"}},
	{GroupSource, "HelmChart", "helmcharts", "v1", []string{"hc"}},
	{GroupSource, "Bucket", "buckets", "v1", nil},
	{GroupKustomize, "Kustomization", "kustomizations", "v1", []string{"ks"}},
	{GroupHelm, "HelmRelease", "helmreleases", "v2", []string{"hr"}},
}

// generic kinds are Flux kinds served by the generic Table path; they need no module here.
var generic = []KindInfo{
	{GroupNotify, "Provider", "providers", "v1beta3", nil},
	{GroupNotify, "Alert", "alerts", "v1beta3", nil},
	{GroupNotify, "Receiver", "receivers", "v1", nil},
	{GroupImage, "ImageRepository", "imagerepositories", "v1", nil},
	{GroupImage, "ImagePolicy", "imagepolicies", "v1", nil},
	{GroupImage, "ImageUpdateAutomation", "imageupdateautomations", "v1", nil},
}

// Kinds returns the curated Flux kinds.
func Kinds() []KindInfo { return append([]KindInfo(nil), curated...) }

// IsFluxGroup reports whether group is one of the Flux API groups (curated or generic kinds).
func IsFluxGroup(group string) bool {
	switch group {
	case GroupSource, GroupKustomize, GroupHelm, GroupNotify, GroupImage:
		return true
	}
	return false
}

// KindOf maps a (group, plural resource) to its Kind, for curated and generic Flux kinds.
func KindOf(group, resource string) (string, bool) {
	for _, set := range [][]KindInfo{curated, generic} {
		for _, k := range set {
			if k.Group == group && k.Resource == resource {
				return k.Kind, true
			}
		}
	}
	return "", false
}

func curatedByKind(group, kind string) *KindInfo {
	for i := range curated {
		if curated[i].Group == group && curated[i].Kind == kind {
			return &curated[i]
		}
	}
	return nil
}

func init() {
	for _, k := range curated {
		ingest.Register(newModule(k.Group, k.Version, k.Resource, k.Kind, k.Short))
	}
	ingest.AddResolver(func(group, version, resource, kind string, namespaced bool) *ingest.Module {
		k := curatedByKind(group, kind)
		if k == nil || resource != k.Resource {
			return nil // includes the generic kinds (notification, image): left to the Table path
		}
		return newModule(group, version, resource, kind, k.Short)
	})
}
