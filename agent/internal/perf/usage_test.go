package perf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/internal/usage"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

// podMetricsFor builds the PodMetricsList metrics-server would return for pods: its labels, a timestamp, a window
// and one usage entry per container.
func podMetricsFor(pods []corev1.Pod) *metricsv1beta1.PodMetricsList {
	ts := metav1.NewTime(time.Now())
	l := &metricsv1beta1.PodMetricsList{Items: make([]metricsv1beta1.PodMetrics, len(pods))}
	for i := range pods {
		p := &pods[i]
		pm := &l.Items[i]
		pm.Name, pm.Namespace, pm.Labels = p.Name, p.Namespace, p.Labels
		pm.Timestamp, pm.Window = ts, metav1.Duration{Duration: 15 * time.Second}
		for j := range p.Spec.Containers {
			pm.Containers = append(pm.Containers, metricsv1beta1.ContainerMetrics{Name: p.Spec.Containers[j].Name,
				Usage: corev1.ResourceList{
					corev1.ResourceCPU:    *resource.NewScaledQuantity(int64(1_000_000+(i*7919+j*104729)%400_000_000), resource.Nano),
					corev1.ResourceMemory: *resource.NewQuantity(int64(8<<20+(i*104729+j*7919)%(500<<20)), resource.BinarySI)}})
		}
	}
	return l
}

func BenchmarkDecodePodMetrics10k(b *testing.B) {
	c := fake.NewCluster("m", 10000, 0, 1)
	defer c.Stop()
	list := podMetricsFor(listPods(b, c))
	js, err := json.Marshal(list)
	if err != nil {
		b.Fatal(err)
	}
	pb, err := list.Marshal()
	if err != nil {
		b.Fatal(err)
	}
	b.Run("json", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(js)))
		for i := 0; i < b.N; i++ {
			var l metricsv1beta1.PodMetricsList
			if err := json.Unmarshal(js, &l); err != nil {
				b.Fatal(err)
			}
			_ = usage.FromPodMetrics(&l)
		}
	})
	b.Run("proto", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(pb)))
		for i := 0; i < b.N; i++ {
			var l metricsv1beta1.PodMetricsList
			if err := l.Unmarshal(pb); err != nil {
				b.Fatal(err)
			}
			_ = usage.FromPodMetrics(&l)
		}
	})
}

// usageFixture is an engine holding n pods of one cluster, the store, and the encoded protobuf list.
func usageFixture(tb testing.TB, n int) (*view.Engine, *store.Store, []byte, map[string]usage.NodeAlloc) {
	tb.Helper()
	e, sts := engineWithRows(tb, n, 1)
	c := fake.NewCluster(fake.Name(0), n, 0, 1) // the same seed engineWithRows used
	defer c.Stop()
	pb, err := podMetricsFor(listPods(tb, c)).Marshal()
	if err != nil {
		tb.Fatal(err)
	}
	nodes := map[string]usage.NodeAlloc{}
	for i := 0; i < 150; i++ {
		nodes[fmt.Sprintf("node-%d", i)] = usage.NodeAlloc{CPU: 15890, MemMi: 64512}
	}
	return e, sts[0], pb, nodes
}

// BenchmarkUsagePoll10k is one whole poll of a 10k-pod cluster as the agent does it: decode the protobuf list, reduce to
// samples, snapshot the store, join, and publish to an engine holding one default view and one view sorted by %CPU/L.
// Budget: 40 ms (docs/performance.md §13).
func BenchmarkUsagePoll10k(b *testing.B) {
	e, st, pb, nodes := usageFixture(b, 10000)
	plain, _ := e.Subscribe(protocol.ViewQuery{}, 1, protocol.Window{Limit: 2060})
	plain.SetUsage(true)
	sorted, _ := e.Subscribe(protocol.ViewQuery{Sort: []protocol.Sort{{Col: "cpuPctLim", Desc: true}}}, 1, protocol.Window{Limit: 2060})
	sorted.SetUsage(true)
	defer plain.Close()
	defer sorted.Close()
	var buf []store.Item
	var prev *usage.Snap
	seq := usage.VerBase
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var l metricsv1beta1.PodMetricsList
		if err := l.Unmarshal(pb); err != nil {
			b.Fatal(err)
		}
		samples := usage.FromPodMetrics(&l)
		buf = st.Snapshot(buf, nil)
		seq++
		prev = usage.BuildPods(st, buf, nil, samples, nodes, prev, seq)
		e.SetUsage(fake.Name(0), ingest.PodsGVR, prev)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e6, "ms/poll")
}

