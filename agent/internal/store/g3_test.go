package store

import (
	"sync"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ingest"
)

// Re-upserting a row that is already published (progressive rows, then the reflector's Replace with the same
// pointers) must not write to it while readers hold it: run with -race.
func TestReupsertOfPublishedRowDoesNotWrite(t *testing.T) {
	s := New()
	r := ingest.NewRow("u1", "a", "1", "ns", 0, false, 0)
	s.Provisional([]*ingest.Row{r})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = r.Ver()
		}
	}()
	for i := 0; i < 1000; i++ {
		_ = s.Replace([]interface{}{r}, "2")
	}
	wg.Wait()
}

func TestTapSeesAddUpdateDeleteButNotUnchanged(t *testing.T) {
	s := New()
	var adds, upd, del int
	s.SetTap(func(old, new *ingest.Row) {
		switch {
		case old == nil:
			adds++
		case new == nil:
			del++
		default:
			upd++
		}
	})
	a := ingest.NewRow("u1", "a", "1", "ns", 0, false, 1)
	a.PutS("x")
	_ = s.Update(a)
	same := ingest.NewRow("u1", "a", "2", "ns", 0, false, 1)
	same.PutS("x")
	_ = s.Update(same) // equal cells and facts: no change
	b := ingest.NewRow("u1", "a", "3", "ns", 0, false, 1)
	b.PutS("y")
	_ = s.Update(b)
	_ = s.Delete(b)
	if adds != 1 || upd != 1 || del != 1 {
		t.Fatalf("tap saw add=%d update=%d delete=%d", adds, upd, del)
	}
}
