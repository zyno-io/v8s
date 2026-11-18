package fake

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/usage"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Fake resources and metrics (docs/protocol.md "fake mode"): six request/limit profiles shared read-only by
// every pod (no MustParse per pod), and a deterministic metrics.k8s.io that varies every 15 s like the real one.

// resProfile is one container's resources as pre-built lists plus the amounts they hold.
type resProfile struct {
	req, lim       corev1.ResourceList
	cpuReq, cpuLim int64 // millicores, 0 = none
	memReq, memLim int64 // bytes, 0 = none
}

func mkProfile(cpuReq, memReq, cpuLim, memLim string) resProfile {
	p := resProfile{}
	add := func(l *corev1.ResourceList, name corev1.ResourceName, v string) int64 {
		if v == "" {
			return 0
		}
		q := resource.MustParse(v)
		if *l == nil {
			*l = corev1.ResourceList{}
		}
		(*l)[name] = q
		if name == corev1.ResourceCPU {
			return q.MilliValue()
		}
		return q.Value()
	}
	p.cpuReq = add(&p.req, corev1.ResourceCPU, cpuReq)
	p.memReq = add(&p.req, corev1.ResourceMemory, memReq)
	p.cpuLim = add(&p.lim, corev1.ResourceCPU, cpuLim)
	p.memLim = add(&p.lim, corev1.ResourceMemory, memLim)
	return p
}

var (
	// mainProfiles are the app container's resources by app%6: (0) burstable with both limits, (1) memory
	// limit only (%CPU/L is n/a), (2) BestEffort, (3) small burstable (plus a native sidecar), (4) one core,
	// (5) Guaranteed.
	mainProfiles = [6]resProfile{
		mkProfile("100m", "128Mi", "500m", "256Mi"),
		mkProfile("250m", "256Mi", "", "512Mi"),
		{},
		mkProfile("50m", "64Mi", "200m", "128Mi"),
		mkProfile("1", "1Gi", "2", "2Gi"),
		mkProfile("100m", "128Mi", "100m", "128Mi"),
	}
	// sideProfiles are the smaller resources of "sidecar-N" containers.
	sideProfiles = [6]resProfile{
		mkProfile("20m", "32Mi", "100m", "64Mi"),
		mkProfile("20m", "32Mi", "", "64Mi"),
		{},
		mkProfile("10m", "16Mi", "50m", "32Mi"),
		mkProfile("50m", "64Mi", "100m", "128Mi"),
		mkProfile("20m", "32Mi", "20m", "32Mi"),
	}
	// nativeSidecar is the restartable init container of profile-3 running pods.
	nativeSidecar = mkProfile("10m", "32Mi", "10m", "32Mi")
	// podOverhead is the RuntimeClass overhead of every 50th app (app%50==3).
	podOverhead = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("120Mi")}
)

func hasNativeSidecar(p *fakePod) bool { return p.app%6 == 3 && p.state == stRunning }
func hasOverhead(p *fakePod) bool      { return p.app%50 == 3 }

// podCaps returns what a pod's usage varies within: its limits (bounded) or, when it has none, a stand-in.
func podCaps(p *fakePod) (cpu, mem float64, cpuBounded, memBounded bool) {
	pr := p.app % 6
	cpuBounded, memBounded = true, true
	var reqC, reqM int64
	var limC, limM int64
	add := func(r *resProfile) {
		reqC += r.cpuReq
		reqM += r.memReq
		limC += r.cpuLim
		limM += r.memLim
		if r.cpuLim == 0 {
			cpuBounded = false
		}
		if r.memLim == 0 {
			memBounded = false
		}
	}
	add(&mainProfiles[pr])
	for i := 1; i < int(p.nc); i++ {
		add(&sideProfiles[pr])
	}
	if hasNativeSidecar(p) {
		add(&nativeSidecar)
	}
	cpu, mem = float64(limC), float64(limM)
	if !cpuBounded {
		cpu = float64(max(reqC*2, 200))
	}
	if !memBounded {
		mem = float64(max(reqM*2, 128<<20))
	}
	return
}

func mix(x uint64) uint64 { // splitmix64
	x += 0x9e3779b97f4a7c15
	x = (x ^ x>>30) * 0xbf58476d1ce4e5b9
	x = (x ^ x>>27) * 0x94d049bb133111eb
	return x ^ x>>31
}

func frac(f float64) float64 { return f - math.Floor(f) }

// podUsage is the deterministic usage of one pod in the 15 s window t: CPU millicores and memory bytes.
func podUsage(p *fakePod, t int64) (cpu, mem int64) {
	capC, capM, _, _ := podCaps(p)
	h := mix(p.id)
	f := 0.15 + 0.7*frac(float64(h)/(1<<64)+0.07*float64(t))
	g := 0.25 + 0.5*frac(float64(mix(h))/(1<<64)+0.011*float64(t))
	c, m := capC*f, capM*g
	if h%17 == 0 { // hot CPU: 95-102 % of the limit
		c = capC * (0.95 + 0.07*frac(float64(h>>8)/(1<<56)+0.2*float64(t)))
	}
	if h%23 == 0 { // hot memory: 90-98 % of the limit
		m = capM * (0.90 + 0.08*frac(float64(h>>16)/(1<<48)+0.1*float64(t)))
	}
	return max(int64(c), 1), int64(m)
}

// reportsMetrics: pods that are not running anything have no metrics.
func reportsMetrics(p *fakePod) bool {
	switch p.state {
	case stPending, stImagePull, stCompleted, stError, stOOM, stDead:
		return false
	}
	return true
}

func window(now time.Time) (t int64, at time.Time) {
	t = now.Unix() / 15
	return t, time.Unix(t*15, 0)
}

var _ kube.MetricsReader = (*Backend)(nil)

// PodMetrics implements kube.MetricsReader: the usage of one namespace ("" = all) at the current 15 s window.
func (b *Backend) PodMetrics(ctx context.Context, namespace string) ([]usage.PodSample, error) {
	if b.noMetrics {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io", Resource: "pods"}, "")
	}
	t, at := window(time.Now())
	ms := at.UnixMilli()
	c := b.c
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]usage.PodSample, 0, len(c.pods))
	for i := range c.pods {
		p := &c.pods[i]
		if !reportsMetrics(p) {
			continue
		}
		ns := "team-" + strconv.Itoa(int(p.ns))
		if namespace != "" && ns != namespace {
			continue
		}
		cpu, mem := podUsage(p, t)
		out = append(out, usage.PodSample{NS: ns, Name: p.name(), At: ms, CPU: uint32(cpu), MemKi: uint32(mem >> 10)})
	}
	return out, nil
}

// NodeMetrics implements kube.MetricsReader: each node is the sum of its pods plus system usage.
func (b *Backend) NodeMetrics(ctx context.Context) ([]usage.NodeSample, error) {
	if b.noMetrics {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io", Resource: "nodes"}, "")
	}
	t, at := window(time.Now())
	var cpu, mem [nodes]int64
	c := b.c
	c.mu.Lock()
	for i := range c.pods {
		p := &c.pods[i]
		if !reportsMetrics(p) || int(p.node) >= nodes {
			continue
		}
		pc, pm := podUsage(p, t)
		cpu[p.node] += pc
		mem[p.node] += pm
	}
	c.mu.Unlock()
	out := make([]usage.NodeSample, nodes)
	for n := range out {
		out[n] = usage.NodeSample{Name: "node-" + strconv.Itoa(n), At: at.UnixMilli(),
			CPU: uint32(cpu[n] + 350), MemKi: uint32((mem[n] + 1536<<20) >> 10)}
	}
	return out, nil
}
