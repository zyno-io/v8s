package kyverno

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/rpc"
	"github.com/zyno-io/v8s/agent/internal/security"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
)

func init() {
	security.RegisterHandler("kyverno.setMode", func(cfg *security.Config) rpc.Handler {
		return func(ctx context.Context, decode func(any) error) (any, error) {
			var p protocol.KyvernoSetModeParams
			if err := decode(&p); err != nil {
				return nil, ops.NewError("bad_request", "params: %s", err.Error())
			}
			return SetMode(ctx, cfg, p)
		}
	})
	security.RegisterHandler("kyverno.exceptionTemplate", func(cfg *security.Config) rpc.Handler {
		return func(ctx context.Context, decode func(any) error) (any, error) {
			var p protocol.KyvernoExceptionTemplateParams
			if err := decode(&p); err != nil {
				return nil, ops.NewError("bad_request", "params: %s", err.Error())
			}
			return ExceptionTemplate(p)
		}
	})
}

// ActionsFor returns the validationActions a policy gets in a mode, keeping a Warn the policy already had.
func ActionsFor(mode string, current []string) ([]string, error) {
	var out []string
	switch mode {
	case ModeEnforce:
		out = []string{"Deny"}
	case ModeAudit:
		out = []string{"Audit"}
	default:
		return nil, ops.NewError("bad_request", "mode must be Audit or Enforce")
	}
	for _, a := range current {
		if strings.EqualFold(a, "warn") {
			out = append(out, "Warn")
		}
	}
	return out, nil
}

// SetMode switches a policy between Audit and Enforce. CEL validating policies change their validationActions;
// legacy kyverno.io/v1 policies are read-only (Kyverno >= 1.20 no longer executes them) and are refused.
func SetMode(ctx context.Context, cfg *security.Config, p protocol.KyvernoSetModeParams) (*protocol.KyvernoSetModeResult, error) {
	ref := p.Ref
	if ref.Cluster == "" || ref.GVR == "" || ref.Name == "" {
		return nil, ops.NewError("bad_request", "ref (cluster, gvr, name) is required")
	}
	group, res := security.GroupOf(ref.GVR), security.ResourceOf(ref.GVR)
	switch {
	case group == GroupLegacy:
		return nil, &ops.Error{Body: protocol.ErrorBody{Code: "forbidden", Message: "legacy kyverno.io/v1 policies are read-only: Kyverno 1.20 and later no longer execute them",
			Hint: "migrate to a CEL policy (policies.kyverno.io/v1 ValidatingPolicy) and switch its mode there"}}
	case group != GroupCEL || !strings.HasSuffix(res, "validatingpolicies"):
		return nil, ops.NewError("bad_request", "%s has no Audit/Enforce mode", ref.GVR)
	case p.Rule != "":
		return nil, ops.NewError("bad_request", "CEL policies switch as a whole: rule applies to legacy policies only")
	}
	ctx = ops.WithUseWrite(ctx, p.UseWriteContext)
	routed, err := cfg.Route(ctx, ref.Cluster, "patch", ref.GVR, "", ref.Namespace, ref.Name)
	if err != nil {
		return nil, err
	}
	g, err := security.ParseGVR(ref.GVR)
	if err != nil {
		return nil, err
	}
	ri := routed.Conn.Dynamic().Resource(g)
	obj, err := security.GetObject(ctx, cfg, ref.Cluster, ref.GVR, ref.Namespace, ref.Name)
	if err != nil {
		return nil, ops.MapError(err, false)
	}
	if ref.UID != "" && string(obj.GetUID()) != ref.UID {
		return nil, ops.NewError("conflict", "object was replaced")
	}
	cur, _, _ := nestedStrings(obj.Object, "spec", "validationActions")
	actions, err := ActionsFor(p.Mode, cur)
	if err != nil {
		return nil, err
	}
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"validationActions": actions}})
	var out interface{ GetResourceVersion() string }
	if ref.Namespace != "" {
		out, err = ri.Namespace(ref.Namespace).Patch(ctx, ref.Name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "v8s"})
	} else {
		out, err = ri.Patch(ctx, ref.Name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "v8s"})
	}
	if err != nil {
		return nil, ops.MapError(err, routed.ViaWrite)
	}
	return &protocol.KyvernoSetModeResult{UsedContext: routed.ContextID, ResourceVersion: out.GetResourceVersion(), Mode: p.Mode}, nil
}

