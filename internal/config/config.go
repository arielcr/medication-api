// Package config loads the service configuration from environment
// variables, following the twelve-factor app methodology: the same build
// runs in every environment, and only the environment changes.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config holds every setting of the service. The struct tags tell the
// env library which variable to read and the default to use.
type Config struct {
	// Env is the deployment environment (development, staging,
	// production). It is attached to logs and traces.
	Env string `env:"APP_ENV" envDefault:"development"`

	// HTTPAddr is the address the HTTP server listens on.
	HTTPAddr string `env:"HTTP_ADDR" envDefault:":8080"`

	// HTTP server timeouts. Without them, slow or malicious clients can
	// hold connections (and memory) forever.
	HTTPReadHeaderTimeout time.Duration `env:"HTTP_READ_HEADER_TIMEOUT" envDefault:"5s"`
	HTTPReadTimeout       time.Duration `env:"HTTP_READ_TIMEOUT" envDefault:"10s"`
	HTTPWriteTimeout      time.Duration `env:"HTTP_WRITE_TIMEOUT" envDefault:"15s"`
	HTTPIdleTimeout       time.Duration `env:"HTTP_IDLE_TIMEOUT" envDefault:"60s"`

	// HTTPRequestTimeout is the deadline of each request's context. When
	// it expires, database queries are canceled and the client gets a 503
	// instead of waiting forever. Keep it below HTTPWriteTimeout.
	HTTPRequestTimeout time.Duration `env:"HTTP_REQUEST_TIMEOUT" envDefault:"10s"`

	// ShutdownTimeout is how long in-flight requests get to finish after
	// SIGTERM before the server stops anyway.
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"20s"`

	// ShutdownDrainDelay keeps serving requests for a while after SIGTERM
	// while /readyz already reports 503, giving load balancers time to
	// stop sending new traffic. Use a few seconds on Kubernetes.
	ShutdownDrainDelay time.Duration `env:"SHUTDOWN_DRAIN_DELAY" envDefault:"0s"`

	// LogLevel is debug, info, warn or error.
	LogLevel slog.Level `env:"LOG_LEVEL" envDefault:"info"`

	// LogFormat is json (production) or text (easier to read locally).
	LogFormat string `env:"LOG_FORMAT" envDefault:"json"`

	// DatabaseURL is the PostgreSQL connection URL
	// (postgres://user:password@host:5432/dbname?sslmode=require). When
	// empty, the service uses an in-memory repository (handy for a quick
	// local run).
	DatabaseURL string `env:"DATABASE_URL"`

	// DBMaxConns is the maximum size of the connection pool.
	DBMaxConns int32 `env:"DB_MAX_CONNS" envDefault:"10"`

	// DBMigrate runs pending database migrations on startup.
	DBMigrate bool `env:"DB_MIGRATE" envDefault:"true"`

	// OTLPEndpoint is the standard OpenTelemetry variable. Telemetry
	// export is enabled only when it is set; the OpenTelemetry SDK reads
	// it (and the other OTEL_* variables) by itself.
	OTLPEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`
}

// Load reads the configuration from the process environment.
func Load() (Config, error) {
	return parse(env.Options{})
}

// parse is separated from Load so tests can pass a fake environment
// (env.Options.Environment) instead of mutating the real one, which
// lets them run in parallel.
func parse(opts env.Options) (Config, error) {
	cfg, err := env.ParseAsWithOptions[Config](opts)
	if err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// validate catches invalid combinations at startup ("fail fast") instead
// of when the first request arrives.
func (c Config) validate() error {
	var errs []error

	if c.HTTPAddr == "" {
		errs = append(errs, errors.New("HTTP_ADDR must not be empty"))
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be json or text, got %q", c.LogFormat))
	}
	if c.DBMaxConns < 1 {
		errs = append(errs, fmt.Errorf("DB_MAX_CONNS must be at least 1, got %d", c.DBMaxConns))
	}

	positive := map[string]time.Duration{
		"HTTP_READ_HEADER_TIMEOUT": c.HTTPReadHeaderTimeout,
		"HTTP_READ_TIMEOUT":        c.HTTPReadTimeout,
		"HTTP_WRITE_TIMEOUT":       c.HTTPWriteTimeout,
		"HTTP_IDLE_TIMEOUT":        c.HTTPIdleTimeout,
		"HTTP_REQUEST_TIMEOUT":     c.HTTPRequestTimeout,
		"SHUTDOWN_TIMEOUT":         c.ShutdownTimeout,
	}
	for name, d := range positive {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("%s must be greater than zero, got %s", name, d))
		}
	}
	if c.ShutdownDrainDelay < 0 {
		errs = append(errs, fmt.Errorf("SHUTDOWN_DRAIN_DELAY must not be negative, got %s", c.ShutdownDrainDelay))
	}

	if c.DatabaseURL != "" {
		u, err := url.Parse(c.DatabaseURL)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
			// Never include the value in the error: it may contain a password.
			errs = append(errs, errors.New("DATABASE_URL must be a URL like postgres://user:password@host:5432/dbname"))
		}
	}

	// errors.Join reports every problem at once.
	return errors.Join(errs...)
}

// LogValue implements slog.LogValuer, so logging a Config never prints
// database credentials: only the scheme, host and database name of
// DatabaseURL are logged, because both the user info and the query
// string (?password=...) can contain secrets.
func (c Config) LogValue() slog.Value {
	db := "in-memory"
	if c.DatabaseURL != "" {
		db = "invalid"
		if u, err := url.Parse(c.DatabaseURL); err == nil {
			db = u.Scheme + "://" + u.Host + u.Path
		}
	}

	return slog.GroupValue(
		slog.String("env", c.Env),
		slog.String("http_addr", c.HTTPAddr),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("log_format", c.LogFormat),
		slog.String("database", db),
		slog.Int("db_max_conns", int(c.DBMaxConns)),
		slog.Bool("db_migrate", c.DBMigrate),
		slog.Bool("telemetry", c.OTLPEndpoint != ""),
	)
}
