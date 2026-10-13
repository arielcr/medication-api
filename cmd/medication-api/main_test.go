package main

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arielcr/medication-api/internal/config"
)

func TestServe_ShutsDownGracefullyOnSignal(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	srv := &http.Server{Addr: "127.0.0.1:0", Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	var shuttingDown atomic.Bool
	cfg := config.Config{ShutdownTimeout: time.Second}

	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, cancel, srv, &shuttingDown, cfg, slog.New(slog.DiscardHandler))
	}()

	cancel() // simulates SIGTERM

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve() error = %v, want nil after a graceful shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve() did not return after the context was canceled")
	}

	if !shuttingDown.Load() {
		t.Error("shuttingDown was not set, so /readyz would keep reporting ready")
	}
}

func TestServe_ReturnsListenErrors(t *testing.T) {
	t.Parallel()

	srv := &http.Server{Addr: "127.0.0.1:-1", Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	var shuttingDown atomic.Bool

	err := serve(t.Context(), func() {}, srv, &shuttingDown, config.Config{ShutdownTimeout: time.Second}, slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("serve() with an invalid address returned nil")
	}
}

func TestNewRepository_InMemoryWithoutDatabaseURL(t *testing.T) {
	t.Parallel()

	repo, ready, closeFn, err := newRepository(t.Context(), config.Config{}, slog.New(slog.DiscardHandler))
	if err != nil || repo == nil {
		t.Fatalf("newRepository() = %v, %v", repo, err)
	}
	defer closeFn()

	if err := ready(t.Context()); err != nil {
		t.Errorf("in-memory readiness = %v, want nil", err)
	}
}
