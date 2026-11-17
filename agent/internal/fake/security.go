package fake

// Fake security data (Trivy reports, Kyverno policies and policy reports, Kubescape summaries): enough variety
// that the Vulnerabilities and Policy views, the policy.* / vuln.* problem classes and security.reportDetail all
// have something to show across the fake clusters. Images are shared across workloads and clusters, so the same
// critical image appears in several clusters.

import (
	"fmt"
	"strconv"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	// the fake kinds below are served through these modules: without them registered the reflector has no list type
	_ "github.com/zyno-io/v8s/agent/internal/kubescape"
	_ "github.com/zyno-io/v8s/agent/internal/kyverno"
	_ "github.com/zyno-io/v8s/agent/internal/platform"
	_ "github.com/zyno-io/v8s/agent/internal/trivy"
)

func tcp(name, typ string, prio int32) ingest.TableCol {
	return ingest.TableCol{Name: name, Type: typ, Priority: prio}
}

func num(o map[string]any, path ...string) float64 {
	var cur any = o
	for _, p := range path {
		m, _ := cur.(map[string]any)
		cur = m[p]
	}
	switch x := cur.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		return x
	}
	return 0
}

func strAt(o map[string]any, path ...string) string {
	var cur any = o
	for _, p := range path {
		m, _ := cur.(map[string]any)
		cur = m[p]
	}
	s, _ := cur.(string)
	return s
}

const (
	trivyV = "aquasecurity.github.io/v1alpha1"
	wgV    = "wgpolicyk8s.io/v1alpha2"
	polV   = "policies.kyverno.io/v1"
	kscV   = "spdx.softwarecomposition.kubescape.io/v1beta1"
)

func init() {
	registerKinds(
		&kindDef{gvr: trivyV + "/vulnerabilityreports", apiVersion: trivyV, kind: "VulnerabilityReport", resource: "vulnerabilityreports", namespaced: true, short: []string{"vuln", "vulns"},
			cols: []ingest.TableCol{tc("Name", "string"), tc("Repository", "string"), tc("Tag", "string"), tc("Scanner", "string"), tc("Age", "date"),
				tcp("Critical", "integer", 1), tcp("High", "integer", 1), tcp("Medium", "integer", 1), tcp("Low", "integer", 1), tcp("Unknown", "integer", 1)},
			cells: func(o map[string]any) []any {
				return []any{nameOf(o), strAt(o, "report", "artifact", "repository"), strAt(o, "report", "artifact", "tag"), "Trivy", "1d",
					num(o, "report", "summary", "criticalCount"), num(o, "report", "summary", "highCount"), num(o, "report", "summary", "mediumCount"),
					num(o, "report", "summary", "lowCount"), num(o, "report", "summary", "unknownCount")}
			}},
		&kindDef{gvr: trivyV + "/configauditreports", apiVersion: trivyV, kind: "ConfigAuditReport", resource: "configauditreports", namespaced: true, short: []string{"configaudit"},
			cols: []ingest.TableCol{tc("Name", "string"), tc("Scanner", "string"), tc("Age", "date"), tcp("Critical", "integer", 1), tcp("High", "integer", 1), tcp("Medium", "integer", 1), tcp("Low", "integer", 1)},
			cells: func(o map[string]any) []any {
				return []any{nameOf(o), "Trivy", "1d", num(o, "report", "summary", "criticalCount"), num(o, "report", "summary", "highCount"),
					num(o, "report", "summary", "mediumCount"), num(o, "report", "summary", "lowCount")}
			}},
		&kindDef{gvr: trivyV + "/exposedsecretreports", apiVersion: trivyV, kind: "ExposedSecretReport", resource: "exposedsecretreports", namespaced: true, short: []string{"exposedsecret"},
			cols: []ingest.TableCol{tc("Name", "string"), tc("Repository", "string"), tc("Tag", "string"), tc("Scanner", "string"), tc("Age", "date"),
				tcp("Critical", "integer", 1), tcp("High", "integer", 1), tcp("Medium", "integer", 1), tcp("Low", "integer", 1)},
			cells: func(o map[string]any) []any {
				return []any{nameOf(o), strAt(o, "report", "artifact", "repository"), strAt(o, "report", "artifact", "tag"), "Trivy", "1d",
					num(o, "report", "summary", "criticalCount"), num(o, "report", "summary", "highCount"), num(o, "report", "summary", "mediumCount"), num(o, "report", "summary", "lowCount")}
			}},
		&kindDef{gvr: wgV + "/policyreports", apiVersion: wgV, kind: "PolicyReport", resource: "policyreports", namespaced: true, short: []string{"polr"},
			cols: []ingest.TableCol{tc("Name", "string"), tc("Kind", "string"), tc("Name", "string"), tc("Pass", "integer"), tc("Fail", "integer"), tc("Warn", "integer"),
				tc("Error", "integer"), tc("Skip", "integer"), tc("Age", "date")},
			cells: func(o map[string]any) []any {
				return []any{nameOf(o), strAt(o, "scope", "kind"), strAt(o, "scope", "name"), num(o, "summary", "pass"), num(o, "summary", "fail"), num(o, "summary", "warn"),
					num(o, "summary", "error"), num(o, "summary", "skip"), "1d"}
			}},
		&kindDef{gvr: polV + "/validatingpolicies", apiVersion: polV, kind: "ValidatingPolicy", resource: "validatingpolicies", short: []string{"vpol"}},
		&kindDef{gvr: "kyverno.io/v1/clusterpolicies", apiVersion: "kyverno.io/v1", kind: "ClusterPolicy", resource: "clusterpolicies", short: []string{"cpol"}},
		&kindDef{gvr: kscV + "/vulnerabilitymanifestsummaries", apiVersion: kscV, kind: "VulnerabilityManifestSummary", resource: "vulnerabilitymanifestsummaries", namespaced: true},
		&kindDef{gvr: kscV + "/workloadconfigurationscansummaries", apiVersion: kscV, kind: "WorkloadConfigurationScanSummary", resource: "workloadconfigurationscansummaries", namespaced: true},
		&kindDef{gvr: kscV + "/vulnerabilitymanifests", apiVersion: kscV, kind: "VulnerabilityManifest", resource: "vulnerabilitymanifests", namespaced: true,
			cols: []ingest.TableCol{tc("Name", "string"), tc("Age", "date")}, cells: func(o map[string]any) []any { return []any{nameOf(o), "1d"} }},
	)
	registerGen(genSecurity)
}

