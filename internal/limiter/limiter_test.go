package limiter_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ratelimiter/internal/limiter"
	"ratelimiter/internal/store/memory"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

const blockFor = 5 * time.Minute

func newLimiter(t *testing.T, clock *fakeClock, cfg limiter.Config) *limiter.Limiter {
	t.Helper()
	if cfg.BlockDuration == 0 {
		cfg.BlockDuration = blockFor
	}
	l, err := limiter.New(memory.NewWithClock(clock.Now), cfg)
	if err != nil {
		t.Fatalf("limiter.New: %v", err)
	}
	return l
}

// sends n requests and returns how many were allowed.
func send(t *testing.T, l *limiter.Limiter, n int, ip, token string) (allowed int) {
	t.Helper()
	for i := 0; i < n; i++ {
		ok, err := l.Allow(context.Background(), ip, token)
		if err != nil {
			t.Fatalf("Allow: %v", err)
		}
		if ok {
			allowed++
		}
	}
	return allowed
}

func TestIPLimit(t *testing.T) {
	l := newLimiter(t, newFakeClock(), limiter.Config{IPLimit: 5, DefaultTokenLimit: 100})

	if got := send(t, l, 5, "1.1.1.1", ""); got != 5 {
		t.Fatalf("first 5 requests: allowed %d, want 5", got)
	}
	if got := send(t, l, 1, "1.1.1.1", ""); got != 0 {
		t.Fatal("6th request from the same IP must be rejected")
	}
}

func TestTokenLimit(t *testing.T) {
	l := newLimiter(t, newFakeClock(), limiter.Config{
		IPLimit: 1, DefaultTokenLimit: 100, TokenLimits: map[string]int{"abc": 3},
	})

	if got := send(t, l, 3, "1.1.1.1", "abc"); got != 3 {
		t.Fatalf("first 3 requests: allowed %d, want 3", got)
	}
	if got := send(t, l, 1, "1.1.1.1", "abc"); got != 0 {
		t.Fatal("4th request with the same token must be rejected")
	}
}

func TestUnknownTokenUsesDefaultTokenLimit(t *testing.T) {
	l := newLimiter(t, newFakeClock(), limiter.Config{IPLimit: 2, DefaultTokenLimit: 7})

	if got := send(t, l, 20, "1.1.1.1", "unlisted"); got != 7 {
		t.Fatalf("allowed %d, want 7 (default token limit, not the IP limit)", got)
	}
}

// Golden rule: the token configuration overrides the IP configuration.
func TestTokenLimitOverridesLowerIPLimit(t *testing.T) {
	l := newLimiter(t, newFakeClock(), limiter.Config{
		IPLimit: 10, DefaultTokenLimit: 1, TokenLimits: map[string]int{"vip": 100},
	})

	if got := send(t, l, 100, "1.1.1.1", "vip"); got != 100 {
		t.Fatalf("with token (limit 100) allowed %d of 100, want 100", got)
	}
	if got := send(t, l, 1, "1.1.1.1", "vip"); got != 0 {
		t.Fatal("101st request with the token must be rejected")
	}

	// Same IP without a token falls back to the IP limit (10 req/s).
	if got := send(t, l, 20, "2.2.2.2", ""); got != 10 {
		t.Fatalf("without token allowed %d, want 10 (IP limit)", got)
	}
}

func TestTokenLimitOverridesHigherIPLimit(t *testing.T) {
	l := newLimiter(t, newFakeClock(), limiter.Config{
		IPLimit: 10, DefaultTokenLimit: 100, TokenLimits: map[string]int{"strict": 2},
	})

	if got := send(t, l, 10, "1.1.1.1", "strict"); got != 2 {
		t.Fatalf("allowed %d, want 2 (token limit wins even though IP limit is 10)", got)
	}
}

func TestIPAndTokenUseIndependentBuckets(t *testing.T) {
	l := newLimiter(t, newFakeClock(), limiter.Config{
		IPLimit: 2, DefaultTokenLimit: 5, TokenLimits: map[string]int{"abc": 5},
	})

	send(t, l, 3, "1.1.1.1", "") // exhausts and blocks the IP bucket
	if got := send(t, l, 1, "1.1.1.1", ""); got != 0 {
		t.Fatal("IP must be blocked")
	}
	if got := send(t, l, 5, "1.1.1.1", "abc"); got != 5 {
		t.Fatalf("token from a blocked IP allowed %d of 5, want 5", got)
	}
}

