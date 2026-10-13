// Package httpapi is the HTTP adapter of the service: it translates HTTP
// requests into calls to the medication domain and domain results (and
// errors) back into HTTP responses.
//
// Routing uses the standard library http.ServeMux, which since Go 1.22
// supports HTTP methods and path wildcards in patterns
// ("GET /v1/medications/{id}"), so no third-party router is needed.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"
	"uuid"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/arielcr/medication-api/internal/medication"
)

// MedicationService is the behavior the HTTP layer needs from the domain.
//
// It is declared here, by the consumer, instead of depending on the
// concrete *medication.Service. Any type with these methods satisfies it
// (Go interfaces are implicit), which keeps this package decoupled and
// easy to test.
type MedicationService interface {
	Create(ctx context.Context, attrs medication.Attributes) (medication.Medication, error)
	Get(ctx context.Context, id uuid.UUID) (medication.Medication, error)
	List(ctx context.Context, filter medication.ListFilter) (medication.Page, error)
	Update(ctx context.Context, id uuid.UUID, attrs medication.Attributes, expectedVersion int64) (medication.Medication, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

// ReadinessCheck reports whether the service can handle traffic right
// now (for example, whether the database is reachable). A nil error
// means ready.
type ReadinessCheck func(ctx context.Context) error

// Options configures the HTTP handler.
type Options struct {
	// Service implements the medication use cases. Required.
	Service MedicationService

	// Logger receives access logs and unexpected errors. Required.
	Logger *slog.Logger

	// Ready is called by GET /readyz. If nil, the service is always ready.
	Ready ReadinessCheck

	// RequestTimeout is the deadline set on every request context. Zero
	// means no deadline.
	RequestTimeout time.Duration

	// TracerProvider and MeterProvider receive the HTTP spans and metrics.
	// When nil, the global OpenTelemetry providers are used (no-ops unless
	// telemetry.Setup enabled exporting). Tests inject their own.
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

// NewHandler returns the root http.Handler of the API: all routes wrapped
// in the middleware chain.
func NewHandler(opts Options) http.Handler {
	if opts.Ready == nil {
		opts.Ready = func(context.Context) error { return nil }
	}

	api := &medicationHandler{svc: opts.Service, logger: opts.Logger}
	hc := &healthHandler{ready: opts.Ready, logger: opts.Logger}

	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/medications", api.create)
	mux.HandleFunc("GET /v1/medications", api.list)
	mux.HandleFunc("GET /v1/medications/{id}", api.get)
	mux.HandleFunc("PUT /v1/medications/{id}", api.update)
	mux.HandleFunc("DELETE /v1/medications/{id}", api.remove)

	mux.HandleFunc("GET /healthz", hc.liveness)
	mux.HandleFunc("GET /readyz", hc.readiness)

	// Middleware runs outside-in:
	//  1. tracing starts a span (and records HTTP metrics) first, so every
	//     log line below can carry the trace_id;
	//  2. requestID, so every log line and error response has the ID;
	//  3. logRequests writes one access log line per request;
	//  4. recoverPanics turns a panic into a 500 that is still logged and
	//     traced;
	//  5. timeout puts a deadline on the request context.
	return chain(problemFallback(mux, opts.Logger),
		tracing(opts),
		requestID,
		logRequests(opts.Logger),
		recoverPanics(opts.Logger),
		timeout(opts.RequestTimeout),
	)
}

// problemFallback makes "no route" responses use the same problem+json
// format as every other error. ServeMux answers those itself with plain
// text (404, or 405 plus an Allow header when the path exists but not
// with that method), so we let it decide the status and the Allow header,
// and only rewrite the body.
func problemFallback(mux *http.ServeMux, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An empty pattern means no route matched (redirects, e.g. for
		// paths with "//", still get a pattern and are served normally).
		if _, pattern := mux.Handler(r); pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}

		probe := &headerRecorder{header: make(http.Header)}
		mux.ServeHTTP(probe, r)

		detail := "no route matches this URL"
		if probe.status == http.StatusMethodNotAllowed {
			detail = "this URL does not support the " + r.Method + " method"
			w.Header().Set("Allow", probe.header.Get("Allow"))
		}

		writeError(w, r, logger, &requestError{status: probe.status, detail: detail})
	})
}

// headerRecorder is a throwaway ResponseWriter that keeps the status and
// headers and discards the body.
type headerRecorder struct {
	header http.Header
	status int
}

func (h *headerRecorder) Header() http.Header { return h.header }

func (h *headerRecorder) WriteHeader(status int) {
	if h.status == 0 {
		h.status = status
	}
}

func (h *headerRecorder) Write(b []byte) (int, error) {
	h.WriteHeader(http.StatusOK)
	return len(b), nil
}

// tracing instruments requests with OpenTelemetry: one span per request
// (continuing the caller's trace if it sent a traceparent header) and the
// standard HTTP server metrics (request duration, sizes). Spans are named
// after the matched route, e.g. "GET /v1/medications/{id}", which keeps
// the number of distinct names small.
func tracing(opts Options) func(http.Handler) http.Handler {
	otelOpts := []otelhttp.Option{
		// Probes are called every few seconds; tracing them is just noise.
		otelhttp.WithFilter(func(r *http.Request) bool { return !isProbe(r) }),
	}
	if opts.TracerProvider != nil {
		otelOpts = append(otelOpts, otelhttp.WithTracerProvider(opts.TracerProvider))
	}
	if opts.MeterProvider != nil {
		otelOpts = append(otelOpts, otelhttp.WithMeterProvider(opts.MeterProvider))
	}

	return otelhttp.NewMiddleware("http.server", otelOpts...)
}

// chain wraps h with the given middleware. The first middleware in the
// list is the outermost one (the first to see the request).
func chain(h http.Handler, middleware ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}

	return h
}
