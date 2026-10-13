package medication

import (
	"errors"
	"strings"
)

// Sentinel errors returned by the medication domain.
// Callers check them with errors.Is, never by comparing messages.
var (
	// ErrNotFound means no medication exists with the given ID.
	ErrNotFound = errors.New("medication not found")

	// ErrAlreadyExists means a medication with the same name, dosage
	// and form is already registered.
	ErrAlreadyExists = errors.New("medication already exists")

	// ErrVersionMismatch means the medication was modified by someone
	// else since the caller read it (optimistic concurrency conflict).
	ErrVersionMismatch = errors.New("medication version mismatch")
)

// FieldError describes why a single field is invalid.
type FieldError struct {
	Field   string
	Message string
}

// ValidationError is returned when one or more fields are invalid.
// It lists every problem at once so clients can fix them in one go.
type ValidationError struct {
	Fields []FieldError
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Field+" "+f.Message)
	}

	return "invalid medication: " + strings.Join(parts, "; ")
}
