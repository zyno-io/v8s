package view

import (
	"fmt"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// podsOnNodes projects n pods spread over `nodes` nodes ("node-<i>").
func podsOnNodes(n, nodes int) []*ingest.Row {
	rows := make([]*ingest.Row, n)
	for i := range rows {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprint("pod-", i), Namespace: fmt.Sprint("team-", i%37), UID: types.UID(fmt.Sprint("u", i))}}
		p.Spec.Containers = []corev1.Container{{Name: "c"}}
		p.Spec.NodeName = fmt.Sprint("node-", i%nodes)
		rows[i] = ingest.Project(p)
	}
	return rows
}

func nodeQuery(picks int) protocol.ViewQuery {
	vals := make([]string, picks)
	for i := range vals {
		vals[i] = fmt.Sprint("node-", i*3)
	}
	return protocol.ViewQuery{GVR: PodsGVR, Filters: []protocol.Filter{{Col: "node", Op: "in", Value: vals}}}
}

// TestMatchNodeIn: the node filter matches exactly the pods on the picked nodes, for one pick and for many (the set path).
func TestMatchNodeIn(t *testing.T) {
	e := NewEngine()
	e.names = []string{"c"}
	rows := podsOnNodes(3000, 500)
	for _, picks := range []int{1, 2, 15, 16, 40} {
		n, err := Normalize(nodeQuery(picks))
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{}
		for i := 0; i < picks; i++ {
			want[fmt.Sprint("node-", i*3)] = true
		}
		match := e.buildMatch(n)
		got := 0
		for _, r := range rows {
			m := match(r, 0)
			if m != want[r.Node().Value()] {
				t.Fatalf("picks=%d: %s on %s matched=%v", picks, r.Name(), r.Node().Value(), m)
			}
			if m {
				got++
			}
		}
		if got != picks*6 {
			t.Fatalf("picks=%d: %d matches, want %d", picks, got, picks*6)
		}
	}
}

// BenchmarkMatchNodeIn50k: the membership scan over 50k pods with a node filter of 1..500 picked nodes.
func BenchmarkMatchNodeIn50k(b *testing.B) {
	e := NewEngine()
	e.names = []string{"c"}
	rows := podsOnNodes(50000, 1000)
	for _, picks := range []int{1, 10, 15, 16, 100, 500} {
		n, err := Normalize(nodeQuery(picks))
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprint("picks=", picks), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				match := e.buildMatch(n)
				k := 0
				for _, r := range rows {
					if match(r, 0) {
						k++
					}
				}
				_ = k
			}
		})
	}
}
