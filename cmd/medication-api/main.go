// Command medication-api runs the Medication REST API server.
//
// main is the composition root: the only place that knows every concrete
// type. It reads the configuration, builds the dependencies (logger,
// telemetry, database, repository, service, HTTP handler), wires them
// together by passing them as arguments (manual dependency injection),
// and runs the HTTP server until it receives a shutdown signal.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/arielcr/medication-api/internal/adapter/httpapi"
	"github.com/arielcr/medication-api/internal/adapter/memory"
	"github.com/arielcr/medication-api/internal/adapter/postgres"
	"github.com/arielcr/medication-api/internal/config"
	"github.com/arielcr/medication-api/internal/medication"
	"github.com/arielcr/medication-api/internal/telemetry"
)

const serviceName = "medication-api"

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

// telemetryFlushTimeout bounds how long shutdown waits to send the last
// spans and metrics.
const telemetryFlushTimeout = 5 * time.Second

var errShuttingDown = errors.New("server is shutting down")

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", serviceName, err)
		os.Exit(1)
	}
}

// run contains the whole program. Keeping it out of main means every
// deferred cleanup runs before os.Exit (which skips defers).
func run() error {
	// ctx is canceled on SIGINT (Ctrl+C) or SIGTERM (docker stop, ECS,
	// Kubernetes), which starts the graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := telemetry.NewLogger(os.Stdout, cfg.LogLevel, cfg.LogFormat).
		With(slog.String("service", serviceName), slog.String("service_version", version))
	slog.SetDefault(logger) // libraries that use the default logger get the same format

	logger.InfoContext(ctx, "starting", slog.Any("config", cfg)) // Config.LogValue hides the password

	shutdownTelemetry, err := telemetry.Setup(ctx, telemetry.Config{
		ServiceName:    serviceName,
		ServiceVersion: version,
		Environment:    cfg.Env,
		Enabled:        cfg.OTLPEndpoint != "",
	})
	if err != nil {
		return fmt.Errorf("set up telemetry: %w", err)
	}
	defer func() {
		// The signal context is already canceled at this point, so flush
		// with a fresh, bounded context.
		flushCtx, cancel := context.WithTimeout(context.Background(), telemetryFlushTimeout)
		defer cancel()
		if err := shutdownTelemetry(flushCtx); err != nil {
			logger.Error("flush telemetry", slog.Any("error", err))
		}
	}()

	repo, dbReady, closeRepo, err := newRepository(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer closeRepo()

	// shuttingDown flips to true when a signal arrives, so /readyz starts
	// failing and load balancers stop sending new requests.
	var shuttingDown atomic.Bool

	handler := httpapi.NewHandler(httpapi.Options{
		Service:        medication.NewService(repo),
		Logger:         logger,
		RequestTimeout: cfg.HTTPRequestTimeout,
		Ready: func(ctx context.Context) error {
			if shuttingDown.Load() {
				return errShuttingDown
			}
			return dbReady(ctx)
		},
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout, // Slowloris protection
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	return serve(ctx, stop, srv, &shuttingDown, cfg, logger)
}

// newRepository picks the storage: PostgreSQL when DATABASE_URL is set,
// otherwise the in-memory repository. It also returns a readiness check
// and a cleanup function.
func newRepository(ctx context.Context, cfg config.Config, logger *slog.Logger) (
	repo medication.Repository, ready httpapi.ReadinessCheck, closeFn func(), err error,
) {
	if cfg.DatabaseURL == "" {
		logger.WarnContext(ctx, "DATABASE_URL is not set: using in-memory storage; data is lost on restart")
		return memory.NewRepository(), func(context.Context) error { return nil }, func() {}, nil
	}

	pool, err := postgres.Connect(ctx, postgres.Config{URL: cfg.DatabaseURL, MaxConns: cfg.DBMaxConns})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("connect to database: %w", err)
	}

	if cfg.DBMigrate {
		if err := postgres.Migrate(ctx, pool, logger); err != nil {
			pool.Close()
			return nil, nil, nil, fmt.Errorf("migrate database: %w", err)
		}
	}

	return postgres.NewRepository(pool), pool.Ping, pool.Close, nil
}

// serve runs srv until ctx is canceled, then shuts it down gracefully:
// stop accepting new connections and let in-flight requests finish (up to
// ShutdownTimeout) instead of cutting them off.
func serve(
	ctx context.Context,
	stopSignals context.CancelFunc,
	srv *http.Server,
	shuttingDown *atomic.Bool,
	cfg config.Config,
	logger *slog.Logger,
) error {
	// ListenAndServe blocks, so it runs in its own goroutine and reports
	// back through a channel. The buffer of 1 lets the goroutine exit even
	// if nobody is receiving anymore.
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", slog.String("addr", srv.Addr))
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		// The server failed before any signal (e.g. the port is in use).
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	// Restore default signal handling: a second Ctrl+C kills the process
	// immediately if the graceful shutdown hangs.
	stopSignals()

	logger.Info("shutdown signal received, draining", slog.String("drain_delay", cfg.ShutdownDrainDelay.String()))
	shuttingDown.Store(true)

	// Keep serving for a moment while /readyz fails, so load balancers
	// notice and stop routing new requests here before we close.
	time.Sleep(cfg.ShutdownDrainDelay)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Some requests did not finish in time: close their connections
		// so the process (and the deferred cleanups) can still exit.
		_ = srv.Close()
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	// Shutdown makes ListenAndServe return http.ErrServerClosed right away.
	if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}

	logger.Info("server stopped gracefully")

	return nil
}
