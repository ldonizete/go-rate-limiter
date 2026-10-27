// Package memory provides an in-process limiter.Store. It is useful for tests
// and as a reference implementation of the persistence Strategy.
package memory

import (
	"context"
	"sync"
	"time"

	"ratelimiter/internal/limiter"
)

const sweepInterval = time.Minute

var _ limiter.Store = (*Store)(nil)

type entry struct {
	count        int64
	windowEnd    time.Time
	blockedUntil time.Time
}

// Store keeps counters and blocks in memory.
type Store struct {
	mu        sync.Mutex
	entries   map[string]*entry
	now       func() time.Time
	lastSweep time.Time
}

// New returns a Store using the wall clock.
func New() *Store {
	return NewWithClock(time.Now)
}

// NewWithClock returns a Store reading time from now, which lets tests control
// the passage of time.
func NewWithClock(now func() time.Time) *Store {
	return &Store{entries: make(map[string]*entry), now: now, lastSweep: now()}
}

// Hit implements limiter.Store.
func (s *Store) Hit(_ context.Context, key string, limit int, window, blockFor time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.sweep(now)

	e, ok := s.entries[key]
	if !ok {
		e = &entry{}
		s.entries[key] = e
	}
	if now.Before(e.blockedUntil) {
		return false, nil
	}
	if !now.Before(e.windowEnd) {
		e.count = 0
		e.windowEnd = now.Add(window)
	}
	e.count++
	if e.count > int64(limit) {
		e.blockedUntil = now.Add(blockFor)
		return false, nil
	}
	return true, nil
}

// sweep drops entries that are neither counting nor blocked. Callers hold s.mu.
func (s *Store) sweep(now time.Time) {
	if now.Sub(s.lastSweep) < sweepInterval {
		return
	}
	s.lastSweep = now
	for key, e := range s.entries {
		if !now.Before(e.windowEnd) && !now.Before(e.blockedUntil) {
			delete(s.entries, key)
		}
	}
}
