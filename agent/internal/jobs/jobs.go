// Package jobs runs agent-owned long operations (drains, Helm actions) that survive a client disconnect:
// a job keeps running after the last client closes for at most NoClientGrace (15 minutes), then it is
// cancelled (docs/operations.md: Idempotency and long-running jobs). Job state lives in memory only.
package jobs

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

const (
	// NoClientGrace is how long jobs continue after the last client closed.
	NoClientGrace = 15 * time.Minute
	// MaxProgress is how many progress entries a job frame carries.
	MaxProgress  = 200
	dedupeTTL    = 10 * time.Minute
	keepFinished = 50
)

// Clock is the time source (injectable for tests).
type Clock interface {
	Now() time.Time
	// AfterFunc calls f after d unless stopped.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) func() bool {
	t := time.AfterFunc(d, f)
	return t.Stop
}

// ErrNoClient is the cancellation cause when the no-client bound expired.
var ErrNoClient = errors.New("stopped: no client was connected for 15 minutes")

// Run is a job body. It reports progress through jc and returns nil (done), ctx's error when cancelled, or
// a failure (*protocol.ErrorBody wrapped by Fail, or any error).
type Run func(ctx context.Context, jc *Ctx) error

// Ctx is the handle a job body reports through.
type Ctx struct {
	j *job
	m *Manager
}

// Progress appends a progress entry and publishes the job.
func (c *Ctx) Progress(message, item, state string) {
	c.m.mu.Lock()
	c.j.addProgress(protocol.JobProgress{At: c.m.clock.Now().UnixMilli(), Message: message, Item: item, State: state})
	c.m.mu.Unlock()
	c.m.publish(c.j)
}

// SetContext records the context the job runs through (usedContext).
func (c *Ctx) SetContext(id string) {
	c.m.mu.Lock()
	c.j.f.UsedContext = id
	c.m.mu.Unlock()
}

type job struct {
	f      protocol.JobFrame
	cancel context.CancelCauseFunc
	done   chan struct{}
	drop   int
}

func (j *job) addProgress(p protocol.JobProgress) {
	j.f.Progress = append(j.f.Progress, p)
	if n := len(j.f.Progress); n > 2*MaxProgress {
		j.drop += n - MaxProgress
		j.f.Progress = append([]protocol.JobProgress(nil), j.f.Progress[n-MaxProgress:]...)
	}
}

// Fail wraps a protocol error so a job body can fail with a specific code.
type Fail struct{ Body protocol.ErrorBody }

func (f *Fail) Error() string { return f.Body.Code + ": " + f.Body.Message }

// Manager owns the jobs of one agent.
type Manager struct {
	clock Clock

	mu       sync.Mutex
	jobs     map[string]*job
	order    []string
	seq      int
	clients  int
	stopIdle func() bool
	dd       map[string]ddEntry
	subs     map[*Subscription]struct{}
	baseCtx  context.Context
}

type ddEntry struct {
	id  string
	exp time.Time
}

// New builds a Manager. clock nil uses real time. Jobs run under ctx.
func New(ctx context.Context, clock Clock) *Manager {
	if clock == nil {
		clock = realClock{}
	}
	return &Manager{clock: clock, jobs: map[string]*job{}, dd: map[string]ddEntry{}, subs: map[*Subscription]struct{}{}, baseCtx: ctx}
}

// ClientConnected / ClientDisconnected track the connected data sockets. When the last one closes, running
// jobs get NoClientGrace to finish; a client connecting again cancels the countdown.
func (m *Manager) ClientConnected() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients++
	if m.stopIdle != nil {
		m.stopIdle()
		m.stopIdle = nil
	}
}

func (m *Manager) ClientDisconnected() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clients > 0 {
		m.clients--
	}
	if m.clients == 0 && m.stopIdle == nil && m.runningLocked() > 0 {
		m.stopIdle = m.clock.AfterFunc(NoClientGrace, m.idleExpired)
	}
}

func (m *Manager) runningLocked() int {
	n := 0
	for _, j := range m.jobs {
		if j.f.State == protocol.JobRunning {
			n++
		}
	}
	return n
}

func (m *Manager) idleExpired() {
	m.mu.Lock()
	m.stopIdle = nil
	if m.clients > 0 {
		m.mu.Unlock()
		return
	}
	var cancels []context.CancelCauseFunc
	for _, j := range m.jobs {
		if j.f.State == protocol.JobRunning {
			cancels = append(cancels, j.cancel)
		}
	}
	m.mu.Unlock()
	for _, c := range cancels {
		c(ErrNoClient)
	}
}

