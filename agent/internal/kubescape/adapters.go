package kubescape

import (
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/security"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func workload(m *ingest.Module, r *ingest.Row) security.WorkloadRef {
	return security.NormalizeWorkload(m.StrOf(r, ColKind), r.GetNamespace(), m.StrOf(r, ColWorkload))
}

// imageOf is the image of a summary row; summaries that do not carry one are keyed by their workload.
func imageOf(m *ingest.Module, r *ingest.Row) (repo, tag string) {
	repo, tag = m.StrOf(r, ColRepository), m.StrOf(r, ColTag)
	if repo == "" {
		w := workload(m, r)
		repo = w.Kind + "/" + w.Name
	}
	return
}

func registerAdapters() {
	grVuln, grCfg := Group+"/"+ResVulnSummary, Group+"/"+ResConfigSummary
	security.RegisterVuln(grVuln, func(c *problems.Ctx, k problems.Key, r *ingest.Row) *security.VulnInfo {
		m := c.Mod(k.GR)
		if m == nil {
			return nil
		}
		repo, tag := imageOf(m, r)
		return &security.VulnInfo{Source: protocol.SourceKubescape, Repository: repo, Tag: tag, Critical: int(m.IntOf(r, ColCritical)), High: int(m.IntOf(r, ColHigh)),
			Medium: int(m.IntOf(r, ColMedium)), Low: int(m.IntOf(r, ColLow)), Unknown: int(m.IntOf(r, ColUnknown)), Workload: workload(m, r), Since: r.CreatedMs()}
	})
	security.RegisterConfig(grCfg, func(c *problems.Ctx, k problems.Key, r *ingest.Row) *security.ConfigInfo {
		m := c.Mod(k.GR)
		if m == nil {
			return nil
		}
		return &security.ConfigInfo{Source: protocol.SourceKubescape, Subject: workload(m, r), Critical: int(m.IntOf(r, ColCritical)), High: int(m.IntOf(r, ColHigh)),
			Medium: int(m.IntOf(r, ColMedium)), Low: int(m.IntOf(r, ColLow)), Since: r.CreatedMs()}
	})
	security.RegisterImageSource(func(cfg *security.Config, cluster string, emit func(security.ImageVuln)) {
		if cfg.Scan == nil {
			return
		}
		for _, m := range ingest.CuratedModules() {
			if m.Group != Group || m.Resource != ResVulnSummary {
				continue
			}
			m := m
			cfg.Scan(cluster, m.GVR, func(r *ingest.Row) bool {
				repo, tag := imageOf(m, r)
				emit(security.ImageVuln{Source: protocol.SourceKubescape, Cluster: cluster, Repository: repo, Tag: tag, Critical: int(m.IntOf(r, ColCritical)),
					High: int(m.IntOf(r, ColHigh)), Medium: int(m.IntOf(r, ColMedium)), Low: int(m.IntOf(r, ColLow)), Unknown: int(m.IntOf(r, ColUnknown)), Workload: workload(m, r)})
				return true
			})
		}
	})
}
