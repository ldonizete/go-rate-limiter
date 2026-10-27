package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"ratelimiter/internal/config"
	"ratelimiter/internal/limiter"
	"ratelimiter/internal/middleware"
	"ratelimiter/internal/store/memory"
	"ratelimiter/internal/store/redisstore"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, closeStore, err := newStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStore()

	lim, err := limiter.New(store, limiter.Config{
		IPLimit:           cfg.IPLimit,
		DefaultTokenLimit: cfg.DefaultTokenLimit,
		TokenLimits:       cfg.TokenLimits,
		Window:            time.Second,
		BlockDuration:     cfg.BlockDuration,
	})
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, `{"message":"ok"}`)
	})

	handler := middleware.RateLimit(lim, middleware.Options{
		FailOpen:          cfg.FailOpen,
		TrustProxyHeaders: cfg.TrustProxyHeaders,
	})(mux)

	srv := &http.Server{
		Addr:              ":" + cfg.ServerPort,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr, "store", cfg.StoreDriver)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// newStore is the single place that maps STORE_DRIVER to a persistence
// strategy. To add a new backend, implement limiter.Store and add a case here.
func newStore(ctx context.Context, cfg config.Config) (limiter.Store, func(), error) {
	switch cfg.StoreDriver {
	case "redis":
		client := goredis.NewClient(&goredis.Options{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
		})
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := client.Ping(pingCtx).Err(); err != nil {
			_ = client.Close()
			return nil, nil, fmt.Errorf("connect to redis at %s: %w", cfg.Redis.Addr, err)
		}
		return redisstore.New(client), func() { _ = client.Close() }, nil
	case "memory":
		return memory.New(), func() {}, nil
	default:
		return nil, nil, fmt.Errorf("unknown STORE_DRIVER %q (supported: redis, memory)", cfg.StoreDriver)
	}
}
