// Package streams implements the per-stream WebSockets of the agent (docs/protocol.md "Streams"): pod logs
// (single and merged multi-pod), exec, debug containers, node shells and port-forwards, with credit-based flow
// control for logs and the write-context idle/cap limits for interactive streams.
package streams

import (
	"context"
	"sync"
	"time"
)

// Transport is one stream socket (the wire layer adapts a WebSocket to it).
type Transport interface {
	// Read returns the next client frame; binary says whether it is a binary (vs text) frame.
	Read(ctx context.Context) (binary bool, data []byte, err error)
	Write(ctx context.Context, binary bool, data []byte) error
	Close(code int, reason string)
}

// Close codes the streams use.
const (
	CloseNormal = 1000
	CloseError  = 1011
	ClosePolicy = 1008
)

// limits applies the write-context stream limits: an idle timeout and a hard cap. Touch resets the idle timer.
type limits struct {
	cancel context.CancelCauseFunc
	idle   time.Duration
	mu     sync.Mutex
	timer  *time.Timer
	cap    *time.Timer
}

// ErrIdle and ErrCap are the cancellation causes of the limits.
type limitErr string

func (e limitErr) Error() string { return string(e) }

const (
	ErrIdle limitErr = "stream closed: idle timeout"
	ErrCap  limitErr = "stream closed: maximum session length reached"
)

// withLimits returns a context cancelled with ErrIdle after idle without Touch, and ErrCap after maxDur. Zero
// durations disable the respective limit. stop releases the timers.
func withLimits(parent context.Context, idle, maxDur time.Duration) (context.Context, *limits) {
	ctx, cancel := context.WithCancelCause(parent)
	l := &limits{cancel: cancel, idle: idle}
	if idle > 0 {
		l.timer = time.AfterFunc(idle, func() { cancel(ErrIdle) })
	}
	if maxDur > 0 {
		l.cap = time.AfterFunc(maxDur, func() { cancel(ErrCap) })
	}
	return ctx, l
}

func (l *limits) Touch() {
	if l == nil || l.timer == nil {
		return
	}
	l.mu.Lock()
	l.timer.Reset(l.idle)
	l.mu.Unlock()
}

func (l *limits) Stop() {
	if l == nil {
		return
	}
	if l.timer != nil {
		l.timer.Stop()
	}
	if l.cap != nil {
		l.cap.Stop()
	}
	l.cancel(context.Canceled)
}
