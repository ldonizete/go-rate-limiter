// Package limiter holds the rate limiting business rules. It knows nothing
// about HTTP or about any specific storage backend.
package limiter

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Config defines the limits applied by a Limiter.
type Config struct {
	// IPLimit is the maximum number of requests per Window for a single IP.
	IPLimit int
	// DefaultTokenLimit is the limit for tokens not listed in TokenLimits.
	DefaultTokenLimit int
	// TokenLimits maps a token to its specific limit per Window.
	TokenLimits map[string]int
	// Window is the counting window. Defaults to one second.
	Window time.Duration
	// BlockDuration is how long a key stays blocked after exceeding its limit.
	BlockDuration time.Duration
}

// Limiter decides whether a request may proceed.
type Limiter struct {
	store Store
	cfg   Config
}

// New validates cfg and returns a Limiter backed by store.
func New(store Store, cfg Config) (*Limiter, error) {
	if store == nil {
		return nil, errors.New("limiter: store is required")
	}
	if cfg.Window == 0 {
		cfg.Window = time.Second
	}
	switch {
	case cfg.Window < 0:
		return nil, errors.New("limiter: window must be positive")
	case cfg.IPLimit <= 0:
		return nil, errors.New("limiter: IP limit must be positive")
	case cfg.DefaultTokenLimit <= 0:
		return nil, errors.New("limiter: default token limit must be positive")
	case cfg.BlockDuration <= 0:
		return nil, errors.New("limiter: block duration must be positive")
	}
	tokens := make(map[string]int, len(cfg.TokenLimits))
	for token, limit := range cfg.TokenLimits {
		if limit <= 0 {
			return nil, fmt.Errorf("limiter: limit for token %q must be positive", token)
		}
		tokens[token] = limit
	}
	cfg.TokenLimits = tokens
	return &Limiter{store: store, cfg: cfg}, nil
}

// Allow reports whether a request from ip carrying token (empty when absent)
// may proceed.
//
// Precedence rule: when a token is present, only the token's limit is
// evaluated and the IP limit is ignored, so a token may exceed the IP limit
// (or be stricter than it). Without a token, the IP limit applies.
func (l *Limiter) Allow(ctx context.Context, ip, token string) (bool, error) {
	key, limit := l.rule(ip, token)
	allowed, err := l.store.Hit(ctx, key, limit, l.cfg.Window, l.cfg.BlockDuration)
	if err != nil {
		return false, fmt.Errorf("limiter: hit %q: %w", key, err)
	}
	return allowed, nil
}

func (l *Limiter) rule(ip, token string) (key string, limit int) {
	if token != "" {
		if limit, ok := l.cfg.TokenLimits[token]; ok {
			return "token:" + token, limit
		}
		return "token:" + token, l.cfg.DefaultTokenLimit
	}
	return "ip:" + ip, l.cfg.IPLimit
}
