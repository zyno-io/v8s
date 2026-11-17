package wire

import (
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// BenchmarkPatchBuild300 builds and encodes patches for a 300-row window of a 50k-row view.
//   - first: a brand-new subscription (ids + 300 rows)
//   - churn: steady state, 3 rows of the window changed
func BenchmarkPatchBuild300(b *testing.B) {
	eng := view.NewEngine()
	eng.IdleTTL = 0
	cl := fake.NewCluster("bench", 50000, 0, 1)
	defer cl.Stop()
	st := fillStore(b, cl)
	eng.Attach("bench", st)
	q := protocol.ViewQuery{}
	w := protocol.Window{Limit: 300}
	hold, _ := eng.Subscribe(q, 1, w) // keeps the materialized view alive so only the patch is timed
	defer hold.Close()

	b.Run("first", func(b *testing.B) {
		c := &conn{}
		c.initCodec()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			sub, _ := eng.Subscribe(q, 1, w)
			p := sub.Prepare()
			for first := true; first || !p.Done(); first = false {
				nr, nd, ids := c.buildPatch(p)
				p.Commit(nr, nd, ids)
			}
			sub.Close()
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e3, "us/patch")
		b.ReportMetric(float64(c.buf.Len()), "frame-bytes")
	})
	b.Run("churn", func(b *testing.B) {
		c := &conn{}
		c.initCodec()
		sub, _ := eng.Subscribe(q, 1, w)
		defer sub.Close()
		p := sub.Prepare()
		nr, nd, ids := c.buildPatch(p)
		p.Commit(nr, nd, ids)
		items := st.Snapshot(nil, nil)
		rows := make([]int, 0, 3)
		_ = rows
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			// touch 3 visible rows: bump restarts by re-adding projected copies
			bumpVisible(b, eng, st, items, i)
			eng.Pump()
			b.StartTimer()
			p := sub.Prepare()
			if p == nil {
				b.Fatalf("expected a patch at i=%d dirty=%v", i, sub.Dirty())
			}
			nr, nd, ids := c.buildPatch(p)
			p.Commit(nr, nd, ids)
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e3, "us/patch")
		b.ReportMetric(float64(c.buf.Len()), "frame-bytes")
	})
}

// bumpVisible changes the restart count of the first three rows in the default order.
var bumpSeq int

func bumpVisible(b *testing.B, eng *view.Engine, st *store.Store, _ []store.Item, _ int) {
	bumpSeq++
	sub, _ := eng.Subscribe(protocol.ViewQuery{}, 1, protocol.Window{Limit: 3})
	p := sub.Prepare()
	sub.Close()
	for _, r := range p.Rows {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: r.Row.Name(), Namespace: r.Row.NS().Value(), UID: types.UID(r.Row.UID()), ResourceVersion: "2"}}
		pod.Spec.Containers = []corev1.Container{{Name: "c"}}
		pod.Status.Phase = corev1.PodPending
		pod.Status.Reason = "Evicted"
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "c", RestartCount: int32(bumpSeq)}}
		if err := st.Update(ingest.Project(pod)); err != nil {
			b.Fatal(err)
		}
	}
}
