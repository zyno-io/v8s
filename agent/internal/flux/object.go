package flux

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

const (
	AnnRequestedAt = "reconcile.fluxcd.io/requestedAt"
	AnnForceAt     = "reconcile.fluxcd.io/forceAt"
	AnnResetAt     = "reconcile.fluxcd.io/resetAt"
)

// Ref is a sourceRef / chartRef / dependsOn entry.
type Ref struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// Condition is a status condition.
type Condition struct {
	Type               string      `json:"type"`
	Status             string      `json:"status"`
	Reason             string      `json:"reason"`
	Message            string      `json:"message"`
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`
}

// Object is the partial decode of any Flux object: only what the projection reads.
type Object struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	Suspend      bool
	Interval     string
	URL          string
	Source       *Ref
	DependsOn    []Ref
	RequestedAt  string
	Conditions   []Condition
	Applied      string // status.lastAppliedRevision
	Attempted    string // status.lastAttemptedRevision
	ArtifactRev  string // status.artifact.revision
	ChartVersion string // status.history[0].chartVersion
	LastHandled  string // status.lastHandledReconcileAt
	ObservedGen  int64
}

func (o *Object) DeepCopyObject() runtime.Object {
	c := *o
	o.ObjectMeta.DeepCopyInto(&c.ObjectMeta)
	if o.Source != nil {
		s := *o.Source
		c.Source = &s
	}
	c.DependsOn = append([]Ref(nil), o.DependsOn...)
	c.Conditions = append([]Condition(nil), o.Conditions...)
	return &c
}

type wire struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Metadata   struct {
		Name              string       `json:"name"`
		Namespace         string       `json:"namespace"`
		UID               types.UID    `json:"uid"`
		ResourceVersion   string       `json:"resourceVersion"`
		CreationTimestamp metav1.Time  `json:"creationTimestamp"`
		DeletionTimestamp *metav1.Time `json:"deletionTimestamp"`
		Annotations       struct {
			RequestedAt string `json:"reconcile.fluxcd.io/requestedAt"`
		} `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Suspend   bool            `json:"suspend"`
		Interval  string          `json:"interval"`
		URL       string          `json:"url"`
		SourceRef *Ref            `json:"sourceRef"`
		ChartRef  *Ref            `json:"chartRef"`
		Chart     json.RawMessage `json:"chart"` // an object on HelmRelease, a string on HelmChart
		DependsOn []Ref           `json:"dependsOn"`
	} `json:"spec"`
	Status struct {
		Conditions            []Condition `json:"conditions"`
		LastAppliedRevision   string      `json:"lastAppliedRevision"`
		LastAttemptedRevision string      `json:"lastAttemptedRevision"`
		Artifact              *struct {
			Revision string `json:"revision"`
		} `json:"artifact"`
		LastHandledReconcileAt string `json:"lastHandledReconcileAt"`
		ObservedGeneration     int64  `json:"observedGeneration"`
		History                []struct {
			ChartVersion string `json:"chartVersion"`
		} `json:"history"`
	} `json:"status"`
}

// Decode decodes one Flux object from its JSON (list items and watch events).
func Decode(raw []byte) (runtime.Object, error) {
	var w wire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	o := &Object{
		TypeMeta: metav1.TypeMeta{Kind: w.Kind, APIVersion: w.APIVersion},
		ObjectMeta: metav1.ObjectMeta{
			Name: w.Metadata.Name, Namespace: w.Metadata.Namespace, UID: w.Metadata.UID,
			ResourceVersion: w.Metadata.ResourceVersion, CreationTimestamp: w.Metadata.CreationTimestamp,
			DeletionTimestamp: w.Metadata.DeletionTimestamp,
		},
		Suspend:     w.Spec.Suspend,
		Interval:    w.Spec.Interval,
		URL:         w.Spec.URL,
		DependsOn:   w.Spec.DependsOn,
		RequestedAt: w.Metadata.Annotations.RequestedAt,
		Conditions:  w.Status.Conditions,
		Applied:     w.Status.LastAppliedRevision,
		Attempted:   w.Status.LastAttemptedRevision,
		LastHandled: w.Status.LastHandledReconcileAt,
		ObservedGen: w.Status.ObservedGeneration,
	}
	if w.Metadata.Annotations.RequestedAt != "" {
		o.Annotations = map[string]string{AnnRequestedAt: w.Metadata.Annotations.RequestedAt}
	}
	switch {
	case w.Spec.SourceRef != nil:
		o.Source = w.Spec.SourceRef
	case w.Spec.ChartRef != nil:
		o.Source = w.Spec.ChartRef
	default:
		if c := w.Spec.Chart; len(c) > 0 && c[0] == '{' {
			var ch struct {
				Spec struct {
					SourceRef *Ref `json:"sourceRef"`
				} `json:"spec"`
			}
			if json.Unmarshal(c, &ch) == nil {
				o.Source = ch.Spec.SourceRef
			}
		}
	}
	if w.Status.Artifact != nil {
		o.ArtifactRev = w.Status.Artifact.Revision
	}
	if len(w.Status.History) > 0 {
		o.ChartVersion = w.Status.History[0].ChartVersion
	}
	return o, nil
}
