package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/arielcr/medication-api/internal/telemetry"
)

func TestNewLogger_AddsTraceContext(t *testing.T) {
	t.Parallel()

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	}))

	var buf bytes.Buffer
	// With(...) returns a new handler: this checks the decorator survives it.
	logger := telemetry.NewLogger(&buf, slog.LevelInfo, "json").With("component", "test")
	logger.InfoContext(ctx, "hello")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log is not JSON: %q", buf.String())
	}
	if entry["trace_id"] != traceID.String() || entry["span_id"] != spanID.String() || entry["component"] != "test" {
		t.Errorf("log entry = %v, want trace_id, span_id and component", entry)
	}
}

func TestNewLogger_NoSpanNoTraceFields(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	telemetry.NewLogger(&buf, slog.LevelInfo, "text").InfoContext(context.Background(), "hello")

	if strings.Contains(buf.String(), "trace_id") {
		t.Errorf("log has trace_id without a span: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "msg=hello") {
		t.Errorf("text format expected, got %q", buf.String())
	}
}

func TestNewLogger_RespectsLevel(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := telemetry.NewLogger(&buf, slog.LevelWarn, "json")
	logger.Info("hidden")
	logger.Warn("shown")

	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), "shown") {
		t.Errorf("level filtering failed: %q", buf.String())
	}
}

func TestSetup_Disabled(t *testing.T) {
	t.Parallel()

	shutdown, err := telemetry.Setup(t.Context(), telemetry.Config{ServiceName: "test", Enabled: false})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if err := shutdown(t.Context()); err != nil {
		t.Fatalf("shutdown() error = %v", err)
	}
}

// Not parallel: it sets environment variables and Setup installs global
// providers.
func TestSetup_ExportsToOTLPEndpoint(t *testing.T) {
	// A fake OTLP/HTTP collector that records which signals it receives.
	var mu sync.Mutex
	received := map[string]bool{}
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received[r.URL.Path] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()

	// The SDK reads the standard OTEL_* variables by itself.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)

	shutdown, err := telemetry.Setup(t.Context(), telemetry.Config{
		ServiceName: "medication-api-test", ServiceVersion: "test", Environment: "test", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	_, span := otel.Tracer("test").Start(t.Context(), "operation")
	span.End()

	// Shutdown flushes the buffered span and the final metrics.
	if err := shutdown(t.Context()); err != nil {
		t.Fatalf("shutdown() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !received["/v1/traces"] || !received["/v1/metrics"] {
		t.Errorf("collector received %v, want /v1/traces and /v1/metrics", received)
	}
}
