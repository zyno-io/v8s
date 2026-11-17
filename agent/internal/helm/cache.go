package helm

import (
	"container/list"
	"context"
	"sync"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// DefaultCacheBytes is the decoded-release cache budget (docs/performance.md).
const DefaultCacheBytes int64 = 64 << 20

const entryOverhead = 1024

// MaxConcurrentFetches bounds lazy body fetches per cluster.
const MaxConcurrentFetches = 4

// Cache is an LRU of decoded, projected releases with a byte budget, keyed by secret uid + "/" +
// resourceVersion (a new resourceVersion misses). Entries larger than the budget are not stored.
type Cache struct {
	mu     sync.Mutex
	budget int64
	bytes  int64
	ll     *list.List
	m      map[string]*list.Element
}

type cacheEntry struct {
	key  string
	rel  protocol.HelmRelease
	cost int64
}

// NewCache creates a cache; budget <= 0 selects DefaultCacheBytes.
func NewCache(budget int64) *Cache {
	if budget <= 0 {
		budget = DefaultCacheBytes
	}
	return &Cache{budget: budget, ll: list.New(), m: map[string]*list.Element{}}
}

// CacheKey is the cache key of a release Secret.
func CacheKey(uid, resourceVersion string) string { return uid + "/" + resourceVersion }

func releaseCost(r *protocol.HelmRelease) int64 {
	return int64(len(r.Manifest)+len(r.Values)+len(r.ComputedValues)+len(r.Notes)+len(r.Description)) + entryOverhead +
		int64(len(r.Hooks))*128
}

// Get returns the cached release and marks it most recently used.
func (c *Cache) Get(key string) (protocol.HelmRelease, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*cacheEntry).rel, true
	}
	return protocol.HelmRelease{}, false
}

// Put stores r (replacing an existing entry) and evicts least recently used entries over budget.
func (c *Cache) Put(key string, r protocol.HelmRelease) {
	cost := releaseCost(&r)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok {
		c.removeLocked(e)
	}
	if cost > c.budget {
		return
	}
	c.m[key] = c.ll.PushFront(&cacheEntry{key: key, rel: r, cost: cost})
	c.bytes += cost
	for c.bytes > c.budget {
		back := c.ll.Back()
		if back == nil {
			break
		}
		c.removeLocked(back)
	}
}

func (c *Cache) removeLocked(e *list.Element) {
	ent := c.ll.Remove(e).(*cacheEntry)
	delete(c.m, ent.key)
	c.bytes -= ent.cost
}

// Len is the number of cached releases.
func (c *Cache) Len() int { c.mu.Lock(); defer c.mu.Unlock(); return c.ll.Len() }

// Bytes is the accounted size of the cache.
func (c *Cache) Bytes() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.bytes }

// limiter is a per-cluster semaphore of MaxConcurrentFetches.
type limiter struct {
	mu sync.Mutex
	m  map[string]chan struct{}
	n  int
}

func newLimiter(n int) *limiter { return &limiter{m: map[string]chan struct{}{}, n: n} }

// acquire blocks until a slot of cluster is free or ctx ends; the returned func releases it.
func (l *limiter) acquire(ctx context.Context, cluster string) (func(), error) {
	l.mu.Lock()
	ch := l.m[cluster]
	if ch == nil {
		ch = make(chan struct{}, l.n)
		l.m[cluster] = ch
	}
	l.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}
