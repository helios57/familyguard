package httpapi

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRateLimiterAllowsUpToTheLimitThenBlocks(t *testing.T) {
	r := NewRateLimiter(5, 100)
	for i := 0; i < 5; i++ {
		if !r.Allow("a") {
			t.Fatalf("request %d blocked while still inside the budget", i+1)
		}
	}
	if r.Allow("a") {
		t.Fatal("request past the budget was allowed")
	}
	// Positive control: a different key still has its own budget, so the limiter is not simply
	// blocking everything after the first burst.
	if !r.Allow("b") {
		t.Fatal("a different client was blocked by another client's usage")
	}
}

func TestRateLimiterRefills(t *testing.T) {
	r := NewRateLimiter(60, 100) // one token per second
	base := time.Now()
	r.now = func() time.Time { return base }

	for i := 0; i < 60; i++ {
		if !r.Allow("a") {
			t.Fatalf("request %d blocked while still inside the budget", i+1)
		}
	}
	if r.Allow("a") {
		t.Fatal("request past the budget was allowed")
	}

	r.now = func() time.Time { return base.Add(2 * time.Second) }
	if !r.Allow("a") {
		t.Fatal("two seconds later the bucket had not refilled at all")
	}
	if !r.Allow("a") {
		t.Fatal("the second refilled token was not available")
	}
	if r.Allow("a") {
		t.Fatal("more tokens were available than two seconds of refill provides")
	}
}

// TestRateLimiterMemoryIsBounded is the reason this limiter is hand-written rather than a map of
// buckets: it asserts the property that a naive implementation silently lacks. An unbounded
// limiter passes every other test in this file.
func TestRateLimiterMemoryIsBounded(t *testing.T) {
	const capKeys = 64
	r := NewRateLimiter(10, capKeys)
	for i := 0; i < 100000; i++ {
		r.Allow(fmt.Sprintf("client-%d", i))
	}
	if got := r.Len(); got > capKeys {
		t.Fatalf("limiter tracks %d keys with a capacity of %d — memory is unbounded", got, capKeys)
	}
	if got := r.Len(); got != capKeys {
		t.Fatalf("expected the limiter to be full at %d keys, got %d", capKeys, got)
	}
}

// TestRateLimiterEvictsLeastRecentlyUsed: a busy client must keep its bucket while one-shot keys
// churn through, otherwise flooding from new addresses would reset an attacker's own limit.
func TestRateLimiterEvictsLeastRecentlyUsed(t *testing.T) {
	r := NewRateLimiter(3, 4)
	for i := 0; i < 3; i++ {
		if !r.Allow("busy") {
			t.Fatalf("busy client blocked early at %d", i)
		}
	}
	if r.Allow("busy") {
		t.Fatal("busy client should be out of tokens")
	}
	for i := 0; i < 50; i++ {
		r.Allow(fmt.Sprintf("churn-%d", i))
		if r.Allow("busy") {
			t.Fatal("busy client regained tokens — its bucket was evicted by churn")
		}
	}
}

func TestRateLimiterIsSafeUnderConcurrency(t *testing.T) {
	r := NewRateLimiter(1000, 100)
	var wg sync.WaitGroup
	allowed := make([]int, 8)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				if r.Allow("shared") {
					allowed[w]++
				}
			}
		}(w)
	}
	wg.Wait()
	total := 0
	for _, n := range allowed {
		total += n
	}
	// 4000 attempts against a 1000-token bucket. Refill during the run is negligible but not
	// exactly zero, so the assertion is a bound rather than an equality.
	if total > 1100 {
		t.Fatalf("allowed %d requests against a burst of 1000 — the bucket is not shared", total)
	}
	if total < 900 {
		t.Fatalf("allowed only %d requests out of a burst of 1000 — tokens were lost", total)
	}
}

func TestRateLimiterRejectsNonsenseConfiguration(t *testing.T) {
	r := NewRateLimiter(0, 0)
	if !r.Allow("a") {
		t.Fatal("a zero limit must clamp to something usable, not block everything")
	}
	if r.Allow("a") {
		t.Fatal("a zero limit must clamp to one, not to unlimited")
	}
	if r.capacity != DefaultRateLimitKeys {
		t.Fatalf("zero capacity should fall back to the default, got %d", r.capacity)
	}
}
