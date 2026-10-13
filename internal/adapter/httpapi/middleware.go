package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
	"uuid"
)

// A middleware is a function that takes an http.Handler and returns a new
// one that does something before and/or after calling it. It is how
// cross-cutting concerns (IDs, logs, panics) are added without touching
// every handler.

const requestIDHeader = "X-Request-ID"

// requestIDKey is an unexported type for the context key, so no other
// package can read or overwrite the value by accident.
type requestIDKey struct{}

// requestID makes sure every request has an ID. It reuses a valid
// X-Request-ID sent by the client or a proxy (so one ID can follow a
// request across services) and generates one otherwise. The ID is echoed
// in the response and stored in the request context.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			id = uuid.NewV4().String()
		}

		w.Header().Set(requestIDHeader, id)
		serveWithContext(context.WithValue(r.Context(), requestIDKey{}, id), next, w, r)
	})
}

// serveWithContext calls next with a copy of r that carries ctx.
//
// r.WithContext returns a shallow copy of the request, and ServeMux
// records the matched route (Pattern) on the copy it receives. Copying it
// back lets the outer middleware see the route: the access log and the
// tracing middleware (which names spans after it) run outside.
func serveWithContext(ctx context.Context, next http.Handler, w http.ResponseWriter, r *http.Request) {
	withCtx := r.WithContext(ctx)
	next.ServeHTTP(w, withCtx)
	r.Pattern = withCtx.Pattern
}

// RequestIDFromContext returns the request ID stored by the middleware,
// or "" if there is none.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// validRequestID accepts short IDs made of safe characters. Anything else
// is replaced, so clients cannot inject arbitrary text into our logs.
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}

	return true
}

// logRequests writes one structured access log line per request, after
// the request finishes, with its status code, size and duration.
func logRequests(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			level := slog.LevelInfo
			switch {
			case isProbe(r):
				// Orchestrators call the probes every few seconds; logging
				// them at info would drown the useful lines.
				level = slog.LevelDebug
			case rec.status >= http.StatusInternalServerError:
				level = slog.LevelError
			}

			// r.Pattern is set by ServeMux once it matched a route, e.g.
			// "GET /v1/medications/{id}". Logging the pattern instead of
			// the raw path groups requests per endpoint.
			logger.LogAttrs(r.Context(), level, "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("route", r.Pattern),
				slog.Int("status", rec.status),
				slog.Int("bytes", rec.bytes),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
				slog.String("request_id", RequestIDFromContext(r.Context())),
				slog.String("remote_addr", r.RemoteAddr),
				slog.String("user_agent", r.UserAgent()),
			)
		})
	}
}

// timeout sets a deadline on the request context. Everything that honors
// the context (database queries in particular) stops when it expires, so
// a slow dependency cannot pile up requests that wait forever. The
// handler then gets context.DeadlineExceeded, which becomes a 503.
func timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if d <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			serveWithContext(ctx, next, w, r)
		})
	}
}

func isProbe(r *http.Request) bool {
	return r.URL.Path == "/healthz" || r.URL.Path == "/readyz"
}

// recoverPanics turns a panic in a handler into a 500 response instead of
// a dropped connection, and logs it with its stack trace.
func recoverPanics(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// http.ErrAbortHandler is the sentinel used to abort a
				// response on purpose; net/http handles it, so re-panic.
				if rec == http.ErrAbortHandler { //nolint:errorlint // recover() returns the exact value passed to panic.
					panic(rec)
				}

				logger.ErrorContext(r.Context(), "panic recovered",
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())),
				)

				writeJSONAs(w, r, logger, http.StatusInternalServerError, "application/problem+json", problem{
					Type:      "about:blank",
					Title:     http.StatusText(http.StatusInternalServerError),
					Status:    http.StatusInternalServerError,
					Detail:    "an unexpected error occurred",
					Instance:  r.URL.Path,
					RequestID: RequestIDFromContext(r.Context()),
				})
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder wraps http.ResponseWriter to remember the status code
// and the number of bytes written, which the standard ResponseWriter
// does not expose.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wroteHeader = true
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n

	return n, err
}

// Unwrap lets http.ResponseController reach the original ResponseWriter
// (for Flush, deadlines, ...) through this wrapper.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
