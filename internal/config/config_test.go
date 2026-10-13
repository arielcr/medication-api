package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
)

// This is an internal test (package config, not config_test) because it
// exercises the unexported parse function with a fake environment.

func TestParse_Defaults(t *testing.T) {
	t.Parallel()

	cfg, err := parse(env.Options{Environment: map[string]string{}})
	if err != nil {
		t.Fatalf("parse() error = %v", err)
	}

	if cfg.HTTPAddr != ":8080" || cfg.LogLevel != slog.LevelInfo || cfg.LogFormat != "json" ||
		cfg.DatabaseURL != "" || cfg.DBMaxConns != 10 || !cfg.DBMigrate ||
		cfg.ShutdownTimeout != 20*time.Second || cfg.HTTPReadHeaderTimeout != 5*time.Second {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestParse_FromEnvironment(t *testing.T) {
	t.Parallel()

	cfg, err := parse(env.Options{Environment: map[string]string{
		"HTTP_ADDR":        ":9090",
		"LOG_LEVEL":        "debug",
		"LOG_FORMAT":       "text",
		"DATABASE_URL":     "postgres://localhost:5432/db",
		"DB_MAX_CONNS":     "25",
		"DB_MIGRATE":       "false",
		"SHUTDOWN_TIMEOUT": "30s",
	}})
	if err != nil {
		t.Fatalf("parse() error = %v", err)
	}

	if cfg.HTTPAddr != ":9090" || cfg.LogLevel != slog.LevelDebug || cfg.LogFormat != "text" ||
		cfg.DBMaxConns != 25 || cfg.DBMigrate || cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("environment not applied: %+v", cfg)
	}
}

func TestParse_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"bad duration", map[string]string{"SHUTDOWN_TIMEOUT": "soon"}, "ShutdownTimeout"},
		{"bad log level", map[string]string{"LOG_LEVEL": "loud"}, "LogLevel"},
		{"bad log format", map[string]string{"LOG_FORMAT": "xml"}, "LOG_FORMAT"},
		{"zero pool", map[string]string{"DB_MAX_CONNS": "0"}, "DB_MAX_CONNS"},
		{"zero timeout", map[string]string{"HTTP_READ_TIMEOUT": "0s"}, "HTTP_READ_TIMEOUT"},
		{"negative drain", map[string]string{"SHUTDOWN_DRAIN_DELAY": "-1s"}, "SHUTDOWN_DRAIN_DELAY"},
		{"zero request timeout", map[string]string{"HTTP_REQUEST_TIMEOUT": "0s"}, "HTTP_REQUEST_TIMEOUT"},
		{"database DSN instead of URL", map[string]string{"DATABASE_URL": "host=db user=app password=s3cr3t"}, "DATABASE_URL"},
		{"database URL with another scheme", map[string]string{"DATABASE_URL": "mysql://db/meds"}, "DATABASE_URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := parse(env.Options{Environment: tt.env})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("parse() error = %v, want it to mention %s", err, tt.wantErr)
			}
		})
	}
}

func TestConfig_LogValueHidesCredentials(t *testing.T) {
	t.Parallel()

	tests := []string{
		"postgres://app:s3cr3t@db:5432/medications",
		"postgres://app@db:5432/medications?sslmode=require&password=s3cr3t",
	}

	for _, dsn := range tests {
		cfg := Config{DatabaseURL: dsn} //nolint:gosec // fake credentials: the test checks they are hidden

		var buf bytes.Buffer
		slog.New(slog.NewJSONHandler(&buf, nil)).Info("config", slog.Any("config", cfg))

		if strings.Contains(buf.String(), "s3cr3t") {
			t.Errorf("log contains the password: %s", buf.String())
		}
		if !strings.Contains(buf.String(), "postgres://db:5432/medications") {
			t.Errorf("log should still show the host and database: %s", buf.String())
		}
	}
}

func TestParse_ErrorDoesNotLeakDatabaseURL(t *testing.T) {
	t.Parallel()

	_, err := parse(env.Options{Environment: map[string]string{"DATABASE_URL": "host=db password=s3cr3t"}})
	if err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("parse() error = %v, want an error without the password", err)
	}
}
