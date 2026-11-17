// Package trivy is the Trivy Operator module: curated server-side Table watches of its reports (docs/integrations.md:
// "The size problem"), so the always-on cost is metadata plus a few integers per report and no CVE array is ever
// transferred to know the counts; image aggregation; the full-report detail; rescan; and the problem adapters.
package trivy

import (
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/security"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// Group is the API group of every Trivy Operator kind.
const Group = "aquasecurity.github.io"

// Version is the version the module is registered at (others are resolved through discovery).
const Version = "v1alpha1"

// Label keys the operator puts on every report.
const (
	LabelKind      = "trivy-operator.resource.kind"
	LabelName      = "trivy-operator.resource.name"
	LabelNamespace = "trivy-operator.resource.namespace"
	LabelContainer = "trivy-operator.container.name"
)

type family uint8

const (
	famVuln family = iota
	famSecret
	famConfig
	famCompliance
)

// kindInfo describes one Trivy report kind.
type kindInfo struct {
	Kind, Resource string
	Short          []string
	Namespaced     bool
	Fam            family
	Tier0          bool   // always watched where discovery lists it
	Gate           string // else watched while this problem class is enabled
}

var kinds = []kindInfo{
	{"VulnerabilityReport", "vulnerabilityreports", []string{"vuln", "vulns"}, true, famVuln, true, ""},
	{"ConfigAuditReport", "configauditreports", []string{"configaudit", "configaudits"}, true, famConfig, true, ""},
	{"ExposedSecretReport", "exposedsecretreports", []string{"exposedsecret", "exposedsecrets"}, true, famSecret, true, ""},
	{"RbacAssessmentReport", "rbacassessmentreports", []string{"rbacassessment", "rbacassessments"}, true, famConfig, false, "rbac.high"},
	{"InfraAssessmentReport", "infraassessmentreports", []string{"infraassessment", "infraassessments"}, true, famConfig, false, "misconfig.high"},
	{"ClusterVulnerabilityReport", "clustervulnerabilityreports", []string{"clustervuln"}, false, famVuln, false, "vuln.high"},
	{"ClusterConfigAuditReport", "clusterconfigauditreports", []string{"clusterconfigaudit"}, false, famConfig, false, "misconfig.high"},
	{"ClusterRbacAssessmentReport", "clusterrbacassessmentreports", []string{"clusterrbacassessmentreport"}, false, famConfig, false, "rbac.high"},
	{"ClusterInfraAssessmentReport", "clusterinfraassessmentreports", []string{"clusterinfraassessment"}, false, famConfig, false, "misconfig.high"},
	{"ClusterComplianceReport", "clustercompliancereports", []string{"compliance"}, false, famCompliance, false, "misconfig.high"},
}

func info(group, resource string) *kindInfo {
	if group != Group {
		return nil
	}
	for i := range kinds {
		if kinds[i].Resource == resource {
			return &kinds[i]
		}
	}
	return nil
}

// Column keys of the report modules.
const (
	ColKind       = "kind"
	ColWorkload   = "workload"
	ColContainer  = "container"
	ColRepository = "repository"
	ColTag        = "tag"
	ColScanner    = "scanner"
	ColCritical   = "critical"
	ColHigh       = "high"
	ColMedium     = "medium"
	ColLow        = "low"
	ColUnknown    = "unknown"
	ColFail       = "fail"
	ColPass       = "pass"
)

func cells(f family) []ingest.TableCellSpec {
	dict := func(key, label, header, lbl string, w int) ingest.TableCellSpec {
		return ingest.TableCellSpec{Key: key, Label: label, Type: protocol.ColDict, Width: w, Header: header, LabelKey: lbl}
	}
	str := func(key, label, header, lbl string, w int) ingest.TableCellSpec {
		return ingest.TableCellSpec{Key: key, Label: label, Type: protocol.ColStr, Width: w, Header: header, LabelKey: lbl}
	}
	num := func(key, label, header string) ingest.TableCellSpec {
		return ingest.TableCellSpec{Key: key, Label: label, Type: protocol.ColInt, Width: 76, Header: header}
	}
	res := []ingest.TableCellSpec{dict(ColKind, "Resource kind", "", LabelKind, 120), str(ColWorkload, "Resource", "", LabelName, 240)}
	switch f {
	case famVuln:
		return append(res, dict(ColContainer, "Container", "", LabelContainer, 120), str(ColRepository, "Repository", "Repository", "", 260), dict(ColTag, "Tag", "Tag", "", 140),
			dict(ColScanner, "Scanner", "Scanner", "", 80), num(ColCritical, "Critical", "Critical"), num(ColHigh, "High", "High"), num(ColMedium, "Medium", "Medium"),
			num(ColLow, "Low", "Low"), num(ColUnknown, "Unknown", "Unknown"))
	case famSecret:
		return append(res, dict(ColContainer, "Container", "", LabelContainer, 120), str(ColRepository, "Repository", "Repository", "", 260), dict(ColTag, "Tag", "Tag", "", 140),
			dict(ColScanner, "Scanner", "Scanner", "", 80), num(ColCritical, "Critical", "Critical"), num(ColHigh, "High", "High"), num(ColMedium, "Medium", "Medium"),
			num(ColLow, "Low", "Low"))
	case famConfig:
		return append(res, dict(ColScanner, "Scanner", "Scanner", "", 80), num(ColCritical, "Critical", "Critical"), num(ColHigh, "High", "High"),
			num(ColMedium, "Medium", "Medium"), num(ColLow, "Low", "Low"))
	}
	return []ingest.TableCellSpec{num(ColFail, "Fail", "Fail"), num(ColPass, "Pass", "Pass")}
}

func cellIdx(sp []ingest.TableCellSpec, key string) int {
	for i := range sp {
		if sp[i].Key == key {
			return i
		}
	}
	return -1
}

func newModule(k *kindInfo, version string) *ingest.Module {
	cs := cells(k.Fam)
	crit, high, fail := cellIdx(cs, ColCritical), cellIdx(cs, ColHigh), cellIdx(cs, ColFail)
	tone := func(r *ingest.Row) uint8 {
		switch {
		case fail >= 0:
			if r.Cell(fail).N > 0 {
				return ingest.ToneDanger
			}
			return ingest.ToneSuccess
		case r.Cell(crit).N > 0:
			return ingest.ToneDanger
		case r.Cell(high).N > 0:
			return ingest.ToneWarning
		}
		return ingest.ToneSuccess
	}
	return ingest.NewTableModule(ingest.TableModuleSpec{
		GVR: Group + "/" + version + "/" + k.Resource, Kind: k.Kind, Namespaced: k.Namespaced, Short: k.Short, Cells: cs, Tone: tone,
		Optional: true, Tier0: k.Tier0, GateClass: k.Gate,
	})
}

func init() {
	for i := range kinds {
		ingest.Register(newModule(&kinds[i], Version))
	}
	ingest.AddResolver(func(group, version, resource, kind string, namespaced bool) *ingest.Module {
		k := info(group, resource)
		if k == nil || k.Kind != kind {
			return nil
		}
		return newModule(k, version)
	})
	registerAdapters()
	registerHandlers()
}

// GVRs returns the registered GVRs (every served version) of the given report kind that are Table modules.
func moduleGVRs(kind string) []string {
	var out []string
	for _, m := range ingest.CuratedModules() {
		if m.Group == Group && m.Kind == kind {
			out = append(out, m.GVR)
		}
	}
	return out
}

var _ = security.ImageRef
