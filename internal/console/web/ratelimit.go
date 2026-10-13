package web

import (
	"sync"
	"time"
)

// maxLimiterKeys bounds limiter memory. Past it, unseen keys are refused.
const maxLimiterKeys = 100_000

// Limits are per replica: with N replicas the effective limit is up to N times higher.
const (
	writeBurst  = 20
	writeEvery  = 3 * time.Second // per session, on writes
	sweepEvery  = time.Minute
	retryAfterS = "10"
)

type bucket struct {
	tokens float64
	last   time.Time
}

// limiter is a token bucket per key.
type limiter struct {
	mu        sync.Mutex
	now       func() time.Time
	burst     float64
	every     time.Duration
	buckets   map[string]*bucket
	lastSweep time.Time
}

func newLimiter(burst int, every time.Duration) *limiter {
	return &limiter{now: time.Now, burst: float64(burst), every: every, buckets: map[string]*bucket{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) > sweepEvery {
		l.sweep(now)
	}
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= maxLimiterKeys {
			return false
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+float64(now.Sub(b.last))/float64(l.every))
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep drops buckets that have refilled completely, as they equal a fresh one.
func (l *limiter) sweep(now time.Time) {
	l.lastSweep = now
	full := time.Duration(l.burst) * l.every
	for k, b := range l.buckets {
		if now.Sub(b.last) >= full {
			delete(l.buckets, k)
		}
	}
}
