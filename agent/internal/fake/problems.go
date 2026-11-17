package fake

import (
	"fmt"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Fixtures for the problems engine (docs/problems.md): evidence times on the broken objects the world already
// has, plus Ingresses that route nowhere, a pending PVC with its Warning event, a failed bare Job and a node
// under pressure. The pod generator already produces crash loops, image pull failures, OOM kills and pending
// pods; Flux, Helm, policy, vulnerability, certificate and flow fixtures live with their modules.
func init() { registerGen(genProblemFixtures) }

func ago(d time.Duration) string { return ts(time.Now().Add(-d)) }

func setStatusConds(o *unstructured.Unstructured, conds []any) {
	_ = unstructured.SetNestedSlice(o.Object, conds, "status", "conditions")
}

func genProblemFixtures(w *World, c *Cluster) {
	// nodes: evidence times for the NotReady ones, one under memory pressure
	for _, o := range w.objs["v1/nodes"] {
		var name = o.GetName()
		i, _ := strconv.Atoi(name[len("node-"):])
		switch {
		case i%47 == 5:
			setStatusConds(o, []any{map[string]any{"type": "Ready", "status": "False", "reason": "KubeletNotReady",
				"message": "container runtime network not ready", "lastTransitionTime": ago(3 * time.Hour)}})
		case i == 9:
			setStatusConds(o, []any{
				map[string]any{"type": "Ready", "status": "True", "lastTransitionTime": ago(48 * time.Hour)},
				map[string]any{"type": "MemoryPressure", "status": "True", "reason": "KubeletHasInsufficientMemory", "message": "memory pressure", "lastTransitionTime": ago(40 * time.Minute)}})
		}
	}
	// deployments with no available replica: failing rollouts with evidence
	for _, o := range w.objs["apps/v1/deployments"] {
		var avail int64
		if v, ok, _ := unstructured.NestedInt64(o.Object, "status", "availableReplicas"); ok {
			avail = v
		}
		if avail != 0 {
			continue
		}
		setStatusConds(o, []any{
			map[string]any{"type": "Available", "status": "False", "reason": "MinimumReplicasUnavailable", "message": "Deployment does not have minimum availability.", "lastTransitionTime": ago(2 * time.Hour)},
			map[string]any{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded", "message": "ReplicaSet has timed out progressing.", "lastTransitionTime": ago(100 * time.Minute)}})
	}
	// ingresses routing to a missing Service and to a Service without ready endpoints
	for _, a := range []int{7, 68} { // 7 and 68 have avail == 0 (a%61 == 7)
		name, ns := "svc-"+strconv.Itoa(a), teamNS(a)
		w.putLocked("networking.k8s.io/v1/ingresses", map[string]any{"metadata": objMeta(ns, name+"-public", nil),
			"spec": map[string]any{"ingressClassName": "nginx", "rules": []any{map[string]any{"host": name + ".example.com", "http": map[string]any{"paths": []any{
				map[string]any{"path": "/", "pathType": "Prefix", "backend": map[string]any{"service": map[string]any{"name": name, "port": map[string]any{"number": int64(80)}}}},
				map[string]any{"path": "/old", "pathType": "Prefix", "backend": map[string]any{"service": map[string]any{"name": name + "-gone", "port": map[string]any{"number": int64(80)}}}}}}}}},
			"status": map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": "203.0.113.20"}}}}})
	}
	// a PVC that never binds, with the provisioner's Warning event
	w.putLocked("v1/persistentvolumeclaims", map[string]any{"metadata": objMeta(teamNS(4), "elastic-data-2", nil),
		"spec":   map[string]any{"storageClassName": "fast-ssd", "accessModes": []any{"ReadWriteOnce"}},
		"status": map[string]any{"phase": "Pending"}})
	w.objs["v1/persistentvolumeclaims"][key(teamNS(4), "elastic-data-2")].Object["metadata"].(map[string]any)["creationTimestamp"] = ago(6 * time.Hour)
	w.putLocked("v1/events", map[string]any{"metadata": objMeta(teamNS(4), "elastic-data-2.17f1", nil), "type": "Warning", "reason": "ProvisioningFailed",
		"message": `storageclass.storage.k8s.io "fast-ssd" not found`, "count": int64(12),
		"involvedObject": map[string]any{"kind": "PersistentVolumeClaim", "name": "elastic-data-2", "namespace": teamNS(4)}, "lastTimestamp": ago(2 * time.Minute)})
	// a bare Job that failed (backoff limit)
	jm := objMeta(teamNS(6), "nightly-export", nil)
	w.putLocked("batch/v1/jobs", map[string]any{"metadata": jm, "spec": map[string]any{"completions": int64(1), "backoffLimit": int64(1)},
		"status": map[string]any{"failed": int64(2), "startTime": ago(5 * time.Hour),
			"conditions": []any{map[string]any{"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded", "message": "Job has reached the specified backoff limit",
				"lastTransitionTime": ago(4 * time.Hour)}}}})
	// the cron jobs' failed Jobs: evidence time
	for _, o := range w.objs["batch/v1/jobs"] {
		conds, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
		for _, cd := range conds {
			if m, ok := cd.(map[string]any); ok && m["type"] == "Failed" && m["lastTransitionTime"] == nil {
				m["reason"], m["message"], m["lastTransitionTime"] = "BackoffLimitExceeded", "Job has reached the specified backoff limit", ago(3*time.Hour-time.Duration(len(o.GetName()))*time.Second)
			}
		}
		_ = unstructured.SetNestedSlice(o.Object, conds, "status", "conditions")
	}
	_ = fmt.Sprint
}
