package platform

import (
	"fmt"
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"k8s.io/apimachinery/pkg/runtime"
)

// Hook is one webhook of a configuration that calls a Service.
type Hook struct {
	Name          string
	FailurePolicy string // "Fail" (the API default) | "Ignore"
	NS, Service   string
}

// WebhookFacts lists the service-backed webhooks of a configuration (nil when it has none).
type WebhookFacts struct{ Hooks []Hook }

// Webhook row cells: webhooks, failClosed.
const (
	GRValidating = "admissionregistration.k8s.io/validatingwebhookconfigurations"
	GRMutating   = "admissionregistration.k8s.io/mutatingwebhookconfigurations"
)

func decodeWebhooks(raw []byte) (runtime.Object, error) {
	type hook struct {
		Name          string `json:"name"`
		FailurePolicy string `json:"failurePolicy"`
		ClientConfig  struct {
			Service *struct {
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
			} `json:"service"`
		} `json:"clientConfig"`
	}
	var w struct {
		meta
		Webhooks []hook `json:"webhooks"`
	}
	if err := decodeInto(raw, &w); err != nil {
		return nil, err
	}
	f := &WebhookFacts{}
	closed := 0
	for _, h := range w.Webhooks {
		fp := h.FailurePolicy
		if fp == "" {
			fp = "Fail"
		}
		if fp == "Fail" {
			closed++
		}
		if s := h.ClientConfig.Service; s != nil {
			f.Hooks = append(f.Hooks, Hook{Name: h.Name, FailurePolicy: fp, NS: s.Namespace, Service: s.Name})
		}
	}
	r := w.newRow(2)
	r.PutN(int64(len(w.Webhooks)))
	r.PutN(int64(closed))
	if len(f.Hooks) > 0 {
		r.SetFacts(f)
	}
	return w.obj(r), nil
}

func hookEval(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
	f, _ := r.Facts().(*WebhookFacts)
	if f == nil {
		return nil
	}
	var bad []string
	var refs []problems.Ref
	for _, h := range f.Hooks {
		if h.FailurePolicy != "Fail" {
			continue
		}
		if ready, _, _ := c.ReadyEndpoints(h.NS, h.Service); ready == 0 {
			bad = append(bad, fmt.Sprintf("%s -> Service %s/%s has no ready endpoints", h.Name, h.NS, h.Service))
			refs = append(refs, problems.Ref{Kind: "Service", NS: h.NS, Name: h.Service, Note: "no ready endpoints"})
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sub := c.SubjectOf(k.GR, r)
	return []problems.Draft{{Subject: sub, Title: fmt.Sprintf("%s %s: failurePolicy=Fail but its webhook Service has no ready endpoints", sub.Kind, sub.Name),
		Message: strings.Join(bad, "; "), Count: len(bad), Evidence: refs, Grace: 30 * time.Second}}
}

// hookDependents re-evaluates the configurations that call the Service of a changed EndpointSlice.
func hookDependents(c *problems.Ctx, k problems.Key, old, new *ingest.Row) []problems.Key {
	r := new
	if r == nil {
		r = old
	}
	svc := r.StrCell(0) // endpointslice cell 0: service name
	if svc == "" {
		return nil
	}
	var out []problems.Key
	for _, gr := range []string{GRValidating, GRMutating} {
		c.Each(gr, func(ns, name string, row *ingest.Row) bool {
			if f, _ := row.Facts().(*WebhookFacts); f != nil {
				for _, h := range f.Hooks {
					if h.NS == k.NS && h.Service == svc {
						out = append(out, problems.Key{GR: gr, NS: ns, Name: name})
						break
					}
				}
			}
			return true
		})
	}
	return out
}

func init() {
	cols := []ingest.Col{ingest.ColInt("webhooks", "Webhooks", 90), ingest.ColInt("failClosed", "Fail-closed", 100), ingest.ColAge()}
	register(kind{group: "admissionregistration.k8s.io", resource: "validatingwebhookconfigurations", kind: "ValidatingWebhookConfiguration", versions: []string{"v1"}, cols: cols, decode: decodeWebhooks})
	register(kind{group: "admissionregistration.k8s.io", resource: "mutatingwebhookconfigurations", kind: "MutatingWebhookConfiguration", versions: []string{"v1"}, cols: cols, decode: decodeWebhooks})
	problems.Register(&problems.Detector{Class: "webhook.failing", Family: problems.FamPlatform, Default: true, Severity: problems.SevCritical,
		Title: "Fail-closed admission webhooks without endpoints", Inputs: []string{GRValidating, GRMutating}, Triggers: []string{problems.GRSlices},
		Evaluate: hookEval, Dependents: hookDependents})
}
