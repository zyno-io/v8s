package streams

import (
	"context"
	"sync"
)

// Credit is the byte credit a client grants a log stream. The sender takes credit for every frame and waits
// while the balance is not positive, so it stops reading from the API server when the client is behind (TCP
// backpressure reaches the kubelet). A frame may overshoot the balance by at most one frame.
type Credit struct {
	mu      sync.Mutex
	balance int64
	wake    chan struct{}
}

// NewCredit starts with an initial balance.
func NewCredit(initial int64) *Credit { return &Credit{balance: initial, wake: make(chan struct{}, 1)} }

// Add grants n more bytes.
func (c *Credit) Add(n int64) {
	if n <= 0 {
		return
	}
	c.mu.Lock()
	c.balance += n
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Balance returns the current balance (tests).
func (c *Credit) Balance() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.balance }

// Take waits until the balance is positive, then deducts n.
func (c *Credit) Take(ctx context.Context, n int64) error {
	for {
		c.mu.Lock()
		if c.balance > 0 {
			c.balance -= n
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		select {
		case <-c.wake:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}