// Lookup returns the job a requestID started within the dedupe window, so callers can answer a retry without
// doing any (possibly expensive or side-effecting) preparation again.
func (m *Manager) Lookup(requestID string) (string, bool) {
	if requestID == "" {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.dd[requestID]; ok && m.clock.Now().Before(e.exp) {
		return e.id, true
	}
	return "", false
}

// Start runs fn as a new job. requestID dedupes retries for 10 minutes (the same id returns the same job).
func (m *Manager) Start(requestID, typ, cluster, subject string, fn Run) string {
	m.mu.Lock()
	now := m.clock.Now()
	if requestID != "" {
		if e, ok := m.dd[requestID]; ok && now.Before(e.exp) {
			m.mu.Unlock()
			return e.id
		}
	}
	m.seq++
	id := "job-" + itoa(m.seq) + "-" + randID()
	ctx, cancel := context.WithCancelCause(m.baseCtx)
	j := &job{cancel: cancel, done: make(chan struct{}),
		f: protocol.JobFrame{JobID: id, Type: typ, State: protocol.JobRunning, Cluster: cluster, Subject: subject, StartedAt: now.UnixMilli(), Progress: []protocol.JobProgress{}}}
	m.jobs[id] = j
	m.order = append(m.order, id)
	if requestID != "" {
		m.dd[requestID] = ddEntry{id: id, exp: now.Add(dedupeTTL)}
	}
	m.trimLocked()
	if m.clients == 0 && m.stopIdle == nil { // started while nobody is connected: the 15-minute bound runs from now
		m.stopIdle = m.clock.AfterFunc(NoClientGrace, m.idleExpired)
	}
	m.mu.Unlock()
	m.publish(j)
	go m.run(ctx, j, fn)
	return id
}

func (m *Manager) run(ctx context.Context, j *job, fn Run) {
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = errors.New("job panicked")
			}
		}()
		err = fn(ctx, &Ctx{j: j, m: m})
	}()
	m.mu.Lock()
	j.f.EndedAt = m.clock.Now().UnixMilli()
	switch {
	case err == nil:
		j.f.State = protocol.JobDone
	case ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, ctx.Err()) || errors.Is(err, ErrNoClient)):
		j.f.State = protocol.JobCancelled
		msg := "cancelled"
		if errors.Is(context.Cause(ctx), ErrNoClient) {
			msg = ErrNoClient.Error()
		}
		j.addProgress(protocol.JobProgress{At: j.f.EndedAt, Message: msg})
	default:
		j.f.State = protocol.JobFailed
		var f *Fail
		if errors.As(err, &f) {
			b := f.Body
			j.f.Error = &b
		} else {
			j.f.Error = &protocol.ErrorBody{Code: "internal", Message: err.Error()}
		}
	}
	j.f.ProgressDropped = j.drop
	if m.stopIdle != nil && m.runningLocked() == 0 {
		m.stopIdle()
		m.stopIdle = nil
	}
	m.mu.Unlock()
	close(j.done)
	m.publish(j)
}

func (m *Manager) trimLocked() {
	if len(m.order) <= keepFinished {
		return
	}
	kept := m.order[:0]
	finished := 0
	for i := len(m.order) - 1; i >= 0; i-- { // keep the newest finished ones
		id := m.order[i]
		j := m.jobs[id]
		if j.f.State != protocol.JobRunning {
			finished++
			if finished > keepFinished {
				delete(m.jobs, id)
				continue
			}
		}
		kept = append(kept, id)
	}
	for i, k := 0, len(kept)-1; i < k; i, k = i+1, k-1 {
		kept[i], kept[k] = kept[k], kept[i]
	}
	m.order = kept
}

// Get returns a job's frame.
func (m *Manager) Get(id string) (protocol.JobFrame, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[id]
	if j == nil {
		return protocol.JobFrame{}, false
	}
	return m.frameLocked(j), true
}

func (m *Manager) frameLocked(j *job) protocol.JobFrame {
	f := j.f
	f.Progress = append([]protocol.JobProgress{}, j.f.Progress...)
	if len(f.Progress) > MaxProgress {
		f.ProgressDropped = j.drop + len(f.Progress) - MaxProgress
		f.Progress = f.Progress[len(f.Progress)-MaxProgress:]
	} else {
		f.ProgressDropped = j.drop
	}
	return f
}

// List returns every job (running first, then newest first).
func (m *Manager) List() []protocol.JobFrame {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]protocol.JobFrame, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		out = append(out, m.frameLocked(m.jobs[m.order[i]]))
	}
	// stable partition: running first
	run := out[:0:0]
	rest := out[:0:0]
	for _, f := range out {
		if f.State == protocol.JobRunning {
			run = append(run, f)
		} else {
			rest = append(rest, f)
		}
	}
	return append(run, rest...)
}

// Cancel cancels a running job (a drain leaves the node cordoned).
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	j := m.jobs[id]
	m.mu.Unlock()
	if j == nil {
		return errors.New("no such job")
	}
	j.cancel(context.Canceled)
	return nil
}

// Wait blocks until the job ended (tests).
func (m *Manager) Wait(id string) {
	m.mu.Lock()
	j := m.jobs[id]
	m.mu.Unlock()
	if j != nil {
		<-j.done
	}
}

// Subscription delivers changed jobs: C is signaled (coalesced) when something changed, Drain returns the
// current frame of every job that changed since the last call. A slow reader therefore never loses a final
// state, it just sees fewer intermediate ones.
type Subscription struct {
	C     <-chan struct{}
	c     chan struct{}
	m     *Manager
	dirty map[string]struct{}
}

func (s *Subscription) Drain() []protocol.JobFrame {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	var out []protocol.JobFrame
	for id := range s.dirty {
		if j := s.m.jobs[id]; j != nil {
			out = append(out, s.m.frameLocked(j))
		}
		delete(s.dirty, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out
}

func (s *Subscription) Close() {
	s.m.mu.Lock()
	delete(s.m.subs, s)
	s.m.mu.Unlock()
}

// Subscribe registers a subscription (one per connected data socket).
func (m *Manager) Subscribe() *Subscription {
	c := make(chan struct{}, 1)
	s := &Subscription{C: c, c: c, m: m, dirty: map[string]struct{}{}}
	m.mu.Lock()
	m.subs[s] = struct{}{}
	m.mu.Unlock()
	return s
}

func (m *Manager) publish(j *job) {
	m.mu.Lock()
	for s := range m.subs {
		s.dirty[j.f.JobID] = struct{}{}
		select {
		case s.c <- struct{}{}:
		default:
		}
	}
	m.mu.Unlock()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}
