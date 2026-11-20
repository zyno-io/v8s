// Package vpa holds the curated VerticalPodAutoscaler module: target, update mode, recommendation status and the
// pod-level CPU / memory recommendation (summed over the containers). It is read as typed partial JSON (ingest
// mode B) and is not part of the baseline: the kind is watched only while a VPA view is open.
package vpa

import (
	"encoding/json"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// GVR is the module's key.
const GVR = "autoscaling.k8s.io/v1/verticalpodautoscalers"

// Row cells: target, mode, status, cpuTarget, cpuLower, cpuUpper, memTarget, memLower, memUpper.
// The six recommendation cells are -1 when there is no recommendation for that resource.

// Obj is a decoded object: its metadata for the reflector and the row projected from it.
type Obj struct {
	metav1.TypeMeta
	metav1.ObjectMeta
	row *ingest.Row
}

func (o *Obj) GetObjectKind() schema.ObjectKind { return &o.TypeMeta }
func (o *Obj) DeepCopyObject() runtime.Object {
	c := *o
	o.ObjectMeta.DeepCopyInto(&c.ObjectMeta)
	return &c
}

type condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type recommendation struct {
	ContainerName string `json:"containerName"`
	// resource.Quantity: the CRD allows a quantity as a string or a number (int-or-string).
	Target     map[string]resource.Quantity `json:"target"`
	LowerBound map[string]resource.Quantity `json:"lowerBound"`
	UpperBound map[string]resource.Quantity `json:"upperBound"`
}

type wire struct {
	Metadata struct {
		Name              string       `json:"name"`
		Namespace         string       `json:"namespace"`
		UID               types.UID    `json:"uid"`
		ResourceVersion   string       `json:"resourceVersion"`
		CreationTimestamp metav1.Time  `json:"creationTimestamp"`
		DeletionTimestamp *metav1.Time `json:"deletionTimestamp"`
	} `json:"metadata"`
	Spec struct {
		TargetRef struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"targetRef"`
		UpdatePolicy struct {
			UpdateMode string `json:"updateMode"`
		} `json:"updatePolicy"`
	} `json:"spec"`
	Status struct {
		Recommendation struct {
			ContainerRecommendations []recommendation `json:"containerRecommendations"`
		} `json:"recommendation"`
		Conditions []condition `json:"conditions"`
	} `json:"status"`
}

// sum adds one resource over the containers: millicores (cpu) or MiB (memory); -1 when no container has it.
func sum(recs []recommendation, pick func(*recommendation) map[string]resource.Quantity, res string, mem bool) int64 {
	total, found := int64(0), false
	for i := range recs {
		q, ok := pick(&recs[i])[res]
		if !ok {
			continue
		}
		found = true
		if mem {
			total += q.Value() // bytes; floored to MiB once, over the sum
		} else {
			total += q.MilliValue()
		}
	}
	if !found {
		return -1
	}
	if mem {
		return total >> 20
	}
	return total
}

func condTrue(cs []condition, typ string) bool {
	for i := range cs {
		if cs[i].Type == typ {
			return cs[i].Status == "True"
		}
	}
	return false
}

// statusOf maps the conditions to the status text and tone.
func statusOf(cs []condition) (string, uint8) {
	switch {
	case condTrue(cs, "ConfigUnsupported"):
		return "ConfigUnsupported", ingest.ToneDanger
	case condTrue(cs, "NoPodsMatched"):
		return "NoPodsMatched", ingest.ToneWarning
	case condTrue(cs, "RecommendationProvided"):
		if condTrue(cs, "LowConfidence") {
			return "LowConfidence", ingest.ToneWarning
		}
		return "Provided", ingest.ToneSuccess
	}
	return "Pending", ingest.ToneNeutral
}

func decode(raw []byte) (runtime.Object, error) {
	var w wire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	m := &w.Metadata
	var created int64
	if !m.CreationTimestamp.IsZero() {
		created = m.CreationTimestamp.UnixMilli()
	}
	r := ingest.NewRow(string(m.UID), m.Name, m.ResourceVersion, m.Namespace, created, m.DeletionTimestamp != nil, 9)
	target := ""
	if t := w.Spec.TargetRef; t.Kind != "" || t.Name != "" {
		target = t.Kind + "/" + t.Name
	}
	r.PutS(target)
	mode := w.Spec.UpdatePolicy.UpdateMode
	if mode == "" {
		mode = "Auto"
	}
	r.PutS(mode)
	status, tone := statusOf(w.Status.Conditions)
	r.PutS(status)
	r.SetTone(tone)
	recs := w.Status.Recommendation.ContainerRecommendations
	tgt := func(x *recommendation) map[string]resource.Quantity { return x.Target }
	lower := func(x *recommendation) map[string]resource.Quantity { return x.LowerBound }
	upper := func(x *recommendation) map[string]resource.Quantity { return x.UpperBound }
	r.PutN(sum(recs, tgt, "cpu", false))
	r.PutN(sum(recs, lower, "cpu", false))
	r.PutN(sum(recs, upper, "cpu", false))
	r.PutN(sum(recs, tgt, "memory", true))
	r.PutN(sum(recs, lower, "memory", true))
	r.PutN(sum(recs, upper, "memory", true))
	return &Obj{ObjectMeta: metav1.ObjectMeta{Name: m.Name, Namespace: m.Namespace, UID: m.UID, ResourceVersion: m.ResourceVersion,
		CreationTimestamp: m.CreationTimestamp}, row: r}, nil
}

func module() *ingest.Module {
	m := &ingest.Module{GVR: GVR, Kind: "VerticalPodAutoscaler", Namespaced: true, Short: []string{"vpa"},
		Curated: true, Optional: true, Tier0: false, NoWatchList: true,
		Cols: []ingest.Col{ingest.ColCluster(), ingest.ColName(), ingest.ColNS(),
			ingest.ColStr("target", "Target", 200), ingest.ColDict("mode", "Mode", 80), ingest.ColDict("status", "Status", 140), ingest.ColTone(),
			ingest.ColUnitInt("cpuTarget", "CPU", "", "millicores", 44),
			ingest.ColUnitInt("cpuLower", "CPU lower", "low", "millicores", 44),
			ingest.ColUnitInt("cpuUpper", "CPU upper", "up", "millicores", 44),
			ingest.ColUnitInt("memTarget", "MEM", "", "MiB", 48),
			ingest.ColUnitInt("memLower", "MEM lower", "low", "MiB", 48),
			ingest.ColUnitInt("memUpper", "MEM upper", "up", "MiB", 48),
			ingest.ColAge()},
		NewAPI: ingest.NewPartialAPI(decode), DecodeJSON: decode}
	m.ProjectObj = func(o runtime.Object) *ingest.Row {
		if x, ok := o.(*Obj); ok {
			return x.row
		}
		return nil
	}
	m.Each = ingest.PartialEach(m)
	return m
}

func init() { ingest.Register(module()) }
