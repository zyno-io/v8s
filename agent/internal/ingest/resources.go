package ingest

import (
	corev1 "k8s.io/api/core/v1"
)

// podRes computes a pod's effective requests and limits with Kubernetes semantics. It mirrors
// k8s.io/component-helpers/resource PodRequests/PodLimits (regular containers summed, init containers
// maxed against the running total, restartable init containers (sidecars) counted as running, a
// pod-level resources block overriding, overhead added) using scalar int64 arithmetic only: no
// ResourceList maps and no allocation.
//
// A limit counts only if every regular container and every sidecar has a non-zero limit for the
// resource, or a pod-level limit is set; otherwise the pod is unbounded and Lim is 0 (shown n/a).
// Non-restartable init containers are left out of that check but still count in the init maximum.
// The source is spec (desired), not in-place-resize status.
func podRes(p *corev1.Pod) Res {
	sp := &p.Spec
	if len(sp.Overhead) == 0 && sp.Resources == nil && noResources(sp) {
		return Res{}
	}
	var out Res
	c, _ := podResource(sp, corev1.ResourceCPU, false)
	out.CPUReq = clampU32(c)
	c, ok := podResource(sp, corev1.ResourceCPU, true)
	if ok {
		out.CPULim = clampU32(c)
	}
	m, _ := podResource(sp, corev1.ResourceMemory, false)
	out.MemReq = clampU64(m)
	m, ok = podResource(sp, corev1.ResourceMemory, true)
	if ok {
		out.MemLim = clampU64(m)
	}
	return out
}

func noResources(sp *corev1.PodSpec) bool {
	for i := range sp.Containers {
		r := &sp.Containers[i].Resources
		if len(r.Requests)+len(r.Limits) != 0 {
			return false
		}
	}
	for i := range sp.InitContainers {
		r := &sp.InitContainers[i].Resources
		if len(r.Requests)+len(r.Limits) != 0 {
			return false
		}
	}
	return true
}

// qty reads one resource of a container's requests or limits as millicores (cpu) or bytes (memory).
func qty(rl corev1.ResourceList, name corev1.ResourceName) int64 {
	if len(rl) == 0 {
		return 0
	}
	q, ok := rl[name]
	if !ok {
		return 0
	}
	if name == corev1.ResourceCPU {
		return q.MilliValue()
	}
	return q.Value()
}

// podResource returns the effective request (limits=false) or limit (limits=true) of one resource.
// For limits, ok is false when the pod is unbounded for it.
func podResource(sp *corev1.PodSpec, name corev1.ResourceName, limits bool) (v int64, ok bool) {
	pick := func(r *corev1.ResourceRequirements) int64 {
		if limits {
			return qty(r.Limits, name)
		}
		return qty(r.Requests, name)
	}
	var sum, side, initMax int64
	bounded := true
	for i := range sp.Containers {
		q := pick(&sp.Containers[i].Resources)
		if limits && q == 0 {
			bounded = false
		}
		sum += q
	}
	for i := range sp.InitContainers {
		ic := &sp.InitContainers[i]
		q := pick(&ic.Resources)
		var cur int64
		if ic.RestartPolicy != nil && *ic.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			if limits && q == 0 {
				bounded = false
			}
			sum += q
			side += q
			cur = side
		} else {
			cur = q + side // runs alongside the earlier sidecars
		}
		if cur > initMax {
			initMax = cur
		}
	}
	eff := sum
	if initMax > eff {
		eff = initMax
	}
	if sp.Resources != nil {
		var pl int64
		if limits {
			pl = qty(sp.Resources.Limits, name)
		} else {
			pl = qty(sp.Resources.Requests, name)
		}
		if pl != 0 {
			eff = pl
			bounded = true
		}
	}
	if limits {
		if !bounded || eff == 0 {
			return 0, false
		}
		return eff + qty(sp.Overhead, name), true
	}
	return eff + qty(sp.Overhead, name), true
}

func clampU32(v int64) uint32 {
	if v <= 0 {
		return 0
	}
	if v > 1<<32-1 {
		return 1<<32 - 1
	}
	return uint32(v)
}

func clampU64(v int64) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v)
}
