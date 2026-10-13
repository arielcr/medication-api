package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/arielcr/medication-api/internal/medication"
)

// problem is an error response body following RFC 9457
// ("Problem Details for HTTP APIs"), served as application/problem+json.
// A standard error format lets clients handle every error the same way.
type problem struct {
	// Type identifies the problem kind. "about:blank" means the HTTP
	// status code alone describes it (RFC 9457 §4.2.1).
	Type string `json:"type"`
	// Title is a short summary of the problem type (the status text).
	Title string `json:"title"`
	// Status repeats the HTTP status code for convenience.
	Status int `json:"status"`
	// Detail is a human-readable explanation of this occurrence.
	Detail string `json:"detail,omitempty"`
	// Instance is the request path that caused the problem.
	Instance string `json:"instance,omitempty"`

	// Extension members (allowed by RFC 9457).
	RequestID string         `json:"request_id,omitempty"`
	Errors    []problemField `json:"errors,omitempty"`
}

type problemField struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// requestError is an error caused by the HTTP request itself (bad JSON,
// invalid query parameter, wrong header...), detected before the domain
// is involved. It carries the HTTP status to answer with.
type requestError struct {
	status int
	detail string
	fields []medication.FieldError
}

func (e *requestError) Error() string { return e.detail }

// writeError is the single place where errors become HTTP responses.
//
// It maps domain errors to status codes with errors.Is / errors.As, so
// errors keep working even after being wrapped with fmt.Errorf("...: %w").
// Unexpected errors are logged here, once, at the edge of the system, and
// the client gets a generic 500 that leaks no internal details.
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	p := problem{Type: "about:blank", Instance: r.URL.Path, RequestID: RequestIDFromContext(r.Context())}

	var reqErr *requestError
	var validationErr *medication.ValidationError

	switch {
	case errors.As(err, &reqErr):
		p.Status, p.Detail = reqErr.status, reqErr.detail
		p.Errors = toProblemFields(reqErr.fields)

	case errors.As(err, &validationErr):
		// 422: the JSON is well-formed but its content breaks business rules.
		p.Status, p.Detail = http.StatusUnprocessableEntity, "the medication is not valid"
		p.Errors = toProblemFields(validationErr.Fields)

	case errors.Is(err, medication.ErrNotFound):
		p.Status, p.Detail = http.StatusNotFound, "medication not found"

	case errors.Is(err, medication.ErrAlreadyExists):
		p.Status, p.Detail = http.StatusConflict, "a medication with the same name, dosage and form already exists"

	case errors.Is(err, medication.ErrVersionMismatch):
		// 412: the If-Match precondition sent by the client failed.
		p.Status, p.Detail = http.StatusPreconditionFailed, "the medication was modified since you last read it; fetch it again and retry"

	case errors.Is(err, context.Canceled):
		// The client went away; nobody will read the response. Not an
		// error worth alerting on.
		logger.InfoContext(r.Context(), "request canceled by client", slog.Any("error", err))
		p.Status, p.Detail = statusClientClosedRequest, "request canceled"

	case errors.Is(err, context.DeadlineExceeded):
		logger.ErrorContext(r.Context(), "request timed out", slog.Any("error", err))
		p.Status, p.Detail = http.StatusServiceUnavailable, "the request took too long; try again later"

	default:
		logger.ErrorContext(r.Context(), "unexpected error", slog.Any("error", err))
		p.Status, p.Detail = http.StatusInternalServerError, "an unexpected error occurred"
	}

	p.Title = statusText(p.Status)
	writeJSONAs(w, r, logger, p.Status, "application/problem+json", p)
}

// statusClientClosedRequest is the de facto status (popularized by nginx)
// for requests the client abandoned. It only shows up in logs and metrics.
const statusClientClosedRequest = 499

func statusText(status int) string {
	if status == statusClientClosedRequest {
		return "Client Closed Request"
	}

	return http.StatusText(status)
}

func toProblemFields(fields []medication.FieldError) []problemField {
	if len(fields) == 0 {
		return nil
	}

	out := make([]problemField, len(fields))
	for i, f := range fields {
		out[i] = problemField{Field: f.Field, Message: f.Message}
	}

	return out
}
