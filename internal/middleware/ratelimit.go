// Package middleware adapts the limiter to net/http. It only deals with HTTP:
// extracting the client identity and writing the rejection response.
package middleware

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

const (
	// TokenHeader is the request header that carries the access token.
	TokenHeader = "API_KEY"

	// TooManyRequestsMessage is the exact body sent when a client is limited.
	TooManyRequestsMessage = "you have reached the maximum number of requests or actions allowed within a certain time frame"
)

// Allower decides whether a request may proceed. *limiter.Limiter implements it.
type Allower interface {
	Allow(ctx context.Context, ip, token string) (bool, error)
}

// Options tunes the middleware behavior.
type Options struct {
	// FailOpen lets requests through when the limiter fails (e.g. Redis is
	// down). When false, such requests receive 503.
	FailOpen bool
	// TrustProxyHeaders makes the client IP come from X-Forwarded-For. Enable
	// it only behind a proxy that sets the header; otherwise clients can forge it.
	TrustProxyHeaders bool
	// Logger receives limiter failures. Defaults to slog.Default().
	Logger *slog.Logger
}

// RateLimit returns a middleware that rejects requests with 429 when the
// limiter denies them.
func RateLimit(a Allower, opts Options) func(http.Handler) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r, opts.TrustProxyHeaders)
			token := strings.TrimSpace(r.Header.Get(TokenHeader))

			allowed, err := a.Allow(r.Context(), ip, token)
			if err != nil {
				logger.Error("rate limiter failure", "err", err, "fail_open", opts.FailOpen)
				if !opts.FailOpen {
					http.Error(w, "service unavailable", http.StatusServiceUnavailable)
					return
				}
				allowed = true
			}
			if !allowed {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, TooManyRequestsMessage)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request, trustProxyHeaders bool) string {
	if trustProxyHeaders {
		first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
		if first = strings.TrimSpace(first); first != "" {
			return normalizeIP(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return normalizeIP(host)
}

// normalizeIP gives equivalent IPv6 spellings the same key.
func normalizeIP(s string) string {
	if ip := net.ParseIP(s); ip != nil {
		return ip.String()
	}
	return s
}