func TestIsolationBetweenClients(t *testing.T) {
	l := newLimiter(t, newFakeClock(), limiter.Config{IPLimit: 1, DefaultTokenLimit: 1})

	send(t, l, 2, "1.1.1.1", "") // blocks 1.1.1.1
	if got := send(t, l, 1, "2.2.2.2", ""); got != 1 {
		t.Fatal("blocking one IP must not affect another")
	}
	send(t, l, 2, "1.1.1.1", "tok-a") // blocks tok-a
	if got := send(t, l, 1, "1.1.1.1", "tok-b"); got != 1 {
		t.Fatal("blocking one token must not affect another")
	}
}

func TestWindowResetsWhenLimitIsNotExceeded(t *testing.T) {
	clock := newFakeClock()
	l := newLimiter(t, clock, limiter.Config{IPLimit: 3, DefaultTokenLimit: 3})

	for i := 0; i < 3; i++ {
		if got := send(t, l, 3, "1.1.1.1", ""); got != 3 {
			t.Fatalf("second %d: allowed %d, want 3", i, got)
		}
		clock.Advance(time.Second)
	}
}

func TestBlockLastsForBlockDuration(t *testing.T) {
	clock := newFakeClock()
	l := newLimiter(t, clock, limiter.Config{IPLimit: 2, DefaultTokenLimit: 2})

	if got := send(t, l, 3, "1.1.1.1", ""); got != 2 {
		t.Fatalf("allowed %d, want 2", got)
	}

	// Well past the 1s window, but still inside the block period.
	clock.Advance(blockFor - time.Second)
	if got := send(t, l, 1, "1.1.1.1", ""); got != 0 {
		t.Fatal("IP must stay blocked until BlockDuration elapses")
	}

	clock.Advance(2 * time.Second)
	if got := send(t, l, 2, "1.1.1.1", ""); got != 2 {
		t.Fatal("IP must be released after BlockDuration")
	}
}

func TestTokenBlockLastsForBlockDuration(t *testing.T) {
	clock := newFakeClock()
	l := newLimiter(t, clock, limiter.Config{IPLimit: 10, DefaultTokenLimit: 1})

	send(t, l, 2, "1.1.1.1", "abc")
	clock.Advance(time.Minute)
	if got := send(t, l, 1, "1.1.1.1", "abc"); got != 0 {
		t.Fatal("token must stay blocked during the block period")
	}
	clock.Advance(blockFor)
	if got := send(t, l, 1, "1.1.1.1", "abc"); got != 1 {
		t.Fatal("token must be released after the block period")
	}
}

func TestConcurrentRequestsNeverExceedLimit(t *testing.T) {
	l, err := limiter.New(memory.New(), limiter.Config{
		IPLimit: 10, DefaultTokenLimit: 10, BlockDuration: blockFor,
	})
	if err != nil {
		t.Fatal(err)
	}

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := l.Allow(context.Background(), "1.1.1.1", ""); err == nil && ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := allowed.Load(); got != 10 {
		t.Fatalf("allowed %d concurrent requests, want exactly 10", got)
	}
}

type failingStore struct{ err error }

func (s failingStore) Hit(context.Context, string, int, time.Duration, time.Duration) (bool, error) {
	return false, s.err
}

func TestStoreErrorIsPropagated(t *testing.T) {
	boom := errors.New("boom")
	l, err := limiter.New(failingStore{err: boom}, limiter.Config{
		IPLimit: 1, DefaultTokenLimit: 1, BlockDuration: blockFor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Allow(context.Background(), "1.1.1.1", ""); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	valid := limiter.Config{IPLimit: 1, DefaultTokenLimit: 1, BlockDuration: time.Second}
	tests := []struct {
		name   string
		store  limiter.Store
		mutate func(*limiter.Config)
	}{
		{"nil store", nil, func(*limiter.Config) {}},
		{"zero IP limit", memory.New(), func(c *limiter.Config) { c.IPLimit = 0 }},
		{"zero token default", memory.New(), func(c *limiter.Config) { c.DefaultTokenLimit = 0 }},
		{"zero block duration", memory.New(), func(c *limiter.Config) { c.BlockDuration = 0 }},
		{"negative window", memory.New(), func(c *limiter.Config) { c.Window = -time.Second }},
		{"invalid token limit", memory.New(), func(c *limiter.Config) { c.TokenLimits = map[string]int{"a": 0} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			if _, err := limiter.New(tt.store, cfg); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
