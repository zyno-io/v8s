package security

import (
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/platform"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// Exposure of workloads (protocol.Exposure* bits), resolved from the agent's watched store with no API call:
//
//	Service           ← EndpointSlice (targetRef pods)  → pod → owner → workload
//	Service.type       → NodePort / LoadBalancer bits of that Service
//	Ingress, routes    → Ingress bit of the Services they name as backends
//
// One pass over Services, Ingresses, routes and EndpointSlices, then one over pods (and Jobs when an exposed pod
// belongs to one); only exposed pods allocate.

const (
	gvrPods      = "v1/pods"
	gvrServices  = "v1/services"
	gvrSlices    = "discovery.k8s.io/v1/endpointslices"
	gvrIngresses = "networking.k8s.io/v1/ingresses"
	gvrJobs      = "batch/v1/jobs"
)

// gvrRoutes are the Gateway API route kinds whose backends count as Ingress, at every version the platform module
// may watch (the store holds whichever the cluster serves; an unwatched gvr scans nothing).
var gvrRoutes = []string{
	"gateway.networking.k8s.io/v1/httproutes", "gateway.networking.k8s.io/v1beta1/httproutes",
	"gateway.networking.k8s.io/v1/grpcroutes", "gateway.networking.k8s.io/v1alpha2/grpcroutes",
}

type nsName struct{ ns, name string }

// Exposure maps a workload (as WorkloadRef after NormalizeWorkload: Deployment, StatefulSet, DaemonSet, Job,
// CronJob, Pod, or the raw owner kind) to its exposure bits.
type Exposure map[WorkloadRef]int

// exposureInputs must be watched and synced for a cluster's exposure to be known.
var exposureInputs = []string{gvrServices, gvrSlices, gvrPods, gvrIngresses}

// ExposureOf resolves the exposure of every workload of a cluster with exposed pods (nil when none is). known is
// false when an input is not loaded yet or not readable (synced reports it; a watched route kind must be synced
// too): the result would read as "not exposed" while it is only unknown.
func ExposureOf(scan ScanFn, synced func(cluster, gvr string) bool, cluster string) (ex Exposure, known bool) {
	if scan == nil {
		return nil, false
	}
	if synced != nil {
		for _, gvr := range exposureInputs {
			if !synced(cluster, gvr) {
				return nil, false
			}
		}
		for _, gvr := range gvrRoutes {
			if scan(cluster, gvr, func(*ingest.Row) bool { return false }) && !synced(cluster, gvr) {
				return nil, false
			}
		}
	}
	return exposureOf(scan, cluster), true
}

func exposureOf(scan ScanFn, cluster string) Exposure {
	// Service → bits from its type, then from the Ingresses / routes that point at it.
	svc := map[nsName]int{}
	if mod := ingest.Lookup(gvrServices); mod != nil {
		col := mod.Col("type")
		scan(cluster, gvrServices, func(r *ingest.Row) bool {
			bits := protocol.ExposureService
			if col != nil && col.Src == ingest.SrcCell {
				switch r.StrCell(col.Idx) {
				case "NodePort":
					bits |= protocol.ExposureNodePort
				case "LoadBalancer":
					bits |= protocol.ExposureLoadBalancer
				}
			}
			svc[nsName{r.NS().Value(), r.Name()}] = bits
			return true
		})
	}
	if len(svc) == 0 {
		return nil
	}
	routed := func(k nsName) {
		if b, ok := svc[k]; ok {
			svc[k] = b | protocol.ExposureIngress
		}
	}
	scan(cluster, gvrIngresses, func(r *ingest.Row) bool {
		if f, ok := r.Facts().(*ingest.IngressFacts); ok {
			ns := r.NS().Value()
			for _, s := range f.Services {
				routed(nsName{ns, s})
			}
		}
		return true
	})
	for _, gvr := range gvrRoutes {
		scan(cluster, gvr, func(r *ingest.Row) bool {
			if f, ok := r.Facts().(*platform.RouteFacts); ok {
				for _, b := range f.Backends {
					routed(nsName{b.NS, b.Name})
				}
			}
			return true
		})
	}

	// Pod → union of the bits of the Services whose slices target it.
	pods := map[nsName]int{}
	scan(cluster, gvrSlices, func(r *ingest.Row) bool {
		f, ok := r.Facts().(*ingest.EndpointSliceFacts)
		if !ok {
			return true
		}
		ns := r.NS().Value()
		bits := svc[nsName{ns, r.StrCell(0)}] // cell 0: the kubernetes.io/service-name label
		if bits == 0 {
			return true
		}
		for _, p := range f.Pods {
			k := nsName{ns, p}
			pods[k] |= bits
		}
		return true
	})
	if len(pods) == 0 {
		return nil
	}

	// Workload ← pod: the pod itself, its owner, and the owner's rollup (ReplicaSet → Deployment by name, as the
	// scanners do; Job → CronJob from the Job's own owner reference).
	out := Exposure{}
	add := func(w WorkloadRef, bits int) { out[w] |= bits }
	jobs := map[nsName]int{}
	scan(cluster, gvrPods, func(r *ingest.Row) bool {
		ns := r.NS().Value()
		bits := pods[nsName{ns, r.Name()}]
		if bits == 0 {
			return true
		}
		add(WorkloadRef{"Pod", ns, r.Name()}, bits)
		kind, name := r.OwnerKind().Value(), r.OwnerName().Value()
		if kind == "" || name == "" {
			return true
		}
		add(WorkloadRef{kind, ns, name}, bits)
		switch kind {
		case "ReplicaSet":
			add(NormalizeWorkload(kind, ns, name), bits)
		case "Job":
			jobs[nsName{ns, name}] |= bits
		}
		return true
	})
	if len(jobs) > 0 {
		scan(cluster, gvrJobs, func(r *ingest.Row) bool {
			ns := r.NS().Value()
			if bits := jobs[nsName{ns, r.Name()}]; bits != 0 && r.OwnerKind().Value() == "CronJob" {
				add(WorkloadRef{"CronJob", ns, r.OwnerName().Value()}, bits)
			}
			return true
		})
	}
	return out
}
