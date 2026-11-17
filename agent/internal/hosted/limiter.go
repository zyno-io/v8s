package hosted

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/client-go/util/flowcontrol"
)

// compositeLimiter charges one request against a per-(user, cluster) bucket and the shared per-cluster
// ceiling. It implements client-go's flowcontrol.RateLimiter.
type compositeLimiter struct {
	user    *rate.Limiter
	cluster *rate.Limiter
	now     func() time.Time
}

var _ flowcontrol.RateLimiter = (*compositeLimiter)(nil)

func newCompositeLimiter(userQPS float64, userBurst int, cluster *rate.Limiter, now func() time.Time) *compositeLimiter {
	if now == nil {
		now = time.Now
	}
	return &compositeLimiter{user: rate.NewLimiter(rate.Limit(userQPS), userBurst), cluster: cluster, now: now}
}

// reserve takes a token from both buckets and returns how long the caller must wait; cancel returns both tokens.
func (c *compositeLimiter) reserve(t time.Time) (delay time.Duration, cancel func()) {
	ru := c.user.ReserveN(t, 1)
	rc := c.cluster.ReserveN(t, 1)
	return max(ru.DelayFrom(t), rc.DelayFrom(t)), func() { ru.CancelAt(t); rc.CancelAt(t) }
}

func (c *compositeLimiter) TryAccept() bool {
	t := c.now()
	d, cancel := c.reserve(t)
	if d > 0 {
		cancel()
		return false
	}
	return true
}

func (c *compositeLimiter) Accept() { _ = c.Wait(context.Background()) }

// Wait queues on the user's own bucket first and only then takes a token from the shared cluster bucket, so a
// user's backlog cannot reserve the cluster's capacity ahead of other users' requests.
func (c *compositeLimiter) Wait(ctx context.Context) error {
	if err := waitOn(ctx, c.user, c.now); err != nil {
		return err
	}
	return waitOn(ctx, c.cluster, c.now)
}

func waitOn(ctx context.Context, l *rate.Limiter, now func() time.Time) error {
	t := now()
	r := l.ReserveN(t, 1)
	d := r.DelayFrom(t)
	if d <= 0 {
		return nil
	}
	if dl, ok := ctx.Deadline(); ok && dl.Before(t.Add(d)) {
		r.CancelAt(t)
		return context.DeadlineExceeded
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		r.CancelAt(now())
		return ctx.Err()
	}
}

func (c *compositeLimiter) Stop()        {}
func (c *compositeLimiter) QPS() float32 { return float32(c.user.Limit()) }

// clusterLimiters holds the per-cluster ceilings shared by every user.
type clusterLimiters struct {
	qps   float64
	burst int
	mu    sync.Mutex
	by    map[string]*rate.Limiter
}

func (l *clusterLimiters) get(id string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.by == nil {
		l.by = map[string]*rate.Limiter{}
	}
	r := l.by[id]
	if r == nil {
		r = rate.NewLimiter(rate.Limit(l.qps), l.burst)
		l.by[id] = r
	}
	return r
}
