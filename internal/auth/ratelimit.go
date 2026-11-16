package auth

import (
	"sync"
	"time"
)

// RateLimiter is an in-memory token bucket keyed by an arbitrary string
// (for login: client IP + email). It is safe for concurrent use.
type RateLimiter struct {
	mu       sync.Mutex
	capacity float64
	rate     float64 // tokens per second
	buckets  map[string]*bucket
	now      func() time.Time
	calls    int
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter allows burst events per period, refilling continuously.
func NewRateLimiter(burst int, period time.Duration) *RateLimiter {
	return &RateLimiter{
		capacity: float64(burst),
		rate:     float64(burst) / period.Seconds(),
		buckets:  map[string]*bucket{},
		now:      time.Now,
	}
}

// Allow consumes one token for key and reports whether it was available.
func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.calls++
	if l.calls%1024 == 0 {
		l.sweep(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.capacity, last: now}
		l.buckets[key] = b
	} else {
		b.tokens = min(l.capacity, b.tokens+now.Sub(b.last).Seconds()*l.rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep drops buckets that are full again (idle long enough).
func (l *RateLimiter) sweep(now time.Time) {
	full := time.Duration(l.capacity / l.rate * float64(time.Second))
	for k, b := range l.buckets {
		if now.Sub(b.last) > full {
			delete(l.buckets, k)
		}
	}
}
