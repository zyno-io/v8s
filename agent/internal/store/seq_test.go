package store

import (
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ingest"
)

// Seq is what lets the capacity scan skip a store nothing happened to.
func TestSeqIncreasesWithEveryMutation(t *testing.T) {
	s := New()
	last := s.Seq()
	step := func(what string, fn func()) {
		t.Helper()
		fn()
		if now := s.Seq(); now <= last {
			t.Fatalf("%s did not bump Seq (%d -> %d)", what, last, now)
		} else {
			last = now
		}
	}
	step("Add", func() { _ = s.Add(row("1", "ns", "a", 0)) })
	step("Update", func() { _ = s.Update(row("1", "ns", "a", 3)) })
	step("Provisional", func() { s.Provisional([]*ingest.Row{row("9", "ns", "p", 0)}) })
	step("Apply", func() { s.Apply([]*ingest.Row{row("2", "ns", "b", 0)}, nil) })
	step("Replace", func() { _ = s.Replace([]interface{}{row("2", "ns", "b", 0)}, "2") })
	step("Delete", func() { _ = s.Delete(row("2", "ns", "b", 0)) })
	step("Reset", func() { s.Reset() })
	// A synced store ignores Provisional: nothing changed, nothing to rescan.
	_ = s.Replace(nil, "3")
	last = s.Seq()
	s.Provisional([]*ingest.Row{row("8", "ns", "q", 0)})
	if s.Seq() != last {
		t.Fatal("a Provisional on a synced store changed nothing and must not bump Seq")
	}
}
