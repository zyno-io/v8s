package ingest

import (
	corev1 "k8s.io/api/core/v1"
)

// podRes computes a pod's effective requests and limits with Kubernetes semantics. It mirrors
// k8s.io/component-helpers/resource PodRequests/PodLimits (regular containers summed, init containers
// maxed against the running total, restartable init containers (sidecars) counted as running, a
// pod-level resources block overriding, overhead added) using scalar int64 arithmetic only: no
// ResourceList maps built and no allocation. Each container is read once into all four amounts (cpu and
// memory, requests and limits), not once per resource.
//
// A limit counts only if every regular container and every sidecar has a non-zero limit for the
// resource, or a pod-level limit is set; otherwise the pod is unbounded and Lim is 0 (shown n/a).
// Non-restartable init containers are left out of that check but still count in the init maximum.
// The source is spec (desired), not in-place resize status.
func podRes(p *corev1.Pod) Res {
	sp := &p.Spec
	if len(sp.Overhead) == 0 && sp.Resources == nil && noResources(sp) {
		return Res{}
	}
	var sum, side, initMax [4]int64
	bounded := [2]bool{true, true} // limits: cpu, memory
	for i := range sp.Containers {
		a := amounts(&sp.Containers[i].Resources)
		for k := range a {
			sum[k] += a[k]
		}
		bounded[0] = bounded[0] && a[iLimCPU] != 0
		bounded[1] = bounded[1] && a[iLimMem] != 0
	}
	for i := range sp.InitContainers {
		ic := &sp.InitContainers[i]
		a := amounts(&ic.Resources)
		var cur [4]int64
		if ic.RestartPolicy != nil && *ic.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			for k := range a {
				sum[k] += a[k]
				side[k] += a[k]
			}
			cur = side
			bounded[0] = bounded[0] && a[iLimCPU] != 0
			bounded[1] = bounded[1] && a[iLimMem] != 0
		} else {
			for k := range a {
				cur[k] = a[k] + side[k] // runs alongside the earlier sidecars
			}
		}
		for k := range cur {
			initMax[k] = max(initMax[k], cur[k])
		}
	}
	var eff [4]int64
	for k := range eff {
		eff[k] = max(sum[k], initMax[k])
	}
	if sp.Resources != nil {
		pl := amounts(sp.Resources)
		for k := range pl {
			if pl[k] != 0 {
				eff[k] = pl[k]
				if k == iLimCPU {
					bounded[0] = true
				} else if k == iLimMem {
					bounded[1] = true
				}
			}
		}
	}
	ov := amounts(&corev1.ResourceRequirements{Requests: sp.Overhead})
	eff[iReqCPU] += ov[iReqCPU]
	eff[iReqMem] += ov[iReqMem]
	var out Res
	out.CPUReq, out.MemReq = clampU32(eff[iReqCPU]), clampU64(eff[iReqMem])
	if bounded[0] && eff[iLimCPU] != 0 {
		out.CPULim = clampU32(eff[iLimCPU] + ov[iReqCPU]) // overhead only on non-zero limits
	}
	if bounded[1] && eff[iLimMem] != 0 {
		out.MemLim = clampU64(eff[iLimMem] + ov[iReqMem])
	}
	return out
}

const (
	iReqCPU = iota
	iReqMem
	iLimCPU
	iLimMem
)

// amounts reads a container's requests and limits: millicores for cpu, bytes for memory.
func amounts(r *corev1.ResourceRequirements) (a [4]int64) {
	if len(r.Requests) != 0 {
		if q, ok := r.Requests[corev1.ResourceCPU]; ok {
			a[iReqCPU] = q.MilliValue()
		}
		if q, ok := r.Requests[corev1.ResourceMemory]; ok {
			a[iReqMem] = q.Value()
		}
	}
	if len(r.Limits) != 0 {
		if q, ok := r.Limits[corev1.ResourceCPU]; ok {
			a[iLimCPU] = q.MilliValue()
		}
		if q, ok := r.Limits[corev1.ResourceMemory]; ok {
			a[iLimMem] = q.Value()
		}
	}
	return a
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
