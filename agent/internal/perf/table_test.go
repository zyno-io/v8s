package perf

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

// tableJSON renders n objects as a server-side Table (what a Kubescape/Trivy-style CRD returns with
// includeObject=Metadata): printer columns plus PartialObjectMetadata, never the object bodies.
func tableJSON(n int, pages int) [][]byte {
	var out [][]byte
	per := n / pages
	for p := 0; p < pages; p++ {
		var b bytes.Buffer
		b.WriteString(`{"kind":"Table","apiVersion":"meta.k8s.io/v1","metadata":{"resourceVersion":"100"`)
		if p < pages-1 {
			fmt.Fprintf(&b, `,"continue":"p%d"`, p+1)
		}
		b.WriteString(`},"columnDefinitions":[{"name":"Name","type":"string"},{"name":"Critical","type":"integer"},{"name":"High","type":"integer"},{"name":"Medium","type":"integer"},{"name":"Ready","type":"string"},{"name":"Age","type":"date"}],"rows":[`)
		for i := 0; i < per; i++ {
			id := p*per + i
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"cells":["vulnerabilityreport-%d",%d,%d,%d,"True","%dd"],"object":{"kind":"PartialObjectMetadata","apiVersion":"meta.k8s.io/v1","metadata":{"name":"vulnerabilityreport-%d","namespace":"team-%d","uid":"00000000-0000-4000-8000-%012d","resourceVersion":"%d","creationTimestamp":"2025-01-02T03:04:05Z"}}}`,
				id, id%3, id%17, id%40, id%90, id, id%37, id, 1000+id)
		}
		b.WriteString(`]}`)
		out = append(out, b.Bytes())
	}
	return out
}

type pagedTable struct{ pages [][]byte }

func (a *pagedTable) List(ctx context.Context, o metav1.ListOptions) (k8sruntime.Object, error) {
	i := 0
	if o.Continue != "" {
		fmt.Sscanf(o.Continue, "p%d", &i)
	}
	return ingest.DecodeTable(a.pages[i])
}
func (a *pagedTable) Watch(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
	return watch.NewFake(), nil
}

func ingestTable(tb testing.TB, pages [][]byte, gvr string) *store.Store {
	st := store.New()
	done := make(chan struct{})
	st.OnReplace = func(int) { close(done) }
	lw := &ingest.ListerWatcher{API: &pagedTable{pages}, Generic: &ingest.GenericSpec{GVR: gvr, Kind: "VulnerabilityReport", Namespaced: true}}
	r := cache.NewReflectorWithOptions(lw, &ingest.Row{}, st, cache.ReflectorOptions{Name: "table-bench"})
	r.WatchListPageSize = 500
	ctx, cancel := context.WithCancel(context.Background())
	go r.RunWithContext(ctx)
	<-done
	cancel()
	return st
}

var _ = schema.GroupVersionResource{}

// BenchmarkTableIngest5k: JSON Table pages -> decode -> generic projection -> reflector -> store, 5k objects.
func BenchmarkTableIngest5k(b *testing.B) {
	pages := tableJSON(5000, 10)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st := ingestTable(b, pages, "aquasecurity.github.io/v1alpha1/vulnerabilityreports")
		if st.Len() != 5000 {
			b.Fatalf("store has %d rows", st.Len())
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e6, "ms/5k-objects")
}

// TestTableRetainedHeap reports (and bounds) the live heap after ingesting 5k generic objects.
func TestTableRetainedHeap(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("memory measurement skipped with -short / -race")
	}
	base := settledHeap()
	pages := tableJSON(5000, 10)
	st := ingestTable(t, pages, "aquasecurity.github.io/v1alpha1/vulnerabilityreports")
	pages = nil // only the projected rows may stay alive
	after := settledHeap()
	runtime.KeepAlive(st)
	t.Logf("base=%d after=%d len=%d", base, after, st.Len())
	got := uint64(0)
	if after > base {
		got = after - base
	}
	t.Logf("retained heap, 5k Table-mode objects (4 cells each): %.2f MB (%.0f bytes/object)", float64(got)/1048576, float64(got)/5000)
	if got > 10<<20 {
		t.Fatalf("retained heap %.1f MB exceeds 10 MB budget for 5k generic objects", float64(got)/1048576)
	}
}

// settledHeap forces a full collection and sweep before reading the heap.
func settledHeap() uint64 {
	debug.FreeOSMemory()
	return liveHeap()
}
