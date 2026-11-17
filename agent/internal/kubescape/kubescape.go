// Package kubescape is the Kubescape module. Kubescape's results live in an aggregated API server
// (spdx.softwarecomposition.kubescape.io/v1beta1), not CRDs: only the small summary kinds are watched (typed
// partial JSON), with a paged-list fallback every 5 minutes when the aggregated server does not support
// watch; they are normalized into the same image-vulnerability and config-check shapes as Trivy, tagged
// source "kubescape". Manifests, ApplicationProfiles and NetworkNeighborhoods are fetched on demand.
package kubescape

import (
	"encoding/json"
	"strings"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/security"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
)

// Group is the aggregated API group.
const Group = "spdx.softwarecomposition.kubescape.io"

// Version is the version the modules are registered at.
const Version = "v1beta1"

// Summary kinds.
const (
	ResVulnSummary   = "vulnerabilitymanifestsummaries"
	ResConfigSummary = "workloadconfigurationscansummaries"
)

// Labels Kubescape puts on its stored objects.
const (
	LabelKind      = "kubescape.io/workload-kind"
	LabelName      = "kubescape.io/workload-name"
	LabelNamespace = "kubescape.io/workload-namespace"
	LabelContainer = "kubescape.io/workload-container-name"
	LabelImageTag  = "kubescape.io/image-tag"
	AnnImageID     = "kubescape.io/image-id"
)

// Column keys (the same as the Trivy modules').
const (
	ColKind       = "kind"
	ColWorkload   = "workload"
	ColContainer  = "container"
	ColRepository = "repository"
	ColTag        = "tag"
	ColScanner    = "scanner"
	ColCritical   = "critical"
	ColHigh       = "high"
	ColMedium     = "medium"
	ColLow        = "low"
	ColUnknown    = "unknown"
)

// Summary is the partial decode of a VulnerabilityManifestSummary or WorkloadConfigurationScanSummary.
type Summary struct {
	metav1.TypeMeta
	metav1.ObjectMeta
	Kind_, Workload, WorkloadNS, Container, Image string
	Critical, High, Medium, Low, Unknown          int
}

func (s *Summary) DeepCopyObject() runtime.Object {
	c := *s
	s.ObjectMeta.DeepCopyInto(&c.ObjectMeta)
	return &c
}

type swire struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		UID               types.UID         `json:"uid"`
		ResourceVersion   string            `json:"resourceVersion"`
		CreationTimestamp metav1.Time       `json:"creationTimestamp"`
		Labels            map[string]string `json:"labels"`
		Annotations       map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Severities map[string]json.RawMessage `json:"severities"`
	} `json:"spec"`
}

// sev reads one severity entry: a plain number, or {"all": n, "fixable": m}.
func sev(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return int(n)
	}
	var o struct {
		All     *float64 `json:"all"`
		Total   *float64 `json:"total"`
		Fixable float64  `json:"fixable"`
	}
	if json.Unmarshal(raw, &o) == nil {
		switch {
		case o.All != nil:
			return int(*o.All)
		case o.Total != nil:
			return int(*o.Total)
		}
	}
	return 0
}

// Decode decodes a summary from its JSON.
func Decode(raw []byte) (runtime.Object, error) {
	var w swire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	l := w.Metadata.Labels
	s := &Summary{
		TypeMeta:   metav1.TypeMeta{Kind: w.Kind},
		ObjectMeta: metav1.ObjectMeta{Name: w.Metadata.Name, Namespace: w.Metadata.Namespace, UID: w.Metadata.UID, ResourceVersion: w.Metadata.ResourceVersion, CreationTimestamp: w.Metadata.CreationTimestamp},
		Kind_:      l[LabelKind], Workload: l[LabelName], WorkloadNS: l[LabelNamespace], Container: l[LabelContainer],
		Critical: sev(w.Spec.Severities["critical"]), High: sev(w.Spec.Severities["high"]), Medium: sev(w.Spec.Severities["medium"]), Low: sev(w.Spec.Severities["low"]),
		Unknown: sev(w.Spec.Severities["unknown"]) + sev(w.Spec.Severities["negligible"]),
	}
	if s.WorkloadNS == "" {
		s.WorkloadNS = w.Metadata.Namespace
	}
	if t := l[LabelImageTag]; t != "" {
		s.Image = t
	} else if id := w.Metadata.Annotations[AnnImageID]; id != "" {
		s.Image = id
	}
	return s, nil
}

