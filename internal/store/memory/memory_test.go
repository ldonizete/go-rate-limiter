package memory

import (
	"context"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func hit(t *testing.T, s *Store, key string, limit int) bool {
	t.Helper()
	ok, err := s.Hit(context.Background(), key, limit, time.Second, 10*time.Second)
	if err != nil {
		t.Fatalf("Hit: %v", err)
	}
	return ok
}

func TestHitBlocksAfterLimitAndReleasesAfterBlockFor(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	s := NewWithClock(c.now)

	for i := 0; i < 2; i++ {
		if !hit(t, s, "k", 2) {
			t.Fatalf("hit %d must be allowed", i+1)
		}
	}
	if hit(t, s, "k", 2) {
		t.Fatal("3rd hit must be rejected")
	}

	c.advance(9 * time.Second)
	if hit(t, s, "k", 2) {
		t.Fatal("key must stay blocked for blockFor")
	}
	c.advance(2 * time.Second)
	if !hit(t, s, "k", 2) {
		t.Fatal("key must be released after blockFor")
	}
}

func TestSweepRemovesIdleEntries(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	s := NewWithClock(c.now)

	hit(t, s, "idle", 5)
	c.advance(2 * sweepInterval)
	hit(t, s, "fresh", 5) // triggers the sweep

	if _, ok := s.entries["idle"]; ok {
		t.Fatal("idle entry should have been swept")
	}
	if _, ok := s.entries["fresh"]; !ok {
		t.Fatal("fresh entry must be kept")
	}
}

func TestSweepKeepsBlockedEntries(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	s := NewWithClock(c.now)

	long := 10 * sweepInterval
	for i := 0; i < 2; i++ {
		if _, err := s.Hit(context.Background(), "blocked", 1, time.Second, long); err != nil {
			t.Fatal(err)
		}
	}
	c.advance(2 * sweepInterval)
	hit(t, s, "other", 5) // triggers the sweep

	if _, ok := s.entries["blocked"]; !ok {
		t.Fatal("blocked entry must survive the sweep")
	}
}
