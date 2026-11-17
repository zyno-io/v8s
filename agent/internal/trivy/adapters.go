package trivy

import (
	"context"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/rpc"
	"github.com/zyno-io/v8s/agent/internal/security"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// subjectOf reads the workload a report is about: the operator's resource labels, falling back to the report's
// owner reference (RBAC reports label only a hash of the name).
func subjectOf(mod *ingest.Module, r *ingest.Row) security.WorkloadRef {
	kind, name := mod.StrOf(r, ColKind), mod.StrOf(r, ColWorkload)
	if name == "" {
		name = r.OwnerName().Value()
	}
	if kind == "" {
		kind = r.OwnerKind().Value()
	}
	return security.NormalizeWorkload(kind, r.GetNamespace(), name)
}

func registerAdapters() {
	for _, k := range kinds {
		k := k
		gr := Group + "/" + k.Resource
		switch k.Fam {
		case famVuln:
			security.RegisterVuln(gr, func(c *problems.Ctx, key problems.Key, r *ingest.Row) *security.VulnInfo {
				m := c.Mod(key.GR)
				if m == nil {
					return nil
				}
				return &security.VulnInfo{Source: protocol.SourceTrivy, Repository: m.StrOf(r, ColRepository), Tag: m.StrOf(r, ColTag),
					Critical: int(m.IntOf(r, ColCritical)), High: int(m.IntOf(r, ColHigh)), Medium: int(m.IntOf(r, ColMedium)), Low: int(m.IntOf(r, ColLow)),
					Unknown: int(m.IntOf(r, ColUnknown)), Workload: subjectOf(m, r), Since: r.CreatedMs()}
			})
		case famSecret:
			security.RegisterSecret(gr, func(c *problems.Ctx, key problems.Key, r *ingest.Row) *security.SecretInfo {
				m := c.Mod(key.GR)
				if m == nil {
					return nil
				}
				n := int(m.IntOf(r, ColCritical) + m.IntOf(r, ColHigh) + m.IntOf(r, ColMedium) + m.IntOf(r, ColLow))
				return &security.SecretInfo{Source: protocol.SourceTrivy, Subject: subjectOf(m, r), Container: m.StrOf(r, ColContainer), Count: n, Since: r.CreatedMs()}
			})
		case famConfig:
			a := func(c *problems.Ctx, key problems.Key, r *ingest.Row) *security.ConfigInfo {
				m := c.Mod(key.GR)
				if m == nil {
					return nil
				}
				return &security.ConfigInfo{Source: protocol.SourceTrivy, Subject: subjectOf(m, r), Critical: int(m.IntOf(r, ColCritical)), High: int(m.IntOf(r, ColHigh)),
					Medium: int(m.IntOf(r, ColMedium)), Low: int(m.IntOf(r, ColLow)), Since: r.CreatedMs()}
			}
			switch k.Kind {
			case "RbacAssessmentReport", "ClusterRbacAssessmentReport":
				security.RegisterRBAC(gr, a)
			default:
				security.RegisterConfig(gr, a)
			}
		}
	}
	security.RegisterImageSource(func(cfg *security.Config, cluster string, emit func(security.ImageVuln)) {
		if cfg.Scan == nil {
			return
		}
		for _, kind := range []string{"VulnerabilityReport", "ClusterVulnerabilityReport"} {
			for _, gvr := range moduleGVRs(kind) {
				mod := ingest.Lookup(gvr)
				cfg.Scan(cluster, gvr, func(r *ingest.Row) bool {
					emit(security.ImageVuln{Source: protocol.SourceTrivy, Cluster: cluster, Repository: mod.StrOf(r, ColRepository), Tag: mod.StrOf(r, ColTag),
						Critical: int(mod.IntOf(r, ColCritical)), High: int(mod.IntOf(r, ColHigh)), Medium: int(mod.IntOf(r, ColMedium)),
						Low: int(mod.IntOf(r, ColLow)), Unknown: int(mod.IntOf(r, ColUnknown)), Workload: subjectOf(mod, r)})
					return true
				})
			}
		}
	})
}

func registerHandlers() {
	security.RegisterHandler("trivy.rescan", func(cfg *security.Config) rpc.Handler {
		return func(ctx context.Context, decode func(any) error) (any, error) {
			var p protocol.TrivyRescanParams
			if err := decode(&p); err != nil {
				return nil, ops.NewError("bad_request", "params: %s", err.Error())
			}
			return Rescan(ctx, cfg, p)
		}
	})
}

// Rescan deletes a report so the operator recreates it on its next reconcile ("Delete report to trigger a rescan").
func Rescan(ctx context.Context, cfg *security.Config, p protocol.TrivyRescanParams) (*protocol.SecurityActionResult, error) {
	ref := p.Ref
	res := security.ResourceOf(ref.GVR)
	if security.GroupOf(ref.GVR) != Group || len(res) < 7 || res[len(res)-7:] != "reports" || ref.Name == "" || ref.Cluster == "" {
		return nil, ops.NewError("bad_request", "ref must be a Trivy Operator report (%s)", Group)
	}
	ctx = ops.WithUseWrite(ctx, p.UseWriteContext)
	routed, err := cfg.Route(ctx, ref.Cluster, "delete", ref.GVR, "", ref.Namespace, ref.Name)
	if err != nil {
		return nil, err
	}
	g, err := security.ParseGVR(ref.GVR)
	if err != nil {
		return nil, err
	}
	ri := routed.Conn.Dynamic().Resource(g)
	opts := metav1.DeleteOptions{}
	if ref.UID != "" {
		uid := types.UID(ref.UID)
		opts.Preconditions = &metav1.Preconditions{UID: &uid}
	}
	if ref.Namespace != "" {
		err = ri.Namespace(ref.Namespace).Delete(ctx, ref.Name, opts)
	} else {
		err = ri.Delete(ctx, ref.Name, opts)
	}
	if err != nil {
		return nil, ops.MapError(err, routed.ViaWrite)
	}
	return &protocol.SecurityActionResult{UsedContext: routed.ContextID}, nil
}
