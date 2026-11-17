package flux

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	apiSource    = GroupSource + "/v1"
	apiKustomize = GroupKustomize + "/v1"
	apiHelm      = GroupHelm + "/v2"
)

func sha(seed int) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	x := uint32(seed)*2654435761 + 12345
	for i := 0; i < 40; i++ {
		x = x*1664525 + 1013904223
		b.WriteByte(hex[(x>>24)&15])
	}
	return b.String()
}

type demo struct {
	now time.Time
	out []*unstructured.Unstructured
	n   int
}

func (d *demo) ts(ago time.Duration) string { return d.now.Add(-ago).UTC().Format(time.RFC3339) }

func (d *demo) cond(typ, status, reason, msg string, ago time.Duration) map[string]any {
	return map[string]any{"type": typ, "status": status, "reason": reason, "message": msg, "lastTransitionTime": d.ts(ago)}
}

type opt struct {
	suspend   bool
	revision  string // lastAppliedRevision (or artifact.revision for sources)
	conds     []any
	spec      map[string]any
	status    map[string]any
	handled   bool
	age       time.Duration
	artifact  bool
	inventory []any
}

func (d *demo) add(apiVersion, kind, ns, name string, o opt) {
	d.n++
	spec := map[string]any{"interval": "10m"}
	for k, v := range o.spec {
		spec[k] = v
	}
	if o.suspend {
		spec["suspend"] = true
	}
	status := map[string]any{"conditions": o.conds, "observedGeneration": int64(1)}
	if o.revision != "" {
		if o.artifact {
			status["artifact"] = map[string]any{"revision": o.revision, "path": "x.tar.gz"}
		} else {
			status["lastAppliedRevision"] = o.revision
			status["lastAttemptedRevision"] = o.revision
		}
	}
	if o.handled {
		status["lastHandledReconcileAt"] = d.ts(7 * time.Minute)
	}
	if o.inventory != nil {
		status["inventory"] = map[string]any{"entries": o.inventory}
	}
	for k, v := range o.status {
		status[k] = v
	}
	age := o.age
	if age == 0 {
		age = time.Duration(24*(5+d.n)) * time.Hour
	}
	d.out = append(d.out, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{
			"name": name, "namespace": ns, "uid": fmt.Sprintf("00000000-0000-4000-8000-%012d", d.n),
			"resourceVersion": fmt.Sprintf("%d", 1000+d.n), "creationTimestamp": d.ts(age),
		},
		"spec": spec, "status": status,
	}})
}

func (d *demo) ready(msg string) []any {
	return []any{d.cond("Ready", "True", "ReconciliationSucceeded", msg, 7*time.Minute)}
}

func ref(kind, name, ns string) map[string]any {
	m := map[string]any{"kind": kind, "name": name}
	if ns != "" {
		m["namespace"] = ns
	}
	return m
}

func inv(ns, name, group, kind, v string) any {
	return map[string]any{"id": ns + "_" + name + "_" + group + "_" + kind, "v": v}
}