type fakeImage struct {
	repo, tag                 string
	crit, high, med, low, unk int
}

// images shared across workloads and clusters; the first ones carry criticals.
var fakeImages = []fakeImage{
	{"library/nginx", "1.25.3", 4, 31, 16, 4, 164},
	{"library/redis", "7.0.11", 2, 12, 9, 3, 40},
	{"library/postgres", "15.4", 0, 3, 11, 20, 8},
	{"bitnami/kafka", "3.5.1", 1, 9, 22, 6, 12},
	{"library/busybox", "1.36", 0, 0, 2, 1, 0},
	{"library/alpine", "3.19", 0, 0, 0, 0, 0},
}

func sevLabel(i int) string { return []string{"CRITICAL", "HIGH", "MEDIUM", "LOW"}[i%4] }

// jsonInts converts Go ints to int64 (unstructured objects only deep-copy JSON number types).
func jsonInts(v any) any {
	switch x := v.(type) {
	case int:
		return int64(x)
	case map[string]any:
		for k, e := range x {
			x[k] = jsonInts(e)
		}
	case []any:
		for i, e := range x {
			x[i] = jsonInts(e)
		}
	}
	return v
}

func genSecurity(w *World, c *Cluster) {
	put := func(gvr string, o map[string]any) { w.putLocked(gvr, jsonInts(o).(map[string]any)) }
	ci := clusterIndex(c.name)
	now := time.Now()
	created := ts(now.Add(-6 * time.Hour))
	meta := func(ns, name string, labels map[string]string) map[string]any {
		m := objMeta(ns, name, labels)
		m["creationTimestamp"] = created
		return m
	}

	// ---- Trivy: one VulnerabilityReport per workload container (apps 0..39), ConfigAudit for some, an exposed secret ----
	for a := 0; a < 40; a++ {
		img := fakeImages[(a+ci)%len(fakeImages)]
		app := "svc-" + strconv.Itoa(a)
		ns := teamNS(a)
		rs := fmt.Sprintf("%s-%010x", app, rsHash(a))
		labels := map[string]string{"trivy-operator.resource.kind": "ReplicaSet", "trivy-operator.resource.name": rs,
			"trivy-operator.resource.namespace": ns, "trivy-operator.container.name": app, "app.kubernetes.io/managed-by": "trivy-operator"}
		var vulns []any
		for i := 0; i < min(img.crit+img.high+img.med, 12); i++ {
			vulns = append(vulns, map[string]any{"vulnerabilityID": fmt.Sprintf("CVE-2026-%04d", 1000+i*7+a%5), "severity": sevLabel(i), "resource": "libssl3",
				"installedVersion": "3.1." + strconv.Itoa(i), "fixedVersion": "3.1." + strconv.Itoa(i+1), "title": "OpenSSL: example vulnerability " + strconv.Itoa(i),
				"primaryLink": "https://avd.aquasec.com/nvd/cve-2026-" + strconv.Itoa(1000+i)})
		}
		put(trivyV+"/vulnerabilityreports", map[string]any{"metadata": meta(ns, "replicaset-"+rs+"-"+app, labels),
			"report": map[string]any{"artifact": map[string]any{"repository": img.repo, "tag": img.tag},
				"summary":         map[string]any{"criticalCount": img.crit, "highCount": img.high, "mediumCount": img.med, "lowCount": img.low, "unknownCount": img.unk, "noneCount": 0},
				"vulnerabilities": vulns}})
		if a%4 == 0 {
			hi := a % 3
			put(trivyV+"/configauditreports", map[string]any{"metadata": meta(ns, "replicaset-"+rs, map[string]string{"trivy-operator.resource.kind": "ReplicaSet",
				"trivy-operator.resource.name": rs, "trivy-operator.resource.namespace": ns}),
				"report": map[string]any{"summary": map[string]any{"criticalCount": a % 2, "highCount": hi, "mediumCount": 2, "lowCount": 1},
					"checks": []any{
						map[string]any{"checkID": "KSV017", "title": "Privileged container", "severity": "HIGH", "category": "Kubernetes Security Check", "success": false,
							"messages": []any{"Container '" + app + "' of ReplicaSet '" + rs + "' should set 'securityContext.privileged' to false"}},
						map[string]any{"checkID": "KSV001", "title": "Process can elevate its own privileges", "severity": "MEDIUM", "success": false},
						map[string]any{"checkID": "KSV003", "title": "Default capabilities not dropped", "severity": "LOW", "success": true},
					}}})
		}
	}
	if ci == 0 {
		app, ns := "svc-3", teamNS(3)
		rs := fmt.Sprintf("%s-%010x", app, rsHash(3))
		put(trivyV+"/exposedsecretreports", map[string]any{"metadata": meta(ns, "replicaset-"+rs+"-"+app, map[string]string{"trivy-operator.resource.kind": "ReplicaSet",
			"trivy-operator.resource.name": rs, "trivy-operator.resource.namespace": ns, "trivy-operator.container.name": app}),
			"report": map[string]any{"artifact": map[string]any{"repository": "library/busybox", "tag": "1.36"},
				"summary": map[string]any{"criticalCount": 1, "highCount": 0, "mediumCount": 0, "lowCount": 0},
				"secrets": []any{map[string]any{"ruleID": "aws-access-key-id", "category": "AWS", "severity": "CRITICAL", "title": "AWS Access Key ID", "target": "/app/.env", "match": "AWS_ACCESS_KEY_ID=****"}}}})
	}

	// ---- Kyverno: policies and one report per Deployment (the fake Deployments carry no team label) ----
	cond := func(status, reason, msg string, ago time.Duration) map[string]any {
		return map[string]any{"type": "Ready", "status": status, "reason": reason, "message": msg, "lastTransitionTime": ts(now.Add(-ago))}
	}
	put(polV+"/validatingpolicies", map[string]any{"metadata": func() map[string]any {
		m := meta("", "require-team-label", nil)
		m["annotations"] = map[string]any{"policies.kyverno.io/severity": "medium"}
		return m
	}(), "spec": map[string]any{"validationActions": []any{"Audit"}, "validations": []any{map[string]any{"expression": "has(object.metadata.labels) && 'team' in object.metadata.labels", "message": "Deployments must carry a team label."}}},
		"status": map[string]any{"conditionStatus": map[string]any{"ready": true, "message": "Policy is ready for reporting.", "conditions": []any{cond("True", "Succeeded", "Webhook configured.", 5*time.Hour)}}}})
	put(polV+"/validatingpolicies", map[string]any{"metadata": meta("", "disallow-latest-tag", nil),
		"spec":   map[string]any{"validationActions": []any{"Deny"}, "validations": []any{map[string]any{"expression": "!object.spec.template.spec.containers.exists(c, c.image.endsWith(':latest'))"}}},
		"status": map[string]any{"conditionStatus": map[string]any{"ready": true, "conditions": []any{cond("True", "Succeeded", "ok", 5*time.Hour)}}}})
	put("kyverno.io/v1/clusterpolicies", map[string]any{"metadata": meta("", "restrict-image-registries", nil),
		"spec": map[string]any{"validationFailureAction": "Enforce", "background": true, "rules": []any{
			map[string]any{"name": "validate-registries", "validate": map[string]any{"failureAction": "Enforce"}},
			map[string]any{"name": "audit-tags", "validate": map[string]any{"failureAction": "Audit"}}}},
		"status": map[string]any{"ready": true}})
	if ci == 0 {
		put("kyverno.io/v1/clusterpolicies", map[string]any{"metadata": meta("", "broken-policy", nil),
			"spec":   map[string]any{"rules": []any{map[string]any{"name": "r", "validate": map[string]any{"failureAction": "Audit"}}}},
			"status": map[string]any{"ready": false, "conditions": []any{cond("False", "Failed", "variable references an unknown context entry", 30*time.Minute)}}})
	}
	for a := 0; a < 60; a++ {
		app, ns := "svc-"+strconv.Itoa(a), teamNS(a)
		fail, pass := 1, 0
		if a%7 == 0 {
			fail, pass = 0, 1
		}
		var results []any
		if fail > 0 {
			results = append(results, map[string]any{"policy": "require-team-label", "rule": "validation", "result": "fail", "severity": "medium",
				"message": "Deployments must carry a team label.", "source": "KyvernoValidatingPolicy", "timestamp": map[string]any{"seconds": now.Unix()}})
		} else {
			results = append(results, map[string]any{"policy": "require-team-label", "rule": "validation", "result": "pass", "severity": "medium", "timestamp": map[string]any{"seconds": now.Unix()}})
		}
		put(wgV+"/policyreports", map[string]any{"metadata": meta(ns, "polr-"+app, map[string]string{"app.kubernetes.io/managed-by": "kyverno"}),
			"scope":   map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": app, "namespace": ns},
			"summary": map[string]any{"pass": pass, "fail": fail, "warn": 0, "error": 0, "skip": 0}, "results": results})
	}

	// ---- Kubescape summaries (and the manifests they point to) ----
	for a := 40; a < 50; a++ {
		img := fakeImages[(a+ci)%len(fakeImages)]
		app, ns := "svc-"+strconv.Itoa(a), teamNS(a)
		lbl := map[string]string{"kubescape.io/workload-kind": "Deployment", "kubescape.io/workload-name": app, "kubescape.io/workload-namespace": ns,
			"kubescape.io/workload-container-name": app, "kubescape.io/image-tag": img.repo + ":" + img.tag}
		sev := func(n int) map[string]any { return map[string]any{"all": n, "fixable": n / 2} }
		vm := meta(ns, "deployment-"+app+"-"+app, lbl)
		put(kscV+"/vulnerabilitymanifestsummaries", map[string]any{"metadata": vm,
			"spec": map[string]any{"severities": map[string]any{"critical": sev(img.crit), "high": sev(img.high), "medium": sev(img.med), "low": sev(img.low), "negligible": sev(0), "unknown": sev(img.unk)},
				"vulnerabilitiesRef": map[string]any{"all": map[string]any{"name": "manifest-" + app, "namespace": ns}}}})
		var matches []any
		for i := 0; i < 6; i++ {
			matches = append(matches, map[string]any{"vulnerability": map[string]any{"id": fmt.Sprintf("CVE-2026-%04d", 2000+i), "severity": []string{"Critical", "High", "Medium"}[i%3],
				"description": "Example vulnerability in a library", "dataSource": "https://nvd.nist.gov/vuln/detail/CVE-2026-" + strconv.Itoa(2000+i),
				"fix": map[string]any{"versions": []any{"2.0." + strconv.Itoa(i)}, "state": "fixed"}}, "artifact": map[string]any{"name": "libexample", "version": "1.9." + strconv.Itoa(i)}})
		}
		put(kscV+"/vulnerabilitymanifests", map[string]any{"metadata": meta(ns, "manifest-"+app, lbl), "spec": map[string]any{"payload": map[string]any{"matches": matches}}})
		put(kscV+"/workloadconfigurationscansummaries", map[string]any{"metadata": meta(ns, "deployment-"+app, lbl),
			"spec": map[string]any{"severities": map[string]any{"critical": a % 2, "high": a % 4, "medium": 3, "low": 1}}})
	}
}
