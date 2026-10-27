// Package config loads the application settings from environment variables,
// optionally populated from a .env file in the working directory.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Redis holds the Redis connection settings.
type Redis struct {
	Addr     string
	Password string
	DB       int
}

// Config holds every setting of the application.
type Config struct {
	ServerPort        string
	StoreDriver       string
	IPLimit           int
	DefaultTokenLimit int
	TokenLimits       map[string]int
	BlockDuration     time.Duration
	FailOpen          bool
	TrustProxyHeaders bool
	Redis             Redis
}

// Load reads a .env file (if present; real environment variables win) and then
// the environment.
func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Config{}, fmt.Errorf("config: read .env: %w", err)
	}
	return LoadFrom(os.Getenv)
}

// LoadFrom builds a Config using getenv, applying defaults and validating.
func LoadFrom(getenv func(string) string) (Config, error) {
	p := parser{getenv: getenv}

	cfg := Config{
		ServerPort:        p.str("SERVER_PORT", "8080"),
		StoreDriver:       strings.ToLower(p.str("STORE_DRIVER", "redis")),
		IPLimit:           p.positiveInt("RATE_LIMIT_IP", 10),
		DefaultTokenLimit: p.positiveInt("RATE_LIMIT_TOKEN_DEFAULT", 100),
		BlockDuration:     p.positiveDuration("BLOCK_DURATION", 5*time.Minute),
		FailOpen:          p.boolean("FAIL_OPEN", true),
		TrustProxyHeaders: p.boolean("TRUST_PROXY_HEADERS", false),
		Redis: Redis{
			Addr:     p.str("REDIS_ADDR", "localhost:6379"),
			Password: getenv("REDIS_PASSWORD"),
			DB:       p.nonNegativeInt("REDIS_DB", 0),
		},
	}

	limits, err := ParseTokenLimits(getenv("TOKEN_LIMITS"))
	if err != nil {
		p.errs = append(p.errs, err)
	}
	cfg.TokenLimits = limits

	if err := errors.Join(p.errs...); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// ParseTokenLimits parses "token1:100,token2:50" into a map. An empty string
// yields an empty map.
func ParseTokenLimits(raw string) (map[string]int, error) {
	limits := make(map[string]int)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		// Split on the last colon so tokens may themselves contain colons.
		i := strings.LastIndex(item, ":")
		if i < 0 {
			return nil, fmt.Errorf("TOKEN_LIMITS: %q must look like token:limit", item)
		}
		token, rawLimit := strings.TrimSpace(item[:i]), strings.TrimSpace(item[i+1:])
		if token == "" {
			return nil, fmt.Errorf("TOKEN_LIMITS: %q has an empty token", item)
		}
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit <= 0 {
			return nil, fmt.Errorf("TOKEN_LIMITS: limit for %q must be a positive integer, got %q", token, rawLimit)
		}
		if _, dup := limits[token]; dup {
			return nil, fmt.Errorf("TOKEN_LIMITS: token %q is listed more than once", token)
		}
		limits[token] = limit
	}
	return limits, nil
}

// parser reads typed values and accumulates every error found.
type parser struct {
	getenv func(string) string
	errs   []error
}

func (p *parser) str(key, def string) string {
	if v := strings.TrimSpace(p.getenv(key)); v != "" {
		return v
	}
	return def
}

func (p *parser) positiveInt(key string, def int) int {
	return p.intAtLeast(key, def, 1)
}

func (p *parser) nonNegativeInt(key string, def int) int {
	return p.intAtLeast(key, def, 0)
}

func (p *parser) intAtLeast(key string, def, min int) int {
	raw := strings.TrimSpace(p.getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < min {
		p.errs = append(p.errs, fmt.Errorf("%s must be an integer >= %d, got %q", key, min, raw))
		return def
	}
	return v
}

func (p *parser) positiveDuration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(p.getenv(key))
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v <= 0 {
		p.errs = append(p.errs, fmt.Errorf("%s must be a positive duration like 5m or 30s, got %q", key, raw))
		return def
	}
	return v
}

func (p *parser) boolean(key string, def bool) bool {
	raw := strings.TrimSpace(p.getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s must be a boolean, got %q", key, raw))
		return def
	}
	return v
}
