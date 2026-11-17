package platform

import (
	"fmt"
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"k8s.io/apimachinery/pkg/runtime"
)

// Gateway API kinds (group gateway.networking.k8s.io), version-less keys.
const (
	GRHTTPRoutes = "gateway.networking.k8s.io/httproutes"
	GRGRPCRoutes = "gateway.networking.k8s.io/grpcroutes"
	GRGateways   = "gateway.networking.k8s.io/gateways"
)

// RouteIssue is a route parent condition that is False (Accepted or ResolvedRefs).
type RouteIssue struct {
	Parent, Type, Reason, Message string
	Since                         int64
}

// RouteFacts lists the failing parent conditions of a route (nil when all are fine).
type RouteFacts struct{ Issues []RouteIssue }

// GatewayFacts: Accepted and Programmed conditions of a Gateway.
type GatewayFacts struct{ Accepted, Programmed bool }

func decodeRoute(raw []byte) (runtime.Object, error) {
	var w struct {
		meta
		Status struct {
			Parents []struct {
				ParentRef struct {
					Namespace string `json:"namespace"`
					Name      string `json:"name"`
				} `json:"parentRef"`
				Conditions []condition `json:"conditions"`
			} `json:"parents"`
		} `json:"status"`
	}
	if err := decodeInto(raw, &w); err != nil {
		return nil, err
	}
	f := &RouteFacts{}
	parents := 0
	for _, p := range w.Status.Parents {
		parents++
		for i := range p.Conditions {
			c := &p.Conditions[i]
			if (c.Type == "Accepted" || c.Type == "ResolvedRefs") && c.Status == "False" {
				name := p.ParentRef.Name
				if p.ParentRef.Namespace != "" {
					name = p.ParentRef.Namespace + "/" + name
				}
				cf := c.fact()
				f.Issues = append(f.Issues, RouteIssue{Parent: name, Type: c.Type, Reason: c.Reason, Message: cf.Message, Since: cf.Since})
			}
		}
	}
	r := w.newRow(2)
	r.PutN(int64(parents))
	status := "Accepted"
	r.SetTone(ingest.ToneSuccess)
	if len(f.Issues) > 0 {
		status = f.Issues[0].Type + "=False"
		r.SetTone(ingest.ToneDanger)
		r.SetFacts(f)
	}
	r.PutS(status)
	return w.obj(r), nil
}

func decodeGateway(raw []byte) (runtime.Object, error) {
	var w struct {
		meta
		Status struct {
			Conditions []condition `json:"conditions"`
		} `json:"status"`
	}
	if err := decodeInto(raw, &w); err != nil {
		return nil, err
	}
	f := &GatewayFacts{Accepted: findCond(w.Status.Conditions, "Accepted").Status == "True", Programmed: findCond(w.Status.Conditions, "Programmed").Status == "True"}
	r := w.newRow(1)
	st, tone := "Programmed", ingest.ToneSuccess
	switch {
	case !f.Accepted:
		st, tone = "NotAccepted", ingest.ToneDanger
	case !f.Programmed:
		st, tone = "NotProgrammed", ingest.ToneWarning
	}
	r.PutS(st)
	r.SetTone(tone)
	r.SetFacts(f)
	return w.obj(r), nil
}

func evalRoute(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
	f, _ := r.Facts().(*RouteFacts)
	if f == nil || len(f.Issues) == 0 {
		return nil
	}
	sub := c.SubjectOf(k.GR, r)
	var parts []string
	var since int64
	for _, is := range f.Issues {
		parts = append(parts, fmt.Sprintf("%s=False for %s: %s %s", is.Type, is.Parent, is.Reason, is.Message))
		if since == 0 || (is.Since != 0 && is.Since < since) {
			since = is.Since
		}
	}
	return []problems.Draft{{Subject: sub, Title: fmt.Sprintf("%s %s is not accepted", sub.Kind, sub.Name), Message: strings.Join(parts, "; "),
		Since: firstNZ(since, r.CreatedMs()), Grace: time.Minute, Count: len(f.Issues)}}
}

func init() {
	routeCols := []ingest.Col{ingest.ColInt("parents", "Parents", 80), ingest.ColDict("status", "Status", 160), ingest.ColTone(), ingest.ColAge()}
	register(kind{group: "gateway.networking.k8s.io", resource: "httproutes", kind: "HTTPRoute", versions: []string{"v1", "v1beta1"}, namespaced: true, cols: routeCols, decode: decodeRoute})
	register(kind{group: "gateway.networking.k8s.io", resource: "grpcroutes", kind: "GRPCRoute", versions: []string{"v1", "v1alpha2"}, namespaced: true, cols: routeCols, decode: decodeRoute})
	register(kind{group: "gateway.networking.k8s.io", resource: "gateways", kind: "Gateway", versions: []string{"v1", "v1beta1"}, namespaced: true,
		cols: []ingest.Col{ingest.ColDict("status", "Status", 140), ingest.ColTone(), ingest.ColAge()}, decode: decodeGateway})
	problems.Register(&problems.Detector{Class: "gateway.route-unaccepted", Family: problems.FamNetwork, Default: true, Severity: problems.SevError,
		Title: "Gateway API routes not accepted", Inputs: []string{GRHTTPRoutes, GRGRPCRoutes}, Evaluate: evalRoute})
}
