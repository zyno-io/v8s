// Package kyverno is the Kyverno module: curated views of the CEL policies (policies.kyverno.io/v1) and the
// legacy kyverno.io/v1 policies (read-only: Kyverno >= 1.20 no longer executes them), server-side Table
// watches of the policy reports (wgpolicyk8s.io and openreports.io) for the per-resource counts, lazy report
// details, the Audit/Enforce switch, exception templates and the policy.* problem classes.
package kyverno

import (
	"encoding/json"
	"strings"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// API groups.
const (
	GroupCEL    = "policies.kyverno.io"
	GroupLegacy = "kyverno.io"
	GroupWG     = "wgpolicyk8s.io"
	GroupOR     = "openreports.io"
)

// policyKind describes one policy kind.
type policyKind struct {
	Group, Version, Kind, Resource string
	Short                          []string
	Namespaced                     bool
	// Tier0 kinds feed policy.not-ready and are watched with the baseline; the rest are watched on demand.
	Tier0  bool
	Legacy bool
}

var policyKinds = []policyKind{
	{GroupCEL, "v1", "ValidatingPolicy", "validatingpolicies", []string{"vpol"}, false, true, false},
	{GroupCEL, "v1", "NamespacedValidatingPolicy", "namespacedvalidatingpolicies", []string{"nvpol"}, true, true, false},
	{GroupCEL, "v1", "ImageValidatingPolicy", "imagevalidatingpolicies", []string{"ivpol"}, false, false, false},
	{GroupCEL, "v1", "NamespacedImageValidatingPolicy", "namespacedimagevalidatingpolicies", []string{"nivpol"}, true, false, false},
	{GroupCEL, "v1", "MutatingPolicy", "mutatingpolicies", []string{"mpol"}, false, false, false},
	{GroupCEL, "v1", "NamespacedMutatingPolicy", "namespacedmutatingpolicies", []string{"nmpol"}, true, false, false},
	{GroupCEL, "v1", "GeneratingPolicy", "generatingpolicies", []string{"gpol"}, false, false, false},
	{GroupCEL, "v1", "NamespacedGeneratingPolicy", "namespacedgeneratingpolicies", []string{"ngpol"}, true, false, false},
	{GroupCEL, "v1", "DeletingPolicy", "deletingpolicies", []string{"dpol"}, false, false, false},
	{GroupCEL, "v1", "NamespacedDeletingPolicy", "namespaceddeletingpolicies", []string{"ndpol"}, true, false, false},
	{GroupCEL, "v1", "PolicyException", "policyexceptions", []string{"polex"}, true, false, false},
	{GroupLegacy, "v1", "ClusterPolicy", "clusterpolicies", []string{"cpol"}, false, true, true},
	{GroupLegacy, "v1", "Policy", "policies", []string{"pol"}, true, true, true},
}

func policyKindOf(group, resource string) *policyKind {
	for i := range policyKinds {
		if policyKinds[i].Group == group && policyKinds[i].Resource == resource {
			return &policyKinds[i]
		}
	}
	return nil
}

// ModeOf is the Audit/Enforce/Mixed mode of a policy.
type Mode = string

// Modes.
const (
	ModeAudit   = "Audit"
	ModeEnforce = "Enforce"
	ModeMixed   = "Mixed"
)

// Policy is the partial decode of any Kyverno policy kind (mode B): only what the projection reads.
type Policy struct {
	metav1.TypeMeta
	metav1.ObjectMeta
	Mode       string
	Legacy     bool
	Ready      string // "True" | "False" | ""
	ReadyMsg   string
	ReadySince int64
	Rules      int
	Background string // "true" | "false" | ""
	Severity   string
}

func (p *Policy) DeepCopyObject() runtime.Object {
	c := *p
	p.ObjectMeta.DeepCopyInto(&c.ObjectMeta)
	return &c
}

type wcond struct {
	Type               string      `json:"type"`
	Status             string      `json:"status"`
	Reason             string      `json:"reason"`
	Message            string      `json:"message"`
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`
}

type wire struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Metadata   struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		UID               types.UID         `json:"uid"`
		ResourceVersion   string            `json:"resourceVersion"`
		CreationTimestamp metav1.Time       `json:"creationTimestamp"`
		DeletionTimestamp *metav1.Time      `json:"deletionTimestamp"`
		Annotations       map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		ValidationActions       []string          `json:"validationActions"`
		ValidationFailureAction string            `json:"validationFailureAction"`
		Background              *bool             `json:"background"`
		Validations             []json.RawMessage `json:"validations"`
		Rules                   []struct {
			Name     string `json:"name"`
			Validate *struct {
				FailureAction string `json:"failureAction"`
			} `json:"validate"`
		} `json:"rules"`
		PolicyRefs []json.RawMessage `json:"policyRefs"`
	} `json:"spec"`
	Status struct {
		Ready           *bool   `json:"ready"`
		Conditions      []wcond `json:"conditions"`
		ConditionStatus *struct {
			Ready      *bool   `json:"ready"`
			Message    string  `json:"message"`
			Conditions []wcond `json:"conditions"`
		} `json:"conditionStatus"`
	} `json:"status"`
}

func canon(s string) string {
	switch strings.ToLower(s) {
	case "enforce", "deny":
		return ModeEnforce
	case "audit", "warn":
		return ModeAudit
	}
	return ""
}

// celMode: Deny among the validationActions enforces; otherwise Audit/Warn audit.
func celMode(actions []string) string {
	mode := ""
	for _, a := range actions {
		switch strings.ToLower(a) {
		case "deny":
			return ModeEnforce
		case "audit", "warn":
			mode = ModeAudit
		}
	}
	return mode
}

// legacyMode computes the mode from the per-rule validate.failureAction, falling back to the deprecated spec-level
// validationFailureAction (the per-rule field overrides it, so reading only the spec field can show Enforce while
// rules still audit).
func legacyMode(w *wire) string {
	spec := canon(w.Spec.ValidationFailureAction)
	seen := map[string]bool{}
	for _, r := range w.Spec.Rules {
		if r.Validate == nil {
			continue
		}
		m := canon(r.Validate.FailureAction)
		if m == "" {
			m = spec
		}
		if m == "" {
			m = ModeAudit // Kyverno's default
		}
		seen[m] = true
	}
	switch {
	case len(seen) > 1:
		return ModeMixed
	case seen[ModeEnforce]:
		return ModeEnforce
	case seen[ModeAudit]:
		return ModeAudit
	}
	return spec
}

// Decode decodes one policy of any kind from its JSON (list items and watch events).
func Decode(raw []byte) (runtime.Object, error) {
	var w wire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	group := w.APIVersion
	if i := strings.IndexByte(group, '/'); i >= 0 {
		group = group[:i]
	}
	p := &Policy{
		TypeMeta: metav1.TypeMeta{Kind: w.Kind, APIVersion: w.APIVersion},
		ObjectMeta: metav1.ObjectMeta{Name: w.Metadata.Name, Namespace: w.Metadata.Namespace, UID: w.Metadata.UID, ResourceVersion: w.Metadata.ResourceVersion,
			CreationTimestamp: w.Metadata.CreationTimestamp, DeletionTimestamp: w.Metadata.DeletionTimestamp},
		Legacy:   group == GroupLegacy,
		Severity: w.Metadata.Annotations["policies.kyverno.io/severity"],
	}
	if w.Spec.Background != nil {
		p.Background = "false"
		if *w.Spec.Background {
			p.Background = "true"
		}
	}
	switch {
	case p.Legacy:
		p.Mode = legacyMode(&w)
		p.Rules = len(w.Spec.Rules)
	case strings.Contains(w.Kind, "ValidatingPolicy"):
		p.Mode = celMode(w.Spec.ValidationActions)
		p.Rules = len(w.Spec.Validations)
		if p.Rules == 0 {
			p.Rules = 1
		}
	default:
		p.Rules = len(w.Spec.Rules)
	}
	conds := w.Status.Conditions
	var ready *bool = w.Status.Ready
	if cs := w.Status.ConditionStatus; cs != nil {
		ready = cs.Ready
		conds = append(conds, cs.Conditions...)
		p.ReadyMsg = cs.Message
	}
	for i := range conds {
		if c := &conds[i]; c.Type == "Ready" && ready == nil {
			b := c.Status == "True"
			ready = &b
		}
	}
	if ready != nil {
		p.Ready = "False"
		if *ready {
			p.Ready = "True"
		}
		if !*ready {
			for i := range conds {
				if c := &conds[i]; c.Status != "True" && c.Status != "" {
					p.ReadyMsg, p.ReadySince = c.Message, c.LastTransitionTime.UnixMilli()
					if c.LastTransitionTime.IsZero() {
						p.ReadySince = 0
					}
					break
				}
			}
		}
	}
	return p, nil
}

// PolicyFacts is attached to a policy row for the policy.not-ready detector. Nil for ready policies.
type PolicyFacts struct {
	Ready   ingest.CondFact
	Legacy  bool
	Message string
}

// Cell indexes of the policy modules (after cluster, name and namespace).
const (
	cellMode = iota
	cellReadOnly
	cellReady
	cellRules
	cellBackground
	cellSeverity
	cellMessage
	numPolicyCells
)

const maxMsg = 240

func trunc(s string) string {
	if len(s) <= maxMsg {
		return s
	}
	return strings.ToValidUTF8(s[:maxMsg], "") + "..."
}

// Project builds the row of one policy.
func Project(p *Policy) *ingest.Row {
	var created int64
	if !p.CreationTimestamp.IsZero() {
		created = p.CreationTimestamp.UnixMilli()
	}
	r := ingest.NewRow(string(p.UID), p.Name, p.ResourceVersion, p.Namespace, created, p.DeletionTimestamp != nil, numPolicyCells)
	ro := ""
	if p.Legacy {
		ro = "read-only"
	}
	r.PutS(p.Mode)
	r.PutS(ro)
	r.PutS(p.Ready)
	r.PutN(int64(p.Rules))
	r.PutS(p.Background)
	r.PutS(p.Severity)
	r.PutS(trunc(p.ReadyMsg))
	switch {
	case p.Ready == "False":
		r.SetTone(ingest.ToneDanger)
	case p.Legacy:
		r.SetTone(ingest.ToneInfo)
	case p.Ready == "True":
		r.SetTone(ingest.ToneSuccess)
	default:
		r.SetTone(ingest.ToneWarning)
	}
	if p.Ready == "False" {
		r.SetFacts(&PolicyFacts{Ready: ingest.CondFact{Status: "False", Message: trunc(p.ReadyMsg), Since: p.ReadySince}, Legacy: p.Legacy, Message: trunc(p.ReadyMsg)})
	}
	return r
}

func policyCols(namespaced bool) []ingest.Col {
	cols := []ingest.Col{ingest.ColCluster(), ingest.ColName()}
	if namespaced {
		cols = append(cols, ingest.ColNS())
	}
	return append(cols, ingest.ColDict("mode", "Mode", 90), ingest.ColDict("readOnly", "Read-only", 90), ingest.ColDict("ready", "Ready", 80),
		ingest.ColInt("rules", "Rules", 64), ingest.ColDict("background", "Background", 90), ingest.ColDict("severity", "Severity", 90),
		ingest.ColStr("message", "Message", 360), ingest.ColTone(), ingest.ColAge())
}

func newPolicyModule(k *policyKind, version string) *ingest.Module {
	m := &ingest.Module{
		GVR: k.Group + "/" + version + "/" + k.Resource, Kind: k.Kind, Namespaced: k.Namespaced, Short: k.Short,
		Curated: true, Optional: true, Tier0: k.Tier0, NoWatchList: true, Cols: policyCols(k.Namespaced),
		NewAPI: ingest.NewPartialAPI(Decode), DecodeJSON: Decode,
	}
	m.ProjectObj = func(o runtime.Object) *ingest.Row {
		if x, ok := o.(*Policy); ok {
			return Project(x)
		}
		return nil
	}
	m.Each = ingest.PartialEach(m)
	return m
}

var _ = protocol.SourceKyverno
