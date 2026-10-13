package telemetry

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// NewLogger returns a structured logger that writes to w.
//
// format is "json" (one JSON object per line, easy for log platforms to
// index) or "text" (key=value, easier to read in a terminal). Every
// record logged with a context that carries an active span gets trace_id
// and span_id fields, so a log line can be matched to its trace.
func NewLogger(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}

	var h slog.Handler
	if format == "text" {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}

	return slog.New(traceHandler{Handler: h})
}

// traceHandler is a slog.Handler decorator: it adds trace context to each
// record and delegates everything else to the wrapped handler.
type traceHandler struct {
	slog.Handler
}

// Handle adds trace_id and span_id when ctx carries a valid span.
func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}

	return h.Handler.Handle(ctx, r)
}

// WithAttrs and WithGroup must be overridden: the embedded methods would
// return the inner handler unwrapped, and loggers created with
// logger.With(...) would silently lose the trace fields.

// WithAttrs implements slog.Handler.
func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler.
func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{Handler: h.Handler.WithGroup(name)}
}
