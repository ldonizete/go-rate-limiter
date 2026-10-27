// Package redisstore provides a Redis-backed limiter.Store.
package redisstore

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"ratelimiter/internal/limiter"
)

const keyPrefix = "ratelimit:"

var _ limiter.Store = (*Store)(nil)

// hitScript runs the whole decision in one atomic Redis round trip.
//
//	KEYS[1] counter key (expires with the window)
//	KEYS[2] block key   (expires when the block ends)
//	ARGV[1] limit, ARGV[2] window in ms, ARGV[3] block duration in ms
//
// Returns 1 when the hit is allowed and 0 otherwise.
var hitScript = goredis.NewScript(`
if redis.call('EXISTS', KEYS[2]) == 1 then
  return 0
end
local count = redis.call('INCR', KEYS[1])
if count == 1 or redis.call('PTTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
if count > tonumber(ARGV[1]) then
  redis.call('SET', KEYS[2], '1', 'PX', ARGV[3])
  return 0
end
return 1
`)

// Store keeps counters and blocks in Redis.
type Store struct {
	client goredis.Scripter
}

// New returns a Store that talks to Redis through client.
func New(client goredis.Scripter) *Store {
	return &Store{client: client}
}

// Hit implements limiter.Store.
func (s *Store) Hit(ctx context.Context, key string, limit int, window, blockFor time.Duration) (bool, error) {
	// The hash tag keeps both keys in the same slot on Redis Cluster.
	tag := keyPrefix + "{" + key + "}"
	keys := []string{tag + ":hit", tag + ":block"}

	allowed, err := hitScript.Run(ctx, s.client, keys, limit, millis(window), millis(blockFor)).Int64()
	if err != nil {
		return false, fmt.Errorf("redisstore: run hit script: %w", err)
	}
	return allowed == 1, nil
}

func millis(d time.Duration) int64 {
	if ms := d.Milliseconds(); ms > 0 {
		return ms
	}
	return 1
}
