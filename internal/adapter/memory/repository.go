// Package memory provides an in-memory implementation of
// medication.Repository.
//
// It is used for local development (when DATABASE_URL is not set) and in
// HTTP tests. Data lives in a map and is lost when the process stops.
package memory

import (
	"context"
	"slices"
	"strings"
	"sync"
	"uuid"

	"github.com/arielcr/medication-api/internal/medication"
)

// Repository is an in-memory medication.Repository. It is safe for
// concurrent use: a sync.RWMutex lets many readers in at the same time
// but gives writers exclusive access.
type Repository struct {
	mu    sync.RWMutex
	items map[uuid.UUID]medication.Medication
}

// Compile-time check that *Repository implements medication.Repository.
var _ medication.Repository = (*Repository)(nil)

// NewRepository returns an empty repository.
func NewRepository() *Repository {
	return &Repository{items: make(map[uuid.UUID]medication.Medication)}
}

// Create implements medication.Repository.
func (r *Repository) Create(ctx context.Context, m medication.Medication) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.items[m.ID]; exists || r.duplicateOf(m) {
		return medication.ErrAlreadyExists
	}

	r.items[m.ID] = m

	return nil
}

// Get implements medication.Repository.
func (r *Repository) Get(ctx context.Context, id uuid.UUID) (medication.Medication, error) {
	if err := ctx.Err(); err != nil {
		return medication.Medication{}, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	m, ok := r.items[id]
	if !ok {
		return medication.Medication{}, medication.ErrNotFound
	}

	return m, nil
}

// List implements medication.Repository.
func (r *Repository) List(ctx context.Context, filter medication.ListFilter) ([]medication.Medication, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	name := strings.ToLower(filter.Name)

	r.mu.RLock()
	matches := make([]medication.Medication, 0, len(r.items))
	for _, m := range r.items {
		if m.ID.Compare(filter.After) <= 0 {
			continue
		}
		if filter.Form != "" && m.Form != filter.Form {
			continue
		}
		if name != "" && !strings.Contains(strings.ToLower(m.Name), name) {
			continue
		}
		matches = append(matches, m)
	}
	r.mu.RUnlock()

	// Go maps have no order, so sort to honor the "ordered by ID" contract.
	slices.SortFunc(matches, func(a, b medication.Medication) int {
		return a.ID.Compare(b.ID)
	})

	if filter.Limit > 0 && len(matches) > filter.Limit {
		matches = matches[:filter.Limit]
	}

	return matches, nil
}

// Update implements medication.Repository.
func (r *Repository) Update(ctx context.Context, m medication.Medication) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	stored, ok := r.items[m.ID]
	switch {
	case !ok:
		return medication.ErrNotFound
	case stored.Version != m.Version:
		return medication.ErrVersionMismatch
	case r.duplicateOf(m):
		return medication.ErrAlreadyExists
	}

	m.Version++
	r.items[m.ID] = m

	return nil
}

// Delete implements medication.Repository.
func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.items[id]; !ok {
		return medication.ErrNotFound
	}

	delete(r.items, id)

	return nil
}

// duplicateOf reports whether another medication (different ID) has the
// same name, dosage and form, ignoring case. It mirrors the unique index
// of the PostgreSQL schema. It is O(n), which is fine for an in-memory
// store meant for development and tests. The caller must hold the lock.
func (r *Repository) duplicateOf(m medication.Medication) bool {
	for id, other := range r.items {
		if id != m.ID &&
			other.Form == m.Form &&
			strings.EqualFold(other.Name, m.Name) &&
			strings.EqualFold(other.Dosage, m.Dosage) {
			return true
		}
	}

	return false
}
