package hosted

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestAcquireOnEndedSessionDoesNotDeadlock(t *testing.T) {
	e := newEnv(t, nil)
	id := Identity{Sub: "alice-sub", Username: "alice@example.com", Groups: []string{"devs"}}
	live := e.srv.store.Create(id)
	_, release, err := e.srv.pool.Acquire(live) // creates the identity's engine
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ended := e.srv.store.Create(id)
	e.srv.store.Delete(ended.ID()) // ended after the caller looked it up
	done := make(chan error, 1)
	go func() { _, _, err := e.srv.pool.Acquire(ended); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, errSessionEnded) {
			t.Fatalf("want errSessionEnded, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Acquire deadlocked on an ended session")
	}
	// The pool lock is still usable by everyone.
	got := make(chan struct{})
	go func() { e.srv.pool.Get(live); e.srv.pool.Active(); close(got) }()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("pool lock is stuck")
	}
}

func TestRetiredEngineReleasesSessionHooks(t *testing.T) {
	e := newEnv(t, nil)
	sess := e.srv.store.Create(Identity{Sub: "alice-sub", Username: "alice@example.com", Groups: []string{"devs"}})
	ue, release, _ := e.srv.pool.Acquire(sess)
	release()
	e.clock.Advance(31 * time.Second)
	if e.srv.pool.Retained() != 0 {
		t.Fatal("engine not retired")
	}
	sess.mu.Lock()
	n := len(sess.closers)
	sess.mu.Unlock()
	if n != 0 || len(ue.sessions) != 0 {
		t.Fatalf("retired engine still reachable from the session (%d hooks, %d sessions)", n, len(ue.sessions))
	}
}

func TestAnonymousLoginFloodDoesNotLockOutRealLogins(t *testing.T) {
	e := newEnv(t, nil)
	for i := 0; i < maxFlows+500; i++ {
		e.srv.putFlow("state-"+string(rune('a'+i%26))+time.Duration(i).String(), &flow{bind: "bind-" + time.Duration(i).String()})
	}
	e.srv.fmu.Lock()
	n := len(e.srv.flows)
	e.srv.fmu.Unlock()
	if n > maxFlows {
		t.Fatalf("flow table grew to %d", n)
	}
	b := e.browser()
	hops := b.login("alice-sub")
	if last := hops[len(hops)-1]; last.StatusCode != 200 {
		t.Fatalf("real login during a flood: %d", last.StatusCode)
	}
	// One pending flow per browser: starting again replaces the previous one.
	e.srv.putFlow("s1", &flow{bind: "same"})
	e.srv.putFlow("s2", &flow{bind: "same"})
	if e.srv.takeFlow("s1") != nil || e.srv.takeFlow("s2") == nil {
		t.Fatal("a browser must have at most one pending flow")
	}
}

func TestSettingsSizeIsCapped(t *testing.T) {
	e := newEnv(t, nil)
	sess := e.srv.store.Create(Identity{Sub: "alice-sub", Username: "alice@example.com", Groups: []string{"devs"}})
	ue, release, _ := e.srv.pool.Acquire(sess)
	defer release()
	big := make([]byte, 100<<10)
	for i := range big {
		big[i] = 'x'
	}
	var firstErr error
	for i := 0; i < 10 && firstErr == nil; i++ {
		_, firstErr = ue.set.Patch(map[string]any{"acks": map[string]any{string(rune('a' + i)): string(big)}})
	}
	if firstErr == nil {
		t.Fatal("settings grew without bound")
	}
}

func TestSettingsStoreSharedAcrossGroupChanges(t *testing.T) {
	e := newEnv(t, nil)
	a := e.srv.store.Create(Identity{Sub: "alice-sub", Username: "alice@example.com", Groups: []string{"devs"}})
	b := e.srv.store.Create(Identity{Sub: "alice-sub", Username: "alice@example.com", Groups: []string{"devs", "ops"}})
	ua, ra, _ := e.srv.pool.Acquire(a)
	ub, rb, _ := e.srv.pool.Acquire(b)
	defer ra()
	defer rb()
	if ua == ub {
		t.Fatal("different group sets must be different engines")
	}
	if ua.set != ub.set {
		t.Fatal("one user's engines must share one settings store (no lost updates)")
	}
}

func TestWaitDoesNotReserveClusterCapacityWhileQueuedOnUserBucket(t *testing.T) {
	cluster := rate.NewLimiter(100, 200)
	l := newCompositeLimiter(1, 1, cluster, nil)
	if !l.TryAccept() {
		t.Fatal("first token")
	}
	before := cluster.Tokens()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Wait(ctx); close(done) }() // queued behind the user's own bucket for ~1 s
	time.Sleep(100 * time.Millisecond)
	if after := cluster.Tokens(); after < before-0.5 {
		t.Fatalf("queued request already took cluster capacity: %.1f -> %.1f", before, after)
	}
	cancel()
	<-done
}
