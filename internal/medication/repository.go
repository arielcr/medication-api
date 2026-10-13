package medication

import (
	"context"
	"uuid"
)

// Repository is the port the domain uses to persist medications.
//
// It is defined here, next to the code that consumes it, and implemented
// by adapters (internal/adapter/memory, internal/adapter/postgres). This
// follows the Go idiom "accept interfaces, return structs" and keeps the
// dependency arrows pointing towards the domain.
//
// Every implementation must pass the contract tests in the
// medicationtest package.
type Repository interface {
	// Create stores a new medication. It returns ErrAlreadyExists when a
	// medication with the same name, dosage and form (case-insensitive)
	// is already stored.
	Create(ctx context.Context, m Medication) error

	// Get returns the medication with the given ID, or ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (Medication, error)

	// List returns at most filter.Limit medications ordered by ID.
	// Because IDs are UUIDv7, ID order is creation order.
	List(ctx context.Context, filter ListFilter) ([]Medication, error)

	// Update saves m only if the stored version equals m.Version, and
	// stores it with the version incremented by one. It returns
	// ErrNotFound, ErrVersionMismatch or ErrAlreadyExists.
	Update(ctx context.Context, m Medication) error

	// Delete removes the medication with the given ID, or returns
	// ErrNotFound.
	Delete(ctx context.Context, id uuid.UUID) error
}

// ListFilter selects which medications List returns.
type ListFilter struct {
	// Name, when not empty, keeps only medications whose name contains
	// it (case-insensitive).
	Name string

	// Form, when not empty, keeps only medications of that form.
	Form Form

	// After is the keyset pagination cursor: only medications with an ID
	// greater than After are returned. The zero UUID means "from the
	// beginning".
	After uuid.UUID

	// Limit is the maximum number of medications to return.
	Limit int
}
