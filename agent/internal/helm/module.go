package helm

import (
	"strconv"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
)

// Release is one release (its latest revision) as the list sees it, built from Secret labels only.
// It carries ObjectMeta so the reflector can track its resourceVersion.
type Release struct {
	metav1.TypeMeta
	metav1.ObjectMeta
	Revision  int
	Status    string
	UpdatedMs int64
	ManagedBy string // "flux:<ns>/<name>" or ""
}

func (r *Release) GetObjectKind() schema.ObjectKind { return &r.TypeMeta }
func (r *Release) DeepCopyObject() runtime.Object {
	c := *r
	r.ObjectMeta.DeepCopyInto(&c.ObjectMeta)
	return &c
}

// ReleaseList is the list the release API returns (one non-paged list: every release).
type ReleaseList struct {
	metav1.TypeMeta
	metav1.ListMeta
	Items []*Release
}

func (l *ReleaseList) GetObjectKind() schema.ObjectKind { return &l.TypeMeta }
func (l *ReleaseList) DeepCopyObject() runtime.Object {
	c := *l
	l.ListMeta.DeepCopyInto(&c.ListMeta)
	c.Items = make([]*Release, len(l.Items))
	for i, it := range l.Items {
		c.Items[i] = it.DeepCopyObject().(*Release)
	}
	return &c
}

// ReleaseUID is the stable row id of a release.
func ReleaseUID(ns, name string) string { return "helm:" + ns + "/" + name }

// Tone maps a Helm status to a row tone.
func Tone(status string) uint8 {
	switch status {
	case "deployed":
		return ingest.ToneSuccess
	case "failed":
		return ingest.ToneDanger
	case "pending-install", "pending-upgrade", "pending-rollback", "unknown":
		return ingest.ToneWarning
	case "superseded":
		return ingest.ToneInfo
	}
	return ingest.ToneNeutral // uninstalling, uninstalled
}

func project(r *Release) *ingest.Row {
	row := ingest.NewRow(string(r.UID), r.Name, r.ResourceVersion, r.Namespace, r.UpdatedMs, false, 4)
	row.PutN(int64(r.Revision))
	row.PutS(r.Status)
	row.PutN(r.UpdatedMs)
	row.PutS(r.ManagedBy)
	row.SetTone(Tone(r.Status))
	return row
}

// Module is the registered release-list module.
var Module = &ingest.Module{
	GVR:         protocol.HelmGVR,
	Kind:        "Release",
	Namespaced:  true,
	Curated:     true,
	Virtual:     true,
	NoWatchList: true,
	Tier0:       true, // the release metadata watch is part of the problems baseline (helm.failed, helm.stuck-pending)
	Cols: []ingest.Col{
		ingest.ColCluster(), ingest.ColName(), ingest.ColNS(),
		ingest.ColInt("revision", "Revision", 80),
		ingest.ColDict("status", "Status", 110),
		ingest.ColTimeCell("updated", "Updated", 80),
		ingest.ColDict("managedBy", "Managed by", 180),
		ingest.ColTone(),
	},
	NewAPI:  func(c rest.Interface, _, _, _ string) ingest.API { return NewReleaseAPI(NewRESTMetaSource(c)) },
	NewList: func() runtime.Object { return &ReleaseList{} },
	Each: func(l runtime.Object, fn func(*ingest.Row)) {
		if rl, ok := l.(*ReleaseList); ok {
			for _, it := range rl.Items {
				fn(project(it))
			}
		}
	},
	ProjectObj: func(o runtime.Object) *ingest.Row {
		if r, ok := o.(*Release); ok {
			return project(r)
		}
		return nil
	},
}

func init() { ingest.Register(Module) }

// revision is one release Secret's label-derived facts.
type revision struct {
	ns, name  string
	version   int
	status    string
	updatedMs int64
	managedBy string
	fluxName  string
	fluxNS    string
	uid, rv   string
}

// fluxOwner returns "flux:<ns>/<name>" from the Helm release labels, or "".
func fluxOwner(lbl map[string]string) string {
	n := lbl[FluxNameLabel]
	if n == "" {
		return ""
	}
	return "flux:" + lbl[FluxNSLabel] + "/" + n
}

// revisionOf reads a release Secret's metadata; ok is false for Secrets that are not Helm releases.
func revisionOf(m *metav1.PartialObjectMetadata) (revision, bool) {
	l := m.Labels
	if l[LabelOwner] != "helm" || l[LabelName] == "" {
		return revision{}, false
	}
	v, err := strconv.Atoi(l[LabelVersion])
	if err != nil {
		return revision{}, false
	}
	sec := l[LabelModified]
	if sec == "" {
		sec = l[LabelCreatedAt]
	}
	var ms int64
	if n, err := strconv.ParseInt(sec, 10, 64); err == nil {
		ms = n * 1000
	} else if !m.CreationTimestamp.IsZero() {
		ms = m.CreationTimestamp.UnixMilli()
	}
	return revision{ns: m.Namespace, name: l[LabelName], version: v, status: l[LabelStatus], updatedMs: ms,
		managedBy: fluxOwner(l), fluxName: l[FluxNameLabel], fluxNS: l[FluxNSLabel], uid: string(m.UID), rv: m.ResourceVersion}, true
}

func (rv revision) release(eventRV string) *Release {
	return &Release{
		TypeMeta:   metav1.TypeMeta{APIVersion: "helm.sh/v1", Kind: "Release"},
		ObjectMeta: metav1.ObjectMeta{Name: rv.name, Namespace: rv.ns, UID: types.UID(ReleaseUID(rv.ns, rv.name)), ResourceVersion: eventRV},
		Revision:   rv.version, Status: rv.status, UpdatedMs: rv.updatedMs, ManagedBy: rv.managedBy,
	}
}