// DemoObjects returns ~30 realistic Flux objects for the fake world of cluster.
func DemoObjects(cluster string, now time.Time) []*unstructured.Unstructured {
	d := &demo{now: now}
	rev := func(i int) string { return "main@sha1:" + sha(i) }

	// sources
	d.add(apiSource, "GitRepository", "flux-system", "flux-system", opt{revision: rev(1), artifact: true, conds: d.ready("stored artifact for revision '" + rev(1) + "'"), handled: true,
		spec: map[string]any{"url": "ssh://git@github.com/example/fleet", "ref": map[string]any{"branch": "main"}}})
	d.add(apiSource, "GitRepository", "team-2", "team-2-apps", opt{revision: rev(2), artifact: true,
		conds: []any{d.cond("Ready", "False", "AuthenticationFailed", "failed to checkout and determine revision: authentication required", 3*time.Hour)},
		spec:  map[string]any{"url": "https://github.com/example/team-2-apps", "ref": map[string]any{"branch": "main"}}})
	d.add(apiSource, "OCIRepository", "flux-system", "podinfo-manifests", opt{revision: "latest@sha256:" + sha(3) + sha(4)[:24], artifact: true, conds: d.ready("stored artifact for digest"), handled: true,
		spec: map[string]any{"url": "oci://ghcr.io/stefanprodan/manifests/podinfo", "ref": map[string]any{"tag": "latest"}}})
	d.add(apiSource, "HelmRepository", "flux-system", "bitnami", opt{revision: "sha256:" + sha(5) + sha(6)[:24], artifact: true, conds: d.ready("stored artifact: revision 'sha256:" + sha(5)[:12] + "'"), handled: true,
		spec: map[string]any{"url": "https://charts.bitnami.com/bitnami", "type": "default"}})
	d.add(apiSource, "HelmRepository", "flux-system", "podinfo", opt{revision: "sha256:" + sha(7) + sha(8)[:24], artifact: true, conds: d.ready("stored artifact"), handled: true,
		spec: map[string]any{"url": "https://stefanprodan.github.io/podinfo"}})
	d.add(apiSource, "HelmChart", "flux-system", "flux-system-podinfo", opt{revision: "6.5.4", artifact: true, conds: d.ready("pulled 'podinfo' chart with version '6.5.4'"), handled: true,
		spec: map[string]any{"chart": "podinfo", "version": "6.x", "sourceRef": ref("HelmRepository", "podinfo", "")}})
	d.add(apiSource, "Bucket", "flux-system", "assets", opt{revision: "sha256:" + sha(9) + sha(10)[:24], artifact: true, conds: d.ready("stored artifact"),
		spec: map[string]any{"bucketName": "fleet-assets", "endpoint": "s3.amazonaws.com", "provider": "aws"}})

	// kustomizations
	var entries []any
	for n := 1; n <= 4; n++ {
		ns := fmt.Sprintf("team-%d", n)
		entries = append(entries,
			inv(ns, fmt.Sprintf("svc-%d", n), "apps", "Deployment", "v1"),
			inv(ns, fmt.Sprintf("svc-%d", n), "", "Service", "v1"),
			inv(ns, fmt.Sprintf("svc-%d-config", n), "", "ConfigMap", "v1"))
	}
	ksSpec := func(src string, extra map[string]any) map[string]any {
		m := map[string]any{"sourceRef": ref("GitRepository", src, "flux-system"), "path": "./clusters/" + cluster, "prune": true}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	d.add(apiKustomize, "Kustomization", "flux-system", "flux-system", opt{revision: rev(1), conds: d.ready("Applied revision: " + rev(1)), handled: true, spec: ksSpec("flux-system", nil)})
	d.add(apiKustomize, "Kustomization", "flux-system", "infra", opt{revision: rev(1), conds: d.ready("Applied revision: " + rev(1)), handled: true, spec: ksSpec("flux-system", nil)})
	d.add(apiKustomize, "Kustomization", "flux-system", "apps", opt{revision: rev(1), conds: d.ready("Applied revision: " + rev(1)), handled: true, inventory: entries,
		spec: ksSpec("flux-system", map[string]any{"dependsOn": []any{ref("Kustomization", "infra", "")}, "targetNamespace": "team-1"})})
	d.add(apiKustomize, "Kustomization", "flux-system", "team-apps", opt{revision: rev(1), conds: d.ready("Applied revision: " + rev(1)), handled: true,
		spec: ksSpec("flux-system", map[string]any{"dependsOn": []any{ref("Kustomization", "apps", "")}})})
	d.add(apiKustomize, "Kustomization", "flux-system", "podinfo", opt{revision: "latest@sha256:" + sha(3), conds: d.ready("Applied revision: latest@sha256:" + sha(3)[:12]), handled: true,
		spec: map[string]any{"sourceRef": ref("OCIRepository", "podinfo-manifests", ""), "path": "./", "prune": true}})
	d.add(apiKustomize, "Kustomization", "team-2", "team-2-apps", opt{revision: rev(11),
		conds: []any{d.cond("Ready", "False", "BuildFailed", "kustomize build failed: accumulating resources: evalsymlink failure on './overlays/prod'", 40*time.Minute)},
		spec:  map[string]any{"sourceRef": ref("GitRepository", "team-2-apps", ""), "path": "./overlays/prod", "prune": true}})
	d.add(apiKustomize, "Kustomization", "team-3", "team-3-legacy", opt{suspend: true, revision: rev(12), conds: d.ready("Applied revision: " + rev(12)),
		spec: map[string]any{"sourceRef": ref("GitRepository", "flux-system", "flux-system"), "path": "./legacy", "prune": false}})
	d.add(apiKustomize, "Kustomization", "team-4", "team-4-rollout", opt{revision: rev(13),
		conds: []any{d.cond("Ready", "Unknown", "Progressing", "Reconciliation in progress", time.Minute), d.cond("Reconciling", "True", "Progressing", "Reconciliation in progress", time.Minute)},
		spec:  map[string]any{"sourceRef": ref("GitRepository", "flux-system", "flux-system"), "path": "./rollout", "prune": true}})
	d.add(apiKustomize, "Kustomization", "team-5", "team-5-broken", opt{revision: rev(14),
		conds: []any{d.cond("Ready", "False", "HealthCheckFailed", "health check failed after 5m0s: timeout waiting for Deployment/team-5/svc-5", 2*time.Hour), d.cond("Stalled", "True", "HealthCheckFailed", "health check failed", 2*time.Hour)},
		spec:  map[string]any{"sourceRef": ref("GitRepository", "flux-system", "flux-system"), "path": "./team-5", "prune": true, "wait": true}})
	d.add(apiKustomize, "Kustomization", "team-6", "team-6-waiting", opt{
		conds: []any{d.cond("Ready", "False", "DependencyNotReady", "dependency 'flux-system/team-apps' is not ready", 10*time.Minute)},
		spec:  map[string]any{"sourceRef": ref("GitRepository", "flux-system", "flux-system"), "path": "./team-6", "dependsOn": []any{ref("Kustomization", "team-apps", "flux-system")}}})

	// helm releases
	hrSpec := func(chart, version string, extra map[string]any) map[string]any {
		m := map[string]any{"chart": map[string]any{"spec": map[string]any{"chart": chart, "version": version, "sourceRef": ref("HelmRepository", "bitnami", "flux-system")}}, "install": map[string]any{"remediation": map[string]any{"retries": int64(3)}}}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	hist := func(v string, status string) []any {
		return []any{map[string]any{"chartName": "chart", "chartVersion": v, "status": status, "name": "rel", "namespace": "x", "version": int64(2)}}
	}
	d.add(apiHelm, "HelmRelease", "flux-system", "podinfo", opt{revision: "6.5.4", conds: d.ready("Helm upgrade succeeded for release flux-system/podinfo.v3 with chart podinfo@6.5.4"), handled: true,
		spec:   map[string]any{"chart": map[string]any{"spec": map[string]any{"chart": "podinfo", "version": "6.x", "sourceRef": ref("HelmRepository", "podinfo", "flux-system")}}},
		status: map[string]any{"history": hist("6.5.4", "deployed")}})
	d.add(apiHelm, "HelmRelease", "team-7", "redis", opt{conds: d.ready("Helm install succeeded for release team-7/redis.v1 with chart redis@19.0.1"), handled: true,
		spec: hrSpec("redis", "19.x", nil), status: map[string]any{"history": hist("19.0.1", "deployed")}})
	d.add(apiHelm, "HelmRelease", "team-8", "postgres", opt{
		conds:  []any{d.cond("Ready", "False", "UpgradeFailed", "Helm upgrade failed for release team-8/postgres with chart postgresql@15.2.0: context deadline exceeded", 25*time.Minute)},
		spec:   hrSpec("postgresql", "15.x", nil),
		status: map[string]any{"history": hist("15.2.0", "failed"), "failures": int64(3), "upgradeFailures": int64(3), "installFailures": int64(0)}})
	d.add(apiHelm, "HelmRelease", "team-9", "kafka", opt{suspend: true, conds: d.ready("Helm install succeeded"),
		spec: hrSpec("kafka", "28.x", nil), status: map[string]any{"history": hist("28.0.2", "deployed")}})
	d.add(apiHelm, "HelmRelease", "team-10", "nginx", opt{conds: d.ready("Helm install succeeded for release team-10/nginx.v1"), handled: true,
		spec: hrSpec("nginx", "18.x", map[string]any{"dependsOn": []any{ref("HelmRelease", "redis", "team-7")}}), status: map[string]any{"history": hist("18.1.0", "deployed")}})
	d.add(apiHelm, "HelmRelease", "team-11", "rabbitmq", opt{
		conds: []any{d.cond("Ready", "Unknown", "Progressing", "Running 'install' action with timeout of 5m0s", time.Minute), d.cond("Reconciling", "True", "Progressing", "Running 'install' action", time.Minute)},
		spec:  hrSpec("rabbitmq", "14.x", nil)})
	d.add(apiHelm, "HelmRelease", "team-12", "grafana", opt{conds: d.ready("Helm upgrade succeeded"), handled: true,
		spec: map[string]any{"chartRef": ref("OCIRepository", "podinfo-manifests", "flux-system")}, status: map[string]any{"history": hist("8.0.0", "deployed")}})
	for n := 13; n <= 15; n++ {
		ns := fmt.Sprintf("team-%d", n)
		d.add(apiKustomize, "Kustomization", ns, ns+"-apps", opt{revision: rev(20 + n), conds: d.ready("Applied revision: " + rev(20+n)), handled: true,
			spec: map[string]any{"sourceRef": ref("GitRepository", "flux-system", "flux-system"), "path": "./" + ns, "prune": true}})
		d.add(apiHelm, "HelmRelease", ns, "svc-"+fmt.Sprint(n)+"-cache", opt{conds: d.ready("Helm upgrade succeeded"), handled: true,
			spec: hrSpec("redis", "19.x", nil), status: map[string]any{"history": hist("19.0.1", "deployed")}})
	}
	return d.out
}

// FakeReconcile simulates the Flux controllers: when requestedAt differs from lastHandledReconcileAt it
// marks the request handled, refreshes the Ready condition and, for a HelmRelease force/reset, clears the
// remediation counters. The fake world calls it shortly after a patch.
func FakeReconcile(o *unstructured.Unstructured) (token string, changed bool) {
	req := o.GetAnnotations()[AnnRequestedAt]
	handled, _, _ := unstructured.NestedString(o.Object, "status", "lastHandledReconcileAt")
	if req == "" || req == handled {
		return req, false
	}
	_ = unstructured.SetNestedField(o.Object, req, "status", "lastHandledReconcileAt")
	conds, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	for i, c := range conds {
		m, ok := c.(map[string]any)
		if !ok || m["type"] != "Ready" {
			continue
		}
		m["lastTransitionTime"] = req
		m["message"] = "Reconciliation finished"
		conds[i] = m
	}
	if conds != nil {
		_ = unstructured.SetNestedSlice(o.Object, conds, "status", "conditions")
	}
	if o.GetKind() == "HelmRelease" {
		ann := o.GetAnnotations()
		if ann[AnnForceAt] == req || ann[AnnResetAt] == req {
			for _, k := range []string{"failures", "installFailures", "upgradeFailures"} {
				unstructured.RemoveNestedField(o.Object, "status", k)
			}
		}
	}
	return req, true
}
