package kyverno

import (
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// Column keys of the policy report modules.
const (
	ColScopeKind = "scopeKind"
	ColScopeName = "scopeName"
	ColPass      = "pass"
	ColFail      = "fail"
	ColWarn      = "warn"
	ColError     = "error"
	ColSkip      = "skip"
)

type reportKind struct {
	Group, Version, Kind, Resource string
	Short                          []string
	Namespaced                     bool
}

var reportKinds = []reportKind{
	{GroupWG, "v1alpha2", "PolicyReport", "policyreports", []string{"polr"}, true},
	{GroupWG, "v1alpha2", "ClusterPolicyReport", "clusterpolicyreports", []string{"cpolr"}, false},
	{GroupOR, "v1alpha1", "Report", "reports", []string{"orep"}, true},
	{GroupOR, "v1alpha1", "ClusterReport", "clusterreports", []string{"corep"}, false},
}

func reportKindOf(group, resource string) *reportKind {
	for i := range reportKinds {
		if reportKinds[i].Group == group && reportKinds[i].Resource == resource {
			return &reportKinds[i]
		}
	}
	return nil
}

// newReportModule watches a report kind in server-side Table mode (mode C): the printer columns carry
// Kind, Name (of the scanned resource) and the Pass/Fail/Warn/Error/Skip counts, so no result array is transferred
// to know which resources fail. The report's own name is the resource uid and is not used.
func newReportModule(k *reportKind, version string) *ingest.Module {
	num := func(key, label, header string) ingest.TableCellSpec {
		return ingest.TableCellSpec{Key: key, Label: label, Type: protocol.ColInt, Width: 70, Header: header}
	}
	cells := []ingest.TableCellSpec{
		{Key: ColScopeKind, Label: "Kind", Type: protocol.ColDict, Width: 120, Header: "Kind"},
		{Key: ColScopeName, Label: "Resource", Type: protocol.ColStr, Width: 260, Header: "Name#2"},
		num(ColPass, "Pass", "Pass"), num(ColFail, "Fail", "Fail"), num(ColWarn, "Warn", "Warn"), num(ColError, "Error", "Error"), num(ColSkip, "Skip", "Skip"),
	}
	const cFail, cWarn, cError = 3, 4, 5 // cell indexes above
	tone := func(r *ingest.Row) uint8 {
		switch {
		case r.Cell(cFail).N > 0 || r.Cell(cError).N > 0:
			return ingest.ToneDanger
		case r.Cell(cWarn).N > 0:
			return ingest.ToneWarning
		}
		return ingest.ToneSuccess
	}
	return ingest.NewTableModule(ingest.TableModuleSpec{GVR: k.Group + "/" + version + "/" + k.Resource, Kind: k.Kind, Namespaced: k.Namespaced, Short: k.Short,
		Cells: cells, Tone: tone, Optional: true, Tier0: true})
}

func init() {
	for i := range policyKinds {
		k := &policyKinds[i]
		ingest.Register(newPolicyModule(k, k.Version))
	}
	for i := range reportKinds {
		k := &reportKinds[i]
		ingest.Register(newReportModule(k, k.Version))
	}
	ingest.AddResolver(func(group, version, resource, kind string, namespaced bool) *ingest.Module {
		if k := policyKindOf(group, resource); k != nil && k.Kind == kind {
			return newPolicyModule(k, version)
		}
		if k := reportKindOf(group, resource); k != nil && k.Kind == kind {
			return newReportModule(k, version)
		}
		return nil
	})
}
