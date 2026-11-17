package hubble

import (
	"sync"
	"time"
)

// Sampler caps a flow stream at Max flows per second (a token bucket) and measures the true offered rate over
// the last second, so the UI can say "showing 200/s of ~3,400/s".
type Sampler struct {
	Max int
	Now func() time.Time

	mu      sync.Mutex
	tokens  float64
	last    time.Time
	buckets [10]rateBucket
}

type rateBucket struct {
	idx            int64
	offered, shown int
}

// NewSampler builds a sampler.
func NewSampler(max int, now func() time.Time) *Sampler {
	if now == nil {
		now = time.Now
	}
	s := &Sampler{Max: max, Now: now}
	s.tokens = s.capacity()
	return s
}

func (s *Sampler) capacity() float64 { return float64(max(1, s.Max/10)) }

// Offer records one flow and reports whether it may be sent.
func (s *Sampler) Offer() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	if s.last.IsZero() {
		s.last = now
	}
	s.tokens = min(s.capacity(), s.tokens+now.Sub(s.last).Seconds()*float64(s.Max))
	s.last = now
	b := s.bucket(now)
	b.offered++
	if s.tokens >= 1 {
		s.tokens--
		b.shown++
		return true
	}
	return false
}

func (s *Sampler) bucket(now time.Time) *rateBucket {
	idx := now.UnixMilli() / 100
	b := &s.buckets[idx%10]
	if b.idx != idx {
		*b = rateBucket{idx: idx}
	}
	return b
}

// Rates returns the flows offered and sent per second over the last second.
func (s *Sampler) Rates() (shown, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.Now().UnixMilli() / 100
	for _, b := range s.buckets {
		if b.idx > cur-10 && b.idx <= cur {
			shown += b.shown
			total += b.offered
		}
	}
	return
}
