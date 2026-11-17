package view

import (
	"fmt"
	"math/rand/v2"
	"syscall"
	"testing"
	"unique"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func cpuNs() int64 {
	var r syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &r)
	return r.Utime.Nano() + r.Stime.Nano()
}

func synthEntries(n int) []entry {
	rng := rand.New(rand.NewPCG(1, 1))
	es := make([]entry, n)
	for i := range es {
		app := i % 400
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("svc-%d-%010x-%05x", app, rng.Uint64()&0xffffffffff, rng.Uint32()&0xfffff),
			Namespace: fmt.Sprint("team-", app%37), UID: types.UID(fmt.Sprint("u", i))}}
		p.Spec.Containers = []corev1.Container{{Name: "c"}}
		p.Status.Phase = corev1.PodRunning
		if i%200 == 0 {
			p.Status.Phase = corev1.PodPending
		}
		es[i] = mkEntry(ingest.Project(p), 0, uint32(i), 1)
	}
	return es
}

// BenchmarkSort50k measures sorting only, reporting wall time and process CPU time (CPU time is
// the trustworthy number on a loaded machine).
func BenchmarkSort50k(b *testing.B) {
	_ = unique.Make[string]
	e := NewEngine()
	e.names = []string{"c"}
	n, _ := Normalize(protocolQuery())
	c := e.buildCmp(n)
	src := synthEntries(50000)
	work := make([]entry, len(src))
	key := e.buildKey(n)
	for _, par := range []int{1, 0, 2} {
		b.Run(fmt.Sprint("mode=", par), func(b *testing.B) {
			var cpu int64
			for i := 0; i < b.N; i++ {
				copy(work, src)
				rand.Shuffle(len(work), func(i, j int) { work[i], work[j] = work[j], work[i] })
				c0 := cpuNs()
				if par == 1 {
					sortSerial(work, c)
				} else if par == 2 {
					sortKeyed(work, c, key)
				} else {
					sortEntries(work, c)
				}
				cpu += cpuNs() - c0
			}
			b.ReportMetric(float64(cpu)/float64(b.N)/1e6, "cpu-ms/sort")
		})
	}
}

func protocolQuery() protocol.ViewQuery { return protocol.ViewQuery{} }