// BenchmarkUsageResort50k is the re-sort a poll costs a 50k-row view sorted by usage. Budget: 10 ms.
func BenchmarkUsageResort50k(b *testing.B) {
	e, st, _, nodes := usageFixture(b, 50000)
	sorted, _ := e.Subscribe(protocol.ViewQuery{Sort: []protocol.Sort{{Col: "cpuPctLim", Desc: true}}}, 1, protocol.Window{Limit: 300})
	defer sorted.Close()
	items := st.Snapshot(nil, nil)
	mk := func(seed, seq uint32) *usage.Snap {
		ss := make([]usage.PodSample, 0, len(items))
		for i, it := range items {
			ss = append(ss, usage.PodSample{NS: it.Row.NS().Value(), Name: it.Row.Name(), At: time.Now().UnixMilli() + 1000,
				CPU: uint32(i*7919+int(seed)*104729) % 900, MemKi: uint32(i*104729+int(seed)*7919) % (500 << 10)})
		}
		return usage.BuildPods(st, items, nil, ss, nodes, nil, seq)
	}
	snaps := [2]*usage.Snap{mk(1, usage.VerBase+1), mk(2, usage.VerBase+2)}
	e.SetUsage(fake.Name(0), ingest.PodsGVR, snaps[0])
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.SetUsage(fake.Name(0), ingest.PodsGVR, snaps[(i+1)%2])
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e6, "ms/resort")
}

type dictStub struct{}

func (dictStub) ID(ingest.Str) uint32 { return 1 }

// BenchmarkUsagePrepare2000: a poll that changes every value of a 10k-pod view re-sends all 2,060 window rows (the worst
// case): Prepare plus encoding them. Snapshots are built outside the timer.
func BenchmarkUsagePrepare2000(b *testing.B) {
	e, st, pb, nodes := usageFixture(b, 10000)
	sub, _ := e.Subscribe(protocol.ViewQuery{}, 1, protocol.Window{Limit: 2060})
	defer sub.Close()
	sub.SetUsage(true)
	var l metricsv1beta1.PodMetricsList
	if err := l.Unmarshal(pb); err != nil {
		b.Fatal(err)
	}
	a := usage.FromPodMetrics(&l)
	c := append([]usage.PodSample(nil), a...)
	for i := range c {
		c[i].CPU++
	}
	items := st.Snapshot(nil, nil)
	snaps := [2]*usage.Snap{usage.BuildPods(st, items, nil, a, nodes, nil, usage.VerBase+1), usage.BuildPods(st, items, nil, c, nodes, nil, usage.VerBase+2)}
	e.SetUsage(fake.Name(0), ingest.PodsGVR, snaps[0])
	mod := ingest.Lookup(ingest.PodsGVR)
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	enc.UseCompactInts(true)
	first := sub.Prepare()
	first.Commit(len(first.Rows), len(first.Removed), first.SendIDs)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.SetUsage(fake.Name(0), ingest.PodsGVR, snaps[(i+1)%2])
		p := sub.Prepare()
		for j := range p.Rows {
			buf.Reset()
			_ = view.EncodeRow(enc, dictStub{}, mod, &p.Rows[j])
		}
		p.Commit(len(p.Rows), len(p.Removed), p.SendIDs)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e6, "ms/patch")
}
