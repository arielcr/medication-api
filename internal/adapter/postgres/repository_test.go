package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arielcr/medication-api/internal/adapter/postgres"
	"github.com/arielcr/medication-api/internal/medication"
	"github.com/arielcr/medication-api/internal/medication/medicationtest"
)

// Integration tests: they need a real PostgreSQL, given in
// TEST_DATABASE_URL (see `make test-integration`). Without it they are
// skipped, so `go test ./...` still works on any machine.
//
// Each subtest gets its own freshly migrated schema, so tests are
// isolated from each other and can run in parallel against one database.

func TestRepositoryContract(t *testing.T) {
	t.Parallel()

	url := databaseURL(t)

	medicationtest.RunRepositoryContract(t, func(t *testing.T) medication.Repository {
		return postgres.NewRepository(newIsolatedPool(t, url))
	})
}

func TestMigrate_IsIdempotent(t *testing.T) {
	t.Parallel()

	pool := newIsolatedPool(t, databaseURL(t)) // already migrated once

	if err := postgres.Migrate(t.Context(), pool, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
}

func TestConnect_InvalidURLDoesNotLeakPassword(t *testing.T) {
	t.Parallel()

	const url = "postgres://user:s3cr3t@host:notaport/db" //nolint:gosec // fake credentials: the test checks they are not leaked
	_, err := postgres.Connect(t.Context(), postgres.Config{URL: url})
	if err == nil {
		t.Fatal("Connect() with an invalid URL returned nil error")
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("error leaks the password: %v", err)
	}
}

func databaseURL(t *testing.T) string {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL integration tests")
	}

	return url
}

// newIsolatedPool creates a uniquely named schema, returns a pool whose
// search_path points to it (so every unqualified table lives there),
// runs the migrations, and drops the schema when the test ends.
func newIsolatedPool(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	ctx := t.Context()

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Cleanup functions run after t.Context() is canceled, so they use
	// context.Background().
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	schema := "test_" + hex.EncodeToString(suffix)

	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := postgres.Migrate(ctx, pool, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	return pool
}
