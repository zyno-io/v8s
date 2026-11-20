package fake

import (
	"strconv"
	"time"

	_ "github.com/zyno-io/v8s/agent/internal/vpa" // the module that reads the kind below
)

// VerticalPodAutoscalers: one per app where app%10 == 4, named and targeted like the app's Deployment. Mode is
// Auto, except app%20 == 14 which is Off (its pods carry no vpaUpdates annotation). App 4 has no recommendation
// yet and no matching pods (NoPodsMatched). The others recommend per container from the pod's profile.

func init() {
	registerKinds(&kindDef{gvr: "autoscaling.k8s.io/v1/verticalpodautoscalers", apiVersion: "autoscaling.k8s.io/v1", kind: "VerticalPodAutoscaler",
		resource: "verticalpodautoscalers", namespaced: true, short: []string{"vpa"}})
	registerGen(genVPA)
}

// vpaApp reports whether app has a VPA, and whether it is in Auto mode (its pods get the vpaUpdates annotation).
func vpaApp(app int) (has, auto bool) {
	has = app%10 == 4
	return has, has && app%20 != 14 && app != 4
}

func vpaAnnotation(app int) string {
	return "Pod resources updated by svc-" + strconv.Itoa(app) + ": container 0: cpu request, memory request"
}

func genVPA(w *World, c *Cluster) {
	for a := 0; a < apps; a++ {
		if has, _ := vpaApp(a); !has {
			continue
		}
		name := "svc-" + strconv.Itoa(a)
		mode := "Auto"
		if a%20 == 14 {
			mode = "Off"
		}
		status := map[string]any{"conditions": []any{cond("RecommendationProvided", "True", "", "", 3*time.Hour)}}
		if a == 4 {
			status = map[string]any{"conditions": []any{cond("NoPodsMatched", "True", "NoPodsMatched", "No pods match this VPA object", 3*time.Hour)}}
		} else {
			nc := 1 + a%3
			recs := make([]any, 0, nc)
			for i := 0; i < nc; i++ {
				cn, prof := name, &mainProfiles[a%6]
				if i > 0 {
					cn, prof = "sidecar-"+strconv.Itoa(i), &sideProfiles[a%6]
				}
				cpu, mem := prof.cpuReq, prof.memReq>>20 // millicores, MiB; BestEffort profiles get a floor
				if cpu == 0 {
					cpu = 25
				}
				if mem == 0 {
					mem = 64
				}
				f := int64([]int{70, 90, 130}[(a/10)%3]) // the recommendation sits below, near or above the current request
				cpu, mem = cpu*f/100, mem*f/100
				q := func(cpu, mem int64) map[string]any {
					return map[string]any{"cpu": strconv.FormatInt(cpu, 10) + "m", "memory": strconv.FormatInt(mem, 10) + "Mi"}
				}
				recs = append(recs, map[string]any{"containerName": cn, "target": q(cpu, mem), "lowerBound": q(cpu*6/10, mem*6/10), "upperBound": q(cpu*25/10, mem*25/10)})
			}
			status["recommendation"] = map[string]any{"containerRecommendations": recs}
		}
		w.putLocked("autoscaling.k8s.io/v1/verticalpodautoscalers", map[string]any{"metadata": objMeta(teamNS(a), name, nil),
			"spec": map[string]any{"targetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": name},
				"updatePolicy": map[string]any{"updateMode": mode}},
			"status": status})
	}
}