// SplitImage splits "registry/repo/name:tag" (or @digest) into repository and tag.
func SplitImage(img string) (repo, tag string) {
	if i := strings.IndexByte(img, '@'); i >= 0 {
		return img[:i], img[i+1:]
	}
	if i := strings.LastIndexByte(img, ':'); i > strings.LastIndexByte(img, '/') {
		return img[:i], img[i+1:]
	}
	return img, ""
}

func tone(crit, high int) uint8 {
	switch {
	case crit > 0:
		return ingest.ToneDanger
	case high > 0:
		return ingest.ToneWarning
	}
	return ingest.ToneSuccess
}

func cols(vuln bool) []ingest.Col {
	c := []ingest.Col{ingest.ColCluster(), ingest.ColName(), ingest.ColNS(),
		ingest.ColDict(ColKind, "Resource kind", 120), ingest.ColStr(ColWorkload, "Resource", 240)}
	if vuln {
		c = append(c, ingest.ColDict(ColContainer, "Container", 120), ingest.ColStr(ColRepository, "Repository", 260), ingest.ColDict(ColTag, "Tag", 140))
	}
	c = append(c, ingest.ColDict(ColScanner, "Scanner", 80), ingest.ColInt(ColCritical, "Critical", 76), ingest.ColInt(ColHigh, "High", 76),
		ingest.ColInt(ColMedium, "Medium", 76), ingest.ColInt(ColLow, "Low", 76))
	if vuln {
		c = append(c, ingest.ColInt(ColUnknown, "Unknown", 76))
	}
	return append(c, ingest.ColTone(), ingest.ColAge())
}

func project(s *Summary, vuln bool) *ingest.Row {
	var created int64
	if !s.CreationTimestamp.IsZero() {
		created = s.CreationTimestamp.UnixMilli()
	}
	n := 7
	if vuln {
		n = 10
	}
	r := ingest.NewRow(string(s.UID), s.Name, s.ResourceVersion, s.Namespace, created, false, n)
	r.PutS(s.Kind_)
	r.PutS(s.Workload)
	if vuln {
		repo, tag := SplitImage(s.Image)
		r.PutS(s.Container)
		r.PutS(repo)
		r.PutS(tag)
	}
	r.PutS("Kubescape")
	r.PutN(int64(s.Critical))
	r.PutN(int64(s.High))
	r.PutN(int64(s.Medium))
	r.PutN(int64(s.Low))
	if vuln {
		r.PutN(int64(s.Unknown))
	}
	r.SetTone(tone(s.Critical, s.High))
	return r
}

func newModule(resource, kind string, vuln bool, version string, namespaced bool) *ingest.Module {
	m := &ingest.Module{GVR: Group + "/" + version + "/" + resource, Kind: kind, Namespaced: namespaced, Curated: true, Optional: true, Tier0: true, NoWatchList: true,
		Cols: cols(vuln), DecodeJSON: Decode,
		NewAPI: func(c rest.Interface, g, v, r string) ingest.API {
			return NewPollingAPI(ingest.NewPartialAPI(Decode)(c, g, v, r), PollInterval)
		}}
	m.ProjectObj = func(o runtime.Object) *ingest.Row {
		if s, ok := o.(*Summary); ok {
			return project(s, vuln)
		}
		return nil
	}
	m.Each = ingest.PartialEach(m)
	return m
}

type kindDef struct {
	Resource, Kind string
	Vuln           bool
}

var kinds = []kindDef{
	{ResVulnSummary, "VulnerabilityManifestSummary", true},
	{ResConfigSummary, "WorkloadConfigurationScanSummary", false},
}

func init() {
	for _, k := range kinds {
		ingest.Register(newModule(k.Resource, k.Kind, k.Vuln, Version, true))
	}
	ingest.AddResolver(func(group, version, resource, kind string, namespaced bool) *ingest.Module {
		if group != Group {
			return nil
		}
		for _, k := range kinds {
			if k.Resource == resource && k.Kind == kind {
				return newModule(k.Resource, k.Kind, k.Vuln, version, namespaced)
			}
		}
		return nil
	})
	registerAdapters()
	registerDetail()
	registerScan()
}

var _ = security.ImageRef
var _ = protocol.SourceKubescape
