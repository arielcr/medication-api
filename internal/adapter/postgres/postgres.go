// Package postgres implements medication.Repository on PostgreSQL using
// pgx, the most widely used native PostgreSQL driver for Go.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/arielcr/medication-api/migrations"
)

// connectTimeout bounds the initial connectivity check, so a wrong
// DATABASE_URL fails startup quickly instead of hanging.
const connectTimeout = 5 * time.Second

// Config holds the connection settings.
type Config struct {
	URL      string
	MaxConns int32
}

// Connect creates a connection pool, instruments it with OpenTelemetry
// (a span per query and pool metrics) and verifies the database is
// reachable.
//
// A pool keeps a few connections open and lends them to concurrent
// requests; opening a new TCP + TLS + auth connection per query would be
// far too slow.
func Connect(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, errors.New("parse DATABASE_URL: invalid connection string") // never echo the URL: it holds the password
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	poolCfg.ConnConfig.Tracer = otelpgx.NewTracer()

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if err := otelpgx.RecordStats(pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("record pool metrics: %w", err)
	}

	return pool, nil
}

// Migrate applies every pending migration embedded in the migrations
// package.
//
// A PostgreSQL advisory lock (goose session locker) guarantees that when
// several replicas start at the same time, only one runs the migrations
// while the others wait.
func Migrate(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	// goose works with database/sql; stdlib exposes the pgx pool as one.
	// Closing this *sql.DB does not close the underlying pool.
	db := stdlib.OpenDBFromPool(pool)
	defer func() {
		if err := db.Close(); err != nil {
			logger.WarnContext(ctx, "close migration database handle", slog.Any("error", err))
		}
	}()

	// Retry taking the lock every second for up to a minute.
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 60))
	if err != nil {
		return fmt.Errorf("create migration lock: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS,
		goose.WithSessionLocker(locker),
	)
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	for _, r := range results {
		logger.InfoContext(ctx, "migration applied",
			slog.String("file", r.Source.Path),
			slog.Int64("migration_version", r.Source.Version),
			slog.String("duration", r.Duration.String()),
		)
	}

	return nil
}
