package middleware_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ratelimiter/internal/limiter"
	"ratelimiter/internal/middleware"
	"ratelimiter/internal/store/memory"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newServer(t *testing.T, cfg limiter.Config, opts middleware.Options) http.Handler {
	t.Helper()
	if cfg.BlockDuration == 0 {
		cfg.BlockDuration = 5 * time.Minute
	}
	l, err := limiter.New(memory.New(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	opts.Logger = quiet
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	return middleware.RateLimit(l, opts)(ok)
}

func do(h http.Handler, remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRejectsWith429AndExactMessage(t *testing.T) {
	h := newServer(t, limiter.Config{IPLimit: 2, DefaultTokenLimit: 100}, middleware.Options{})

	for i := 0; i < 2; i++ {
		if rec := do(h, "1.2.3.4:5000", nil); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i+1, rec.Code)
		}
	}

	rec := do(h, "1.2.3.4:5000", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", rec.Code)
	}
	const want = "you have reached the maximum number of requests or actions allowed within a certain time frame"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body %q, want exactly %q", got, want)
	}
}

func TestBlockedClientKeepsGetting429(t *testing.T) {
	h := newServer(t, limiter.Config{IPLimit: 1, DefaultTokenLimit: 100}, middleware.Options{})

	do(h, "1.2.3.4:5000", nil)
	for i := 0; i < 5; i++ {
		if rec := do(h, "1.2.3.4:5000", nil); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("blocked request %d: status %d, want 429", i+1, rec.Code)
		}
	}
}

func TestIPLimitIsPerIPIgnoringPort(t *testing.T) {
	h := newServer(t, limiter.Config{IPLimit: 1, DefaultTokenLimit: 100}, middleware.Options{})

	do(h, "1.2.3.4:5000", nil)
	if rec := do(h, "1.2.3.4:6000", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("same IP, other port: status %d, want 429", rec.Code)
	}
	if rec := do(h, "9.9.9.9:5000", nil); rec.Code != http.StatusOK {
		t.Fatalf("different IP: status %d, want 200", rec.Code)
	}
}

// Golden rule over HTTP: IP limit 10, token limit 100 → the token's 100 wins.
func TestTokenHeaderOverridesIPLimit(t *testing.T) {
	h := newServer(t, limiter.Config{
		IPLimit: 10, DefaultTokenLimit: 1, TokenLimits: map[string]int{"vip": 100},
	}, middleware.Options{})

	withToken := map[string]string{"API_KEY": "vip"}
	for i := 1; i <= 100; i++ {
		if rec := do(h, "1.2.3.4:5000", withToken); rec.Code != http.StatusOK {
			t.Fatalf("token request %d: status %d, want 200 (token limit is 100)", i, rec.Code)
		}
	}
	if rec := do(h, "1.2.3.4:5000", withToken); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("101st token request: status %d, want 429", rec.Code)
	}

	for i := 1; i <= 10; i++ {
		if rec := do(h, "5.6.7.8:5000", nil); rec.Code != http.StatusOK {
			t.Fatalf("IP request %d: status %d, want 200", i, rec.Code)
		}
	}
	if rec := do(h, "5.6.7.8:5000", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th IP request: status %d, want 429 (IP limit is 10)", rec.Code)
	}
}

func TestTokenHeaderIsTrimmedAndIgnoredWhenBlank(t *testing.T) {
	h := newServer(t, limiter.Config{
		IPLimit: 1, DefaultTokenLimit: 5, TokenLimits: map[string]int{"abc": 5},
	}, middleware.Options{})

	// A blank token must not count as a token: the IP limit (1) applies.
	do(h, "1.2.3.4:5000", map[string]string{"API_KEY": "   "})
	if rec := do(h, "1.2.3.4:5000", map[string]string{"API_KEY": "   "}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("blank token: status %d, want 429 (IP limit)", rec.Code)
	}

	// Surrounding spaces are trimmed, so " abc " is the token "abc".
	for i := 1; i <= 5; i++ {
		if rec := do(h, "9.9.9.9:1", map[string]string{"API_KEY": " abc "}); rec.Code != http.StatusOK {
			t.Fatalf("token request %d: status %d, want 200", i, rec.Code)
		}
	}
	if rec := do(h, "9.9.9.9:1", map[string]string{"API_KEY": "abc"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th token request: status %d, want 429", rec.Code)
	}
}

func TestForwardedForOnlyUsedWhenTrusted(t *testing.T) {
	cfg := limiter.Config{IPLimit: 1, DefaultTokenLimit: 100}

	t.Run("trusted", func(t *testing.T) {
		h := newServer(t, cfg, middleware.Options{TrustProxyHeaders: true})
		do(h, "10.0.0.1:1", map[string]string{"X-Forwarded-For": "203.0.113.7, 10.0.0.1"})
		if rec := do(h, "10.0.0.1:1", map[string]string{"X-Forwarded-For": "203.0.113.7"}); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("same forwarded client: status %d, want 429", rec.Code)
		}
		if rec := do(h, "10.0.0.1:1", map[string]string{"X-Forwarded-For": "198.51.100.9"}); rec.Code != http.StatusOK {
			t.Fatalf("other forwarded client: status %d, want 200", rec.Code)
		}
	})

	t.Run("untrusted", func(t *testing.T) {
		h := newServer(t, cfg, middleware.Options{})
		do(h, "10.0.0.1:1", map[string]string{"X-Forwarded-For": "203.0.113.7"})
		// A forged header must not give the client a fresh bucket.
		if rec := do(h, "10.0.0.1:1", map[string]string{"X-Forwarded-For": "198.51.100.9"}); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("forged header: status %d, want 429", rec.Code)
		}
	})
}

type failingAllower struct{}

func (failingAllower) Allow(context.Context, string, string) (bool, error) {
	return false, errors.New("redis down")
}

func TestLimiterFailureHandling(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("fail open lets the request through", func(t *testing.T) {
		h := middleware.RateLimit(failingAllower{}, middleware.Options{FailOpen: true, Logger: quiet})(next)
		if rec := do(h, "1.2.3.4:1", nil); rec.Code != http.StatusOK {
			t.Fatalf("status %d, want 200", rec.Code)
		}
	})

	t.Run("fail closed answers 503", func(t *testing.T) {
		h := middleware.RateLimit(failingAllower{}, middleware.Options{FailOpen: false, Logger: quiet})(next)
		if rec := do(h, "1.2.3.4:1", nil); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status %d, want 503", rec.Code)
		}
	})
}
