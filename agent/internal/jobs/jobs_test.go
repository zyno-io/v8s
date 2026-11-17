package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped && !t.fired
		t.stopped = true
		return was
	}
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !t.at.After(c.now) {
			t.fired = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.f()
	}
}

func newMgr(t *testing.T) (*Manager, *fakeClock) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clk := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return New(ctx, clk), clk
}

func TestLifecycleDoneFailedCancelled(t *testing.T) {
	m, _ := newMgr(t)
	m.ClientConnected()
	ok := m.Start("", "drain", "c1", "node-1", func(ctx context.Context, jc *Ctx) error {
		jc.Progress("working", "item", "evicting")
		return nil
	})
	bad := m.Start("", "drain", "c1", "node-2", func(ctx context.Context, jc *Ctx) error {
		return &Fail{Body: protocol.ErrorBody{Code: "invalid", Message: "nope"}}
	})
	block := make(chan struct{})
	can := m.Start("", "drain", "c1", "node-3", func(ctx context.Context, jc *Ctx) error { <-ctx.Done(); close(block); return ctx.Err() })
	for _, id := range []string{ok, bad} {
		m.Wait(id)
	}
	if err := m.Cancel(can); err != nil {
		t.Fatal(err)
	}
	m.Wait(can)
	get := func(id string) protocol.JobFrame { f, _ := m.Get(id); return f }
	if f := get(ok); f.State != protocol.JobDone || len(f.Progress) != 1 || f.Progress[0].State != "evicting" || f.EndedAt == 0 {
		t.Fatalf("%+v", f)
	}
	if f := get(bad); f.State != protocol.JobFailed || f.Error == nil || f.Error.Code != "invalid" {
		t.Fatalf("%+v", f)
	}
	if f := get(can); f.State != protocol.JobCancelled {
		t.Fatalf("%+v", f)
	}
	if err := m.Cancel("nope"); err == nil {
		t.Fatal("unknown job")
	}
	if l := m.List(); len(l) != 3 {
		t.Fatalf("%d jobs", len(l))
	}
}

func TestRequestIDDedupesRetries(t *testing.T) {
	m, _ := newMgr(t)
	runs := 0
	f := func(ctx context.Context, jc *Ctx) error { runs++; return nil }
	a := m.Start("req-1", "drain", "c", "n", f)
	b := m.Start("req-1", "drain", "c", "n", f)
	m.Wait(a)
	if a != b || runs != 1 {
		t.Fatalf("a=%s b=%s runs=%d", a, b, runs)
	}
}

// The bounded exception to "nothing runs without a client": a job continues for 15 minutes after the last
// client closed, then it is stopped.
func TestNoClientBound(t *testing.T) {
	m, clk := newMgr(t)
	m.ClientConnected()
	started := make(chan struct{})
	id := m.Start("", "drain", "c1", "n1", func(ctx context.Context, jc *Ctx) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	<-started
	m.ClientDisconnected()
	clk.advance(14*time.Minute + 59*time.Second)
	if f, _ := m.Get(id); f.State != protocol.JobRunning {
		t.Fatalf("stopped early: %+v", f)
	}
	clk.advance(2 * time.Second)
	m.Wait(id)
	f, _ := m.Get(id)
	if f.State != protocol.JobCancelled || len(f.Progress) == 0 || f.Progress[len(f.Progress)-1].Message != ErrNoClient.Error() {
		t.Fatalf("%+v", f)
	}
}

func TestReconnectCancelsTheCountdown(t *testing.T) {
	m, clk := newMgr(t)
	m.ClientConnected()
	stop := make(chan struct{})
	id := m.Start("", "drain", "c1", "n1", func(ctx context.Context, jc *Ctx) error {
		select {
		case <-stop:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	m.ClientDisconnected()
	clk.advance(10 * time.Minute)
	m.ClientConnected() // a client is back: the job is not stopped, the countdown restarts at the next disconnect
	clk.advance(10 * time.Minute)
	if f, _ := m.Get(id); f.State != protocol.JobRunning {
		t.Fatalf("%+v", f)
	}
	m.ClientDisconnected()
	clk.advance(14 * time.Minute)
	if f, _ := m.Get(id); f.State != protocol.JobRunning {
		t.Fatalf("restarted countdown: %+v", f)
	}
	close(stop)
	m.Wait(id)
	if f, _ := m.Get(id); f.State != protocol.JobDone {
		t.Fatalf("%+v", f)
	}
}

func TestSubscriptionCoalescesAndKeepsFinalState(t *testing.T) {
	m, _ := newMgr(t)
	sub := m.Subscribe()
	defer sub.Close()
	id := m.Start("", "drain", "c1", "n1", func(ctx context.Context, jc *Ctx) error {
		for i := 0; i < 500; i++ {
			jc.Progress("p", "i", "s")
		}
		return errors.New("boom")
	})
	m.Wait(id)
	<-sub.C
	frames := sub.Drain()
	if len(frames) != 1 || frames[0].State != protocol.JobFailed || len(frames[0].Progress) > MaxProgress || frames[0].ProgressDropped == 0 {
		t.Fatalf("%d frames, %+v", len(frames), frames[0].State)
	}
}

// Regression: a job started while no client is connected still gets the 15-minute bound.
func TestStartWithoutClientArmsIdleTimer(t *testing.T) {
	m, clk := newMgr(t)
	id := m.Start("", "drain", "c1", "n1", func(ctx context.Context, jc *Ctx) error {
		<-ctx.Done()
		return ctx.Err()
	})
	clk.advance(14*time.Minute + 59*time.Second)
	if f, _ := m.Get(id); f.State != protocol.JobRunning {
		t.Fatalf("stopped early: %+v", f)
	}
	clk.advance(2 * time.Second)
	m.Wait(id)
	if f, _ := m.Get(id); f.State != protocol.JobCancelled {
		t.Fatalf("%+v", f)
	}
}

func TestLookupFindsRequestID(t *testing.T) {
	m, _ := newMgr(t)
	if _, ok := m.Lookup("r"); ok {
		t.Fatal("unknown id")
	}
	id := m.Start("r", "drain", "c", "n", func(context.Context, *Ctx) error { return nil })
	if got, ok := m.Lookup("r"); !ok || got != id {
		t.Fatalf("%s %v", got, ok)
	}
	if _, ok := m.Lookup(""); ok {
		t.Fatal("empty id is never deduped")
	}
}
