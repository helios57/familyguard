package httpapi

import (
	"container/list"
	"sync"
	"time"
)

// DefaultRateLimitKeys bounds how many distinct clients the limiter tracks at once.
//
// The bound is the point. A limiter that keeps one bucket per client address in an unbounded map
// is itself the denial-of-service: an attacker sending one request each from a large address space
// grows the map until the process dies, and the limiter reports nothing wrong while it happens
// (NFR-9). Eviction is least-recently-used, so evicting a key costs an attacker their own bucket
// before it costs a real client theirs.
const DefaultRateLimitKeys = 20000

// RateLimiter is a fixed-capacity, LRU-evicting token-bucket limiter.
type RateLimiter struct {
	mu       sync.Mutex
	capacity int
	burst    float64
	refill   float64 // tokens per second
	order    *list.List
	entries  map[string]*list.Element
	now      func() time.Time
}

type bucket struct {
	key    string
	tokens float64
	last   time.Time
}

// NewRateLimiter builds a limiter allowing perMinute requests per key per minute, with a burst of
// the same size. maxKeys of zero uses DefaultRateLimitKeys.
func NewRateLimiter(perMinute, maxKeys int) *RateLimiter {
	if perMinute < 1 {
		perMinute = 1
	}
	if maxKeys < 1 {
		maxKeys = DefaultRateLimitKeys
	}
	return &RateLimiter{
		capacity: maxKeys,
		burst:    float64(perMinute),
		refill:   float64(perMinute) / 60.0,
		order:    list.New(),
		entries:  make(map[string]*list.Element, maxKeys),
		now:      time.Now,
	}
}

// Allow consumes one token for key and reports whether the request may proceed.
func (r *RateLimiter) Allow(key string) bool {
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()

	if el, ok := r.entries[key]; ok {
		r.order.MoveToFront(el)
		b := el.Value.(*bucket)
		b.tokens = min(r.burst, b.tokens+now.Sub(b.last).Seconds()*r.refill)
		b.last = now
		if b.tokens < 1 {
			return false
		}
		b.tokens--
		return true
	}

	for r.order.Len() >= r.capacity {
		oldest := r.order.Back()
		if oldest == nil {
			break
		}
		r.order.Remove(oldest)
		delete(r.entries, oldest.Value.(*bucket).key)
	}

	b := &bucket{key: key, tokens: r.burst - 1, last: now}
	r.entries[key] = r.order.PushFront(b)
	return true
}

// Len reports how many keys are tracked. Tests assert on it: without a way to read the size, an
// unbounded limiter and a bounded one are indistinguishable from the outside.
func (r *RateLimiter) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.order.Len()
}
