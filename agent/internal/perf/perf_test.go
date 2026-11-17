// Package perf holds the budget test (docs/performance.md §11) and the benchmarks (§13).
package perf

import (
	"context"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func listPods(tb testing.TB, c *fake.Cluster) []corev1.Pod {
	tb.Helper()
	pl, err := c.List(context.Background(), metav1.ListOptions{})
	if err != nil {
		tb.Fatal(err)
	}
	return pl.Items
}

func project(pods []corev1.Pod) []*ingest.PodRow {
	rows := make([]*ingest.PodRow, len(pods))
	for i := range pods {
		rows[i] = ingest.Project(&pods[i])
	}
	return rows
}

// engineWithRows builds an engine with `clusters` stores holding n rows in total.
func engineWithRows(tb testing.TB, n, clusters int) (*view.Engine, []*store.Store) {
	tb.Helper()
	e := view.NewEngine()
	e.IdleTTL = 0
	var sts []*store.Store
	for i := 0; i < clusters; i++ {
		c := fake.NewCluster(fake.Name(i), n/clusters, 0, uint64(i)+1)
		rows := project(listPods(tb, c))
		c.Stop()
		st := store.New()
		items := make([]interface{}, len(rows))
		for j, r := range rows {
			items[j] = r
		}
		if err := st.Replace(items, "1"); err != nil {
			tb.Fatal(err)
		}
		e.Attach(fake.Name(i), st)
		sts = append(sts, st)
	}
	return e, sts
}

func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// retainedHeap10k measures the live heap added by a fake cluster of 10k pods running through the
// whole path (fake API -> projecting ListerWatcher -> reflector -> store -> view engine), after GC.
func retainedHeap10k(tb testing.TB) (bytes uint64) {
	tb.Helper()
	base := liveHeap()
	ctx, cancel := context.WithCancel(context.Background())
	eng := view.NewEngine()
	src := fake.NewSource(fake.Config{Clusters: 1, Pods: 10000})
	mgr := cluster.NewManager(ctx, src, eng, nil)
	if _, err := mgr.Select([]string{"prod-us"}); err != nil {
		tb.Fatal(err)
	}
	waitReady(tb, mgr)
	sub, err := eng.Subscribe(protocol.ViewQuery{}, 1, protocol.Window{Limit: 300})
	if err != nil {
		tb.Fatal(err)
	}
	if p := sub.Prepare(); p == nil || p.Total != 10000 {
		tb.Fatalf("view not populated: %+v", p)
	}
	after := liveHeap()
	runtime.KeepAlive(sub)
	runtime.KeepAlive(mgr)
	runtime.KeepAlive(eng)
	cancel()
	mgr.Close()
	if after < base {
		return 0
	}
	return after - base
}

func waitReady(tb testing.TB, mgr *cluster.Manager) {
	tb.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		st := mgr.States().States
		if len(st) > 0 && st[0].Phase == protocol.PhaseReady {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	tb.Fatal("cluster never became ready")
}

func median(ds []time.Duration) time.Duration {
	s := slices.Clone(ds)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

// TestBudgets enforces the §11 numbers that are cheap to assert in a normal `go test` run.
func TestBudgets(t *testing.T) {
	if testing.Short() {
		t.Skip("budget test skipped with -short")
	}
	if raceEnabled {
		t.Skip("budget test skipped under the race detector (5-10x slowdown makes timings meaningless)")
	}
	t.Run("retained heap 10k pods", func(t *testing.T) {
		got := retainedHeap10k(t)
		t.Logf("retained heap, fake cluster of 10k pods, whole path: %.1f MB", float64(got)/1048576)
		if limit := uint64(50 << 20); got > limit {
			t.Fatalf("retained heap %.1f MB exceeds budget %d MB", float64(got)/1048576, limit>>20)
		}
	})
	t.Run("view rebuild 50k", func(t *testing.T) {
		for _, clusters := range []int{1, 3} {
			e, _ := engineWithRows(t, 50000, clusters)
			q := protocol.ViewQuery{}
			var ds []time.Duration
			for i := 0; i < 15; i++ {
				start := time.Now()
				s, err := e.Subscribe(q, 1, protocol.Window{Limit: 300})
				if err != nil {
					t.Fatal(err)
				}
				ds = append(ds, time.Since(start))
				s.Close() // IdleTTL=0 drops the view, so the next Subscribe rebuilds
			}
			// The budget is asserted on the fastest run: wall time on a shared or loaded machine
			// only ever inflates, so the minimum is the measurement closest to the code's real cost.
			best := slices.Min(ds)
			t.Logf("view rebuild at 50k rows over %d cluster(s): best %v, median %v", clusters, best, median(ds))
			if best > 20*time.Millisecond {
				t.Fatalf("view rebuild at 50k took %v (best of %d), budget 20ms", best, len(ds))
			}
		}
	})
}

func BenchmarkProject10k(b *testing.B) {
	c := fake.NewCluster("p", 10000, 0, 1)
	defer c.Stop()
	pods := listPods(b, c)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range pods {
			_ = ingest.Project(&pods[j])
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e6, "ms/10k-pods")
}

// BenchmarkReflectorIngest10k: fake API -> ListerWatcher (projection) -> client-go reflector -> store.
func BenchmarkReflectorIngest10k(b *testing.B) {
	c := fake.NewCluster("r", 10000, 0, 1)
	defer c.Stop()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st := store.New()
		done := make(chan struct{})
		st.OnReplace = func(int) { close(done) }
		r := cache.NewReflectorWithOptions(&ingest.ListerWatcher{API: ingest.PodAdapter{PodAPI: c}}, &ingest.PodRow{}, st, cache.ReflectorOptions{Name: "bench"})
		ctx, cancel := context.WithCancel(context.Background())
		go r.RunWithContext(ctx)
		<-done
		cancel()
		if st.Len() != 10000 {
			b.Fatalf("store has %d rows", st.Len())
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e6, "ms/10k-pods")
}

func BenchmarkViewBuild50k(b *testing.B) {
	for _, clusters := range []int{1, 3} {
		b.Run(fmt.Sprintf("clusters=%d", clusters), func(b *testing.B) {
			e, _ := engineWithRows(b, 50000, clusters)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s, _ := e.Subscribe(protocol.ViewQuery{}, 1, protocol.Window{Limit: 300})
				s.Close()
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e6, "ms/build")
		})
	}
}

// BenchmarkIncrementalUpdate50k: one pod changes (restarts and status) in a 50k-row view, then the
// engine applies the feed (binary-search move) and the subscription is marked dirty.
func BenchmarkIncrementalUpdate50k(b *testing.B) {
	e, sts := engineWithRows(b, 50000, 1)
	st := sts[0]
	s, _ := e.Subscribe(protocol.ViewQuery{}, 1, protocol.Window{Limit: 300})
	defer s.Close()
	items := st.Snapshot(nil, nil)
	rng := rand.New(rand.NewPCG(3, 4))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		it := items[rng.IntN(len(items))]
		old := it.Row
		p := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: old.Name(), Namespace: old.NS().Value(), UID: "x", ResourceVersion: "2"}}
		p.UID = types_UID(old.UID())
		p.Spec.Containers = []corev1.Container{{Name: "c"}}
		p.Status.Phase = corev1.PodRunning
		if i%3 == 0 {
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "c", RestartCount: int32(i), State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}
		} else {
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "c", Ready: true, RestartCount: int32(i), State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
		}
		row := ingest.Project(&p)
		_ = st.Update(row)
		e.Pump()
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e3, "us/event")
}

// BenchmarkHeapPer10kPods reports retained heap of the whole fake-cluster path.
func BenchmarkHeapPer10kPods(b *testing.B) {
	for i := 0; i < b.N; i++ {
		got := retainedHeap10k(b)
		b.ReportMetric(float64(got)/1048576, "MB/10k-pods")
	}
}
