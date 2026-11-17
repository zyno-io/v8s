package ops

import (
	"context"
	"time"

	"github.com/zyno-io/v8s/agent/internal/flux"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Source wait tuning for flux.reconcileWithSource (variables so tests can shorten them).
var (
	fluxPollInterval = 500 * time.Millisecond
	fluxPollTimeout  = 45 * time.Second // below mutationTimeout, so the poll can report its own timeout
)

// fluxAction runs the flux.* object actions (docs/integrations.md).
func (s *Service) fluxAction(ctx context.Context, p protocol.ObjectActionParams, gvr schema.GroupVersionResource) (protocol.MutationResult, error) {
	var res protocol.MutationResult
	if !flux.IsFluxGroup(gvr.Group) {
		return res, newErr("bad_request", "action %q is not supported for %s", p.Action, gvrString(gvr))
	}
	kind, ok := flux.KindOf(gvr.Group, gvr.Resource)
	if !ok {
		return res, newErr("bad_request", "action %q is not supported for %s", p.Action, gvrString(gvr))
	}
	ref := p.Ref

	// reject actions the kind cannot take before reading anything
	if _, perr := flux.PlanAction(p.Action, kind, s.cfg.Now(), true); perr != nil {
		return res, newErr("bad_request", "%s", perr.Error())
	}

	var srcGVR schema.GroupVersionResource
	var srcNS, srcName string
	hasSource := false
	if p.Action == "flux.reconcileWithSource" {
		rc, err := s.conn(ref.Cluster)
		if err != nil {
			return res, err
		}
		obj, gerr := resource(rc.Dynamic(), gvr, ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if gerr != nil {
			return res, mapErr(gerr, false)
		}
		if apiVersion, skind, ns, name, found := flux.SourceOf(obj); found {
			hasSource = true
			if s.cfg.Resolve == nil {
				return res, newErr("internal", "no resolver")
			}
			g, _, rerr := s.cfg.Resolve(ref.Cluster, apiVersion, skind)
			if rerr != nil {
				return res, newErr("bad_request", "cannot resolve source %s %s: %s", apiVersion, skind, rerr.Error())
			}
			if srcGVR, err = parseGVR(g); err != nil {
				return res, err
			}
			srcNS, srcName = ns, name
		}
	}

	plan, perr := flux.PlanAction(p.Action, kind, s.cfg.Now(), hasSource)
	if perr != nil {
		return res, newErr("bad_request", "%s", perr.Error())
	}

	if plan.SourceFirst {
		if err := s.reconcileSource(ctx, ref.Cluster, srcGVR, srcNS, srcName); err != nil {
			return res, err
		}
	}

	cid, c, viaWrite, err := s.route(ctx, ref.Cluster, "patch", gvr, ref.Namespace, ref.Name)
	if err != nil {
		return res, err
	}
	out, patchErr := resource(c.Dynamic(), gvr, ref.Namespace).Patch(ctx, ref.Name, types.MergePatchType, plan.Patch, metav1.PatchOptions{FieldManager: fieldManager})
	if patchErr != nil {
		return res, s.mapW(ctx, patchErr, viaWrite, wsite{cid: cid, verb: "patch", gvr: gvr, ns: ref.Namespace, name: ref.Name})
	}
	res.UsedContext = cid
	res.ResourceVersion = out.GetResourceVersion()
	res.Token = plan.Token
	return res, nil
}

// reconcileSource requests a reconcile of the source and waits until its lastHandledReconcileAt equals the token.
func (s *Service) reconcileSource(ctx context.Context, cluster string, gvr schema.GroupVersionResource, ns, name string) error {
	token := flux.Token(s.cfg.Now())
	cid, c, viaWrite, err := s.route(ctx, cluster, "patch", gvr, ns, name)
	if err != nil {
		return err
	}
	site := wsite{cid: cid, verb: "patch", gvr: gvr, ns: ns, name: name}
	api := resource(c.Dynamic(), gvr, ns)
	if _, perr := api.Patch(ctx, name, types.MergePatchType, flux.ReconcilePatch(token), metav1.PatchOptions{FieldManager: fieldManager}); perr != nil {
		return s.mapW(ctx, perr, viaWrite, site)
	}
	wctx, cancel := context.WithTimeout(ctx, fluxPollTimeout)
	defer cancel()
	tick := time.NewTicker(fluxPollInterval)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return newErr("internal", "request canceled while waiting for source %s/%s: %s", ns, name, ctx.Err().Error())
		}
		o, gerr := api.Get(wctx, name, metav1.GetOptions{})
		if gerr == nil {
			if h, _, _ := unstructured.NestedString(o.Object, "status", "lastHandledReconcileAt"); h == token {
				return nil
			}
		} else if wctx.Err() == nil {
			return s.mapW(wctx, gerr, viaWrite, wsite{cid: cid, verb: "get", gvr: gvr, ns: ns, name: name})
		}
		select {
		case <-wctx.Done():
			if ctx.Err() != nil {
				return newErr("internal", "request canceled while waiting for source %s/%s: %s", ns, name, ctx.Err().Error())
			}
			return newErr("unavailable", "source %s/%s did not finish reconciling within %s", ns, name, fluxPollTimeout)
		case <-tick.C:
		}
	}
}
