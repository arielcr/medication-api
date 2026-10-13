package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
)

// maxBodyBytes caps request bodies. A medication is a few hundred bytes,
// so 1 MiB is generous while still protecting the server from clients
// that send huge payloads to exhaust memory.
const maxBodyBytes = 1 << 20

// decodeJSON reads exactly one JSON object from the request body into dst.
//
// It is strict on purpose: unknown fields, trailing data, a wrong
// Content-Type and oversized bodies are all rejected with a clear 4xx
// error instead of being silently ignored.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mediaType, _, err := mime.ParseMediaType(ct)
		if err != nil || mediaType != "application/json" {
			return &requestError{
				status: http.StatusUnsupportedMediaType,
				detail: "Content-Type must be application/json",
			}
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			return &requestError{status: http.StatusRequestEntityTooLarge, detail: "request body must not exceed 1 MiB"}
		case errors.Is(err, io.EOF):
			return &requestError{status: http.StatusBadRequest, detail: "request body must not be empty"}
		default:
			// Decoding errors describe the client's own input (syntax
			// error, wrong type, unknown field), so it is safe and helpful
			// to return them.
			return &requestError{status: http.StatusBadRequest, detail: "request body is not valid: " + err.Error()}
		}
	}

	// A second Decode must hit EOF; anything else means extra data after
	// the object, e.g. `{"name":"a"}{"name":"b"}`.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &requestError{status: http.StatusBadRequest, detail: "request body must contain a single JSON object"}
	}

	return nil
}

// writeJSON encodes v as the JSON response body.
func writeJSON(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, v any) {
	writeJSONAs(w, r, logger, status, "application/json", v)
}

func writeJSONAs(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, contentType string, v any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)

	// Once the status line is sent we can no longer change the response,
	// so an encoding failure (usually the client hung up) is only logged.
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.WarnContext(r.Context(), "write response body", slog.Any("error", err))
	}
}
