package redisstore_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"ratelimiter/internal/limiter"
	"ratelimiter/internal/store/redisstore"
)

// These tests need a real Redis. They are skipped unless REDIS_ADDR is set
// (docker compose sets it for the "tests" service).
func newStore(t *testing.T) *redisstore.Store {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR not set; skipping Redis integration test")
	}
	client := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("cannot reach Redis at %s: %v", addr, err)
	}
	return redisstore.New(client)
}

// uniqueKey avoids collisions between tests and between repeated runs.
func uniqueKey(t *testing.T, base string) string {
	t.Helper()
	return fmt.Sprintf("%s:%s:%d", base, t.Name(), time.Now().UnixNano())
}

func hit(t *testing.T, s *redisstore.Store, key string, limit int, window, blockFor time.Duration) bool {
	t.Helper()
	ok, err := s.Hit(context.Background(), key, limit, window, blockFor)
	if err != nil {
		t.Fatalf("Hit: %v", err)
	}
	return ok
}

func TestHitAllowsUpToLimitThenBlocks(t *testing.T) {
	s := newStore(t)
	key := uniqueKey(t, "ip")

	for i := 1; i <= 5; i++ {
		if !hit(t, s, key, 5, time.Second, time.Minute) {
			t.Fatalf("hit %d must be allowed", i)
		}
	}
	if hit(t, s, key, 5, time.Second, time.Minute) {
		t.Fatal("6th hit must be rejected")
	}
}

func TestWindowResetsWhenLimitIsNotExceeded(t *testing.T) {
	s := newStore(t)
	key := uniqueKey(t, "ip")

	for i := 0; i < 2; i++ {
		if !hit(t, s, key, 2, time.Second, time.Minute) {
			t.Fatalf("hit %d must be allowed", i+1)
		}
	}
	time.Sleep(1100 * time.Millisecond)
	for i := 0; i < 2; i++ {
		if !hit(t, s, key, 2, time.Second, time.Minute) {
			t.Fatalf("hit %d in the next window must be allowed", i+1)
		}
	}
}

func TestBlockPersistsPastWindowAndThenExpires(t *testing.T) {
	s := newStore(t)
	key := uniqueKey(t, "token")
	const blockFor = 2 * time.Second

	hit(t, s, key, 1, time.Second, blockFor)
	if hit(t, s, key, 1, time.Second, blockFor) {
		t.Fatal("2nd hit must be rejected and start the block")
	}

	time.Sleep(1200 * time.Millisecond) // the 1s window is over, the block is not
	if hit(t, s, key, 1, time.Second, blockFor) {
		t.Fatal("key must remain blocked after the window ends")
	}

	time.Sleep(1200 * time.Millisecond) // block (2s) is over
	if !hit(t, s, key, 1, time.Second, blockFor) {
		t.Fatal("key must be released after the block expires")
	}
}

func TestBlockedHitsDoNotExtendTheBlock(t *testing.T) {
	s := newStore(t)
	key := uniqueKey(t, "ip")
	const blockFor = 1500 * time.Millisecond

	hit(t, s, key, 1, time.Second, blockFor)
	hit(t, s, key, 1, time.Second, blockFor) // starts the block
	time.Sleep(700 * time.Millisecond)
	hit(t, s, key, 1, time.Second, blockFor) // rejected, must not renew the block
	time.Sleep(900 * time.Millisecond)

	if !hit(t, s, key, 1, time.Second, blockFor) {
		t.Fatal("block must end blockFor after it started")
	}
}

func TestConcurrentHitsAreAtomic(t *testing.T) {
	s := newStore(t)
	key := uniqueKey(t, "ip")

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.Hit(context.Background(), key, 10, 5*time.Second, time.Minute)
			if err == nil && ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := allowed.Load(); got != 10 {
		t.Fatalf("allowed %d concurrent hits, want exactly 10", got)
	}
}

// Token > IP, end to end against Redis through the Limiter.
func TestTokenOverridesIPLimitWithRedis(t *testing.T) {
	s := newStore(t)
	ip := uniqueKey(t, "10.0.0.1")
	vip := uniqueKey(t, "vip-token")

	l, err := limiter.New(s, limiter.Config{
		IPLimit:           10,
		DefaultTokenLimit: 1,
		TokenLimits:       map[string]int{vip: 100},
		Window:            5 * time.Second, // wide window: no flakiness from a rollover mid-test
		BlockDuration:     time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	count := func(n int, token string) (allowed int) {
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

	if got := count(100, vip); got != 100 {
		t.Fatalf("with token (limit 100) allowed %d of 100, want 100", got)
	}
	if got := count(1, vip); got != 0 {
		t.Fatal("101st request with the token must be rejected")
	}
	if got := count(20, ""); got != 10 {
		t.Fatalf("same IP without token allowed %d, want 10 (IP limit)", got)
	}
}
