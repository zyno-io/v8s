package kyverno

import (
	"fmt"
	"sort"

	"github.com/zyno-io/v8s/agent/internal/security"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func init() {
	security.RegisterDetail(security.DetailProvider{
		Match: func(gvr string) bool { return reportKindOf(security.GroupOf(gvr), security.ResourceOf(gvr)) != nil },
		Build: BuildDetail,
	})
}

func s(m map[string]any, k string) string { v, _ := m[k].(string); return v }

func num(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	case int:
		return int64(x)
	}
	return 0
}

var resultOrder = map[string]int{"fail": 0, "error": 1, "warn": 2, "skip": 3, "pass": 4}

// BuildDetail normalizes one policy report (wgpolicyk8s.io PolicyReport / ClusterPolicyReport, openreports.io
// Report / ClusterReport) into PolicyResults, failures first. It is fetched lazily, one report at a time.
func BuildDetail(o *unstructured.Unstructured) (*protocol.ReportDetail, error) {
	d := &protocol.ReportDetail{Kind: "policyResults", Source: protocol.SourceKyverno, Title: o.GetName(), ResourceVersion: o.GetResourceVersion(), Summary: map[string]int{}}
	if sc, ok := o.Object["scope"].(map[string]any); ok {
		d.Title = fmt.Sprintf("%s %s", s(sc, "kind"), s(sc, "name"))
	}
	if sum, ok := o.Object["summary"].(map[string]any); ok {
		for _, k := range []string{"pass", "fail", "warn", "error", "skip"} {
			if _, ok := sum[k]; ok {
				d.Summary[k] = int(num(sum[k]))
			}
		}
	}
	rs, _ := o.Object["results"].([]any)
	for _, r := range rs {
		m, _ := r.(map[string]any)
		pr := protocol.PolicyResult{Policy: s(m, "policy"), Rule: s(m, "rule"), Result: s(m, "result"), Severity: s(m, "severity"), Message: s(m, "message"), Category: s(m, "category")}
		if ts, ok := m["timestamp"].(map[string]any); ok {
			pr.Timestamp = num(ts["seconds"]) * 1000
		}
		d.Results = append(d.Results, pr)
	}
	sort.SliceStable(d.Results, func(i, j int) bool {
		a, b := resultOrder[d.Results[i].Result], resultOrder[d.Results[j].Result]
		if a != b {
			return a < b
		}
		if d.Results[i].Policy != d.Results[j].Policy {
			return d.Results[i].Policy < d.Results[j].Policy
		}
		return d.Results[i].Rule < d.Results[j].Rule
	})
	d.Total = len(d.Results)
	return d, nil
}
