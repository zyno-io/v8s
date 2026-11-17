package hubble

import (
	"context"
	"strings"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
	netpolGVR  = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}
	cnpGVR     = schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
	ccnpGVR    = schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"}
	selectorKV = []string{"k8s:", "any:"}
)

func stripLabelPrefix(k string) string {
	for _, p := range selectorKV {
		k = strings.TrimPrefix(k, p)
	}
	return k
}

func matchLabelsOf(o *unstructured.Unstructured, path ...string) map[string]string {
	m, _, _ := unstructured.NestedMap(o.Object, path...)
	ml, _ := m["matchLabels"].(map[string]any)
	out := map[string]string{}
	for k, v := range ml {
		if s, ok := v.(string); ok {
			out[stripLabelPrefix(k)] = s
		}
	}
	return out
}

func hasKey(o *unstructured.Unstructured, keys ...string) bool {
	for _, k := range keys {
		if v, ok := o.Object["spec"].(map[string]any)[k]; ok && v != nil {
			if l, isList := v.([]any); !isList || len(l) > 0 {
				return true
			}
		}
	}
	return false
}

// summarizeNetworkPolicy reads the fields the drops view needs from a networking.k8s.io NetworkPolicy.
func summarizeNetworkPolicy(o *unstructured.Unstructured) PolicySummary {
	p := PolicySummary{Kind: "NetworkPolicy", Namespace: o.GetNamespace(), Name: o.GetName(), Selector: matchLabelsOf(o, "spec", "podSelector")}
	types, _, _ := unstructured.NestedStringSlice(o.Object, "spec", "policyTypes")
	if len(types) == 0 {
		types = []string{"Ingress"}
		if hasKey(o, "egress") {
			types = append(types, "Egress")
		}
	}
	p.Types = types
	return p
}

// summarizeCilium reads a CiliumNetworkPolicy or CiliumClusterwideNetworkPolicy (spec or specs[]).
func summarizeCilium(o *unstructured.Unstructured, kind string) PolicySummary {
	p := PolicySummary{Kind: kind, Namespace: o.GetNamespace(), Name: o.GetName(), Selector: matchLabelsOf(o, "spec", "endpointSelector")}
	var types []string
	if hasKey(o, "ingress", "ingressDeny") {
		types = append(types, "Ingress")
	}
	if hasKey(o, "egress", "egressDeny") {
		types = append(types, "Egress")
	}
	p.Types = types
	return p
}

// dynPolicies lists policies on demand per namespace (a handful of requests per drops view).
type dynPolicies struct {
	ctx context.Context
	dyn dynamic.Interface
	mu  sync.Mutex
	ns  map[string][]PolicySummary
	cw  []PolicySummary
	cwL bool
}

func (d *dynPolicies) Policies(ns string) []PolicySummary {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.cwL {
		d.cwL = true
		if l, err := d.dyn.Resource(ccnpGVR).List(d.ctx, metav1.ListOptions{}); err == nil {
			for i := range l.Items {
				d.cw = append(d.cw, summarizeCilium(&l.Items[i], "CiliumClusterwideNetworkPolicy"))
			}
		}
	}
	if ps, ok := d.ns[ns]; ok {
		return append(append([]PolicySummary(nil), ps...), d.cw...)
	}
	var ps []PolicySummary
	if l, err := d.dyn.Resource(netpolGVR).Namespace(ns).List(d.ctx, metav1.ListOptions{}); err == nil {
		for i := range l.Items {
			ps = append(ps, summarizeNetworkPolicy(&l.Items[i]))
		}
	}
	if l, err := d.dyn.Resource(cnpGVR).Namespace(ns).List(d.ctx, metav1.ListOptions{}); err == nil {
		for i := range l.Items {
			ps = append(ps, summarizeCilium(&l.Items[i], "CiliumNetworkPolicy"))
		}
	}
	if d.ns == nil {
		d.ns = map[string][]PolicySummary{}
	}
	d.ns[ns] = ps
	return append(append([]PolicySummary(nil), ps...), d.cw...)
}

func (s *Service) policyLister(ctx context.Context, cluster string) PolicyLister {
	if s.cfg.Policies != nil {
		return s.cfg.Policies(ctx, cluster)
	}
	if s.cfg.Conn == nil {
		return nil
	}
	c, err := s.cfg.Conn(cluster)
	if err != nil {
		return nil
	}
	return &dynPolicies{ctx: ctx, dyn: c.Dynamic()}
}