func nestedStrings(o map[string]any, path ...string) ([]string, bool, error) {
	var cur any = o
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		cur = m[p]
	}
	l, ok := cur.([]any)
	if !ok {
		return nil, false, nil
	}
	var out []string
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out, true, nil
}

var nonName = regexp.MustCompile(`[^a-z0-9.-]+`)

func exceptionName(parts ...string) string {
	n := strings.Trim(nonName.ReplaceAllString(strings.ToLower(strings.Join(parts, "-")), "-"), "-.")
	if len(n) > 63 {
		n = strings.Trim(n[:63], "-.")
	}
	return n
}

// ExceptionTemplate returns the YAML of a PolicyException for a policy violation, prefilled with the policy, rule and
// resource: policies.kyverno.io/v1 for CEL policies (matchConditions on the resource), kyverno.io/v2 for legacy
// ClusterPolicy / Policy. The UI opens it in the editor for review, since exceptions are security decisions.
func ExceptionTemplate(p protocol.KyvernoExceptionTemplateParams) (*protocol.KyvernoExceptionTemplateResult, error) {
	pol, res := p.Policy, p.Resource
	if pol.Name == "" || res.Kind == "" || res.Name == "" {
		return nil, ops.NewError("bad_request", "policy.name, resource.kind and resource.name are required")
	}
	group, resource := security.GroupOf(pol.GVR), security.ResourceOf(pol.GVR)
	k := policyKindOf(group, resource)
	if k == nil || k.Kind == "PolicyException" {
		return nil, ops.NewError("bad_request", "ref %q is not a Kyverno policy", pol.GVR)
	}
	ns := res.Namespace
	meta := map[string]any{"name": exceptionName(pol.Name, res.Kind, res.Name)}
	if ns != "" {
		meta["namespace"] = ns
	}
	var doc map[string]any
	apiVersion := GroupCEL + "/v1"
	if k.Legacy {
		apiVersion = GroupLegacy + "/v2"
		ex := map[string]any{"policyName": pol.Name, "ruleNames": []any{"*"}}
		if p.Rule != "" {
			ex["ruleNames"] = []any{p.Rule}
		}
		resMatch := map[string]any{"kinds": []any{res.Kind}, "names": []any{res.Name}}
		if ns != "" {
			resMatch["namespaces"] = []any{ns}
		}
		doc = map[string]any{"apiVersion": apiVersion, "kind": "PolicyException", "metadata": meta,
			"spec": map[string]any{"exceptions": []any{ex}, "match": map[string]any{"any": []any{map[string]any{"resources": resMatch}}}}}
	} else {
		expr := fmt.Sprintf("object.kind == %q && object.metadata.name == %q", res.Kind, res.Name)
		if ns != "" {
			expr += fmt.Sprintf(" && object.metadata.namespace == %q", ns)
		}
		doc = map[string]any{"apiVersion": apiVersion, "kind": "PolicyException", "metadata": meta,
			"spec": map[string]any{
				"policyRefs":      []any{map[string]any{"name": pol.Name, "kind": k.Kind}},
				"matchConditions": []any{map[string]any{"name": exceptionName("exclude", res.Name), "expression": expr}},
			}}
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return nil, ops.NewError("internal", "%s", err.Error())
	}
	return &protocol.KyvernoExceptionTemplateResult{YAML: string(b), APIVersion: apiVersion, Namespace: ns}, nil
}
