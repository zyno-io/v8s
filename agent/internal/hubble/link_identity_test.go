package hubble

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A link that fails after the cluster's identity changed is still invalidated (under the key it was built for).
func TestLinkInvalidateAfterIdentityChange(t *testing.T) {
	var mu sync.Mutex
	eff := "c1"
	var closed atomic.Int32
	s := New(Config{LinkIdle: time.Hour,
		Effective: func(string) string { mu.Lock(); defer mu.Unlock(); return eff },
		Connect: func(context.Context, string) (*Link, error) {
			return &Link{Close: func() { closed.Add(1) }, Dead: func() bool { return false }}, nil
		}})
	l, rel, err := s.acquire(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	eff = "c1-admin"
	mu.Unlock()
	s.invalidate("c1", l)
	rel()
	if closed.Load() != 1 {
		t.Fatalf("closed=%d, want 1", closed.Load())
	}
	mu.Lock()
	eff = "c1"
	mu.Unlock()
	l2, rel2, err := s.acquire(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	defer rel2()
	if l2 == l {
		t.Fatal("re-acquire reused the invalidated link")
	}
}

// An identity change while a link is being built never files that link under the old identity's key.
func TestLinkIdentityChangeDuringConnect(t *testing.T) {
	var mu sync.Mutex
	eff := "c1"
	built := map[*Link]string{}
	var calls atomic.Int32
	s := New(Config{LinkIdle: time.Hour,
		Effective: func(string) string { mu.Lock(); defer mu.Unlock(); return eff },
		Connect: func(context.Context, string) (*Link, error) {
			mu.Lock()
			defer mu.Unlock()
			if calls.Add(1) == 1 {
				eff = "c1-admin" // elevated while the first connect runs
			}
			l := &Link{Close: func() {}, Dead: func() bool { return false }}
			built[l] = eff
			return l, nil
		}})
	l, rel, err := s.acquire(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if built[l] != "c1-admin" {
		t.Fatalf("got a link built as %q", built[l])
	}
	mu.Lock()
	eff = "c1" // reverted
	mu.Unlock()
	l2, rel2, err := s.acquire(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	defer rel2()
	if l2 == l || built[l2] != "c1" {
		t.Fatalf("read-identity acquire got the link built as %q", built[l2])
	}
}
