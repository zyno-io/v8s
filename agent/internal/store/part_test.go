package store

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func evRow(uid string, lastSeen int64) *ingest.Row {
	e := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "e-" + uid, Namespace: "ns", UID: types.UID(uid), ResourceVersion: "1"},
		Type: "Normal", Reason: "R", LastTimestamp: metav1.Time{Time: time.UnixMilli(lastSeen)}}
	return ingest.Lookup(ingest.EventsGVR).ProjectObj(e)
}

func uids(s *Store) map[string]bool {
	out := map[string]bool{}
	for _, k := range s.ListKeys() {
		out[k] = true
	}
	return out
}

func newPart(s *Store, max int) *Part { return NewPart(s, max, ingest.EventLastSeen) }

func TestPartsShareAStoreWithoutTouchingEachOther(t *testing.T) {
	s := New()
	a, b := newPart(s, 10), newPart(s, 10)
	_ = a.Replace([]interface{}{evRow("a1", 1), evRow("a2", 2)}, "1")
	_ = b.Replace([]interface{}{evRow("b1", 1)}, "1")
	_ = a.Replace([]interface{}{evRow("a2", 2), evRow("a3", 3)}, "2") // a1 goes, b1 stays
	if got := uids(s); len(got) != 3 || !got["a2"] || !got["a3"] || !got["b1"] {
		t.Fatalf("rows after a.Replace: %v", got)
	}
	_ = a.Delete(evRow("b1", 1)) // not owned: ignored
	if !uids(s)["b1"] {
		t.Fatal("a deleted b's row")
	}
	_ = b.Delete(evRow("b1", 1))
	if uids(s)["b1"] {
		t.Fatal("owner could not delete its row")
	}
}

func TestPartCapKeepsNewest(t *testing.T) {
	s := New()
	p := newPart(s, 3)
	var list []interface{}
	for i := 1; i <= 6; i++ {
		list = append(list, evRow(fmt.Sprint("u", i), int64(i*10)))
	}
	_ = p.Replace(list, "1")
	if got := uids(s); len(got) != 3 || !got["u4"] || !got["u5"] || !got["u6"] {
		t.Fatalf("replace kept %v", got)
	}
	_ = p.Update(evRow("old", 5)) // older than everything kept: dropped
	if uids(s)["old"] || len(uids(s)) != 3 {
		t.Fatalf("an older event was kept: %v", uids(s))
	}
	_ = p.Update(evRow("new", 70)) // evicts the oldest (u4)
	if got := uids(s); len(got) != 3 || got["u4"] || !got["new"] {
		t.Fatalf("a newer event did not evict the oldest: %v", got)
	}
	_ = p.Update(evRow("u6", 65)) // an owned uid just updates
	if len(uids(s)) != 3 {
		t.Fatalf("update of an owned uid changed the size: %v", uids(s))
	}
}

func TestPartCloseRemovesRowsAndIgnoresLaterWrites(t *testing.T) {
	s := New()
	a, b := newPart(s, 10), newPart(s, 10)
	_ = a.Replace([]interface{}{evRow("a1", 1)}, "1")
	_ = b.Replace([]interface{}{evRow("b1", 1)}, "1")
	a.Close()
	if got := uids(s); len(got) != 1 || !got["b1"] {
		t.Fatalf("after close: %v", got)
	}
	_ = a.Update(evRow("a9", 9))
	_ = a.Replace([]interface{}{evRow("a8", 8)}, "2")
	_ = a.Delete(evRow("b1", 1))
	a.Close()
	if got := uids(s); len(got) != 1 || !got["b1"] {
		t.Fatalf("late writes after close changed the store: %v", got)
	}
}

func TestPartOnSyncedOnce(t *testing.T) {
	s := New()
	p := newPart(s, 10)
	var n atomic.Int32
	p.OnSynced = func() { n.Add(1) }
	_ = p.Update(evRow("a", 1))
	if n.Load() != 0 {
		t.Fatal("synced before the first Replace")
	}
	_ = p.Replace(nil, "1")
	_ = p.Replace(nil, "2")
	if n.Load() != 1 {
		t.Fatalf("OnSynced called %d times", n.Load())
	}
}

func TestApplyNotifiesOnce(t *testing.T) {
	s := New()
	var n atomic.Int32
	s.SetNotify(func() { n.Add(1) })
	s.Apply([]*ingest.Row{evRow("a", 1), evRow("b", 2), evRow("c", 3)}, nil)
	if n.Load() != 1 {
		t.Fatalf("%d notifications for one Apply", n.Load())
	}
	s.Apply([]*ingest.Row{evRow("d", 4)}, []string{"a", "b", "missing"})
	if n.Load() != 2 || len(uids(s)) != 2 {
		t.Fatalf("notifications %d rows %v", n.Load(), uids(s))
	}
}

func BenchmarkPartUpdateAtCap(b *testing.B) {
	s := New()
	p := newPart(s, 200)
	var list []interface{}
	for i := 0; i < 200; i++ {
		list = append(list, evRow(fmt.Sprint("u", i), int64(1000+i)))
	}
	_ = p.Replace(list, "1")
	rows := make([]*ingest.Row, b.N)
	for i := range rows {
		rows[i] = evRow(fmt.Sprint("n", i), int64(2000+i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Update(rows[i])
	}
}
