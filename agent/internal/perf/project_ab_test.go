package perf

import (
	"testing"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// This file uses only corev1 and the fake pod list, so it builds unchanged on main: both trees can measure Project over
// the very same pods, with and without requests and limits.

func abPods(b *testing.B, withRes bool) []corev1.Pod {
	c := fake.NewCluster("ab", 10000, 0, 1)
	defer c.Stop()
	pods := listPods(b, c)
	q := func(s string) resource.Quantity { return resource.MustParse(s) }
	profiles := []corev1.ResourceRequirements{
		{Requests: corev1.ResourceList{"cpu": q("100m"), "memory": q("128Mi")}, Limits: corev1.ResourceList{"cpu": q("500m"), "memory": q("256Mi")}},
		{Requests: corev1.ResourceList{"cpu": q("250m"), "memory": q("256Mi")}, Limits: corev1.ResourceList{"memory": q("512Mi")}},
		{},
		{Requests: corev1.ResourceList{"cpu": q("50m"), "memory": q("64Mi")}, Limits: corev1.ResourceList{"cpu": q("200m"), "memory": q("128Mi")}},
		{Requests: corev1.ResourceList{"cpu": q("1"), "memory": q("1Gi")}, Limits: corev1.ResourceList{"cpu": q("2"), "memory": q("2Gi")}},
		{Requests: corev1.ResourceList{"cpu": q("100m"), "memory": q("128Mi")}, Limits: corev1.ResourceList{"cpu": q("100m"), "memory": q("128Mi")}},
	}
	overhead := corev1.ResourceList{"cpu": q("250m"), "memory": q("120Mi")}
	for i := range pods {
		p := &pods[i]
		p.Spec.Overhead, p.Spec.InitContainers, p.Status.InitContainerStatuses = nil, nil, nil // identical pods on both trees
		cs := make([]corev1.Container, len(p.Spec.Containers))
		copy(cs, p.Spec.Containers)
		for j := range cs {
			cs[j].Resources = corev1.ResourceRequirements{}
			if withRes {
				cs[j].Resources = profiles[i%6] // shared read-only lists, as the fake does
			}
		}
		p.Spec.Containers = cs
		if withRes && i%50 == 3 {
			p.Spec.Overhead = overhead
		}
	}
	return pods
}

func benchAB(b *testing.B, withRes bool) {
	pods := abPods(b, withRes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range pods {
			_ = ingest.Project(&pods[j])
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e6, "ms/10k-pods")
}

// BenchmarkProjectABNoResources and BenchmarkProjectABWithResources project the same 10k fake pods, without and with
// requests, limits and overhead (six profiles).
func BenchmarkProjectABNoResources(b *testing.B)   { benchAB(b, false) }
func BenchmarkProjectABWithResources(b *testing.B) { benchAB(b, true) }
