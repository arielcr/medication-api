package httpapi_test

import (
	"net/http"
	"testing"
	"time"
	"uuid"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/arielcr/medication-api/internal/adapter/httpapi"
	"github.com/arielcr/medication-api/internal/adapter/memory"
	"github.com/arielcr/medication-api/internal/medication"
	"github.com/arielcr/medication-api/internal/telemetry"
)

// Not parallel: telemetry.Setup installs the global W3C propagator, the
// same thing main does at startup.
func TestTracing(t *testing.T) {
	if _, err := telemetry.Setup(t.Context(), telemetry.Config{Enabled: false}); err != nil {
		t.Fatalf("telemetry.Setup() error = %v", err)
	}

	// A SpanRecorder keeps finished spans in memory so the test can
	// inspect them; nothing is exported anywhere.
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	h := httpapi.NewHandler(httpapi.Options{
		Service:        medication.NewService(memory.NewRepository()),
		Logger:         discardLogger(),
		TracerProvider: tp,
		RequestTimeout: time.Second,
	})
	api := &testAPI{t: t, handler: h}

	// Continue a trace started by the caller (W3C traceparent header).
	api.do(http.MethodGet, "/v1/medications/"+uuid.NewV7().String(), "",
		"traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	api.do(http.MethodGet, "/healthz", "") // probes are not traced

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1 (probes must be filtered out)", len(spans))
	}

	span := spans[0]
	if got, want := span.Name(), "GET /v1/medications/{id}"; got != want {
		t.Errorf("span name = %q, want %q (named after the route, not the raw path)", got, want)
	}
	if got := span.SpanContext().TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace ID = %s, want the one from the traceparent header", got)
	}
}
