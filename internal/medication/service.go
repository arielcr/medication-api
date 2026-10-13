package medication

import (
	"context"
	"fmt"
	"strings"
	"time"
	"uuid"
)

const (
	// DefaultPageSize is used by List when no limit is given.
	DefaultPageSize = 20

	// MaxPageSize is the largest page List will return.
	MaxPageSize = 100

	// AnyVersion disables the optimistic concurrency check in Update.
	// Real versions start at 1, so 0 can never match a stored version.
	AnyVersion int64 = 0
)

// Service implements the medication use cases (create, read, list,
// update, delete) on top of a Repository.
//
// It owns the two sources of non-determinism, the clock and the ID
// generator, so the domain functions it calls stay pure.
type Service struct {
	repo  Repository
	now   func() time.Time
	newID func() uuid.UUID
}

// Option customizes a Service. Options exist mainly so tests can control
// time and IDs (functional options pattern).
type Option func(*Service)

// WithClock replaces the function used to read the current time.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithIDGenerator replaces the function used to create new IDs.
func WithIDGenerator(newID func() uuid.UUID) Option {
	return func(s *Service) { s.newID = newID }
}

// NewService returns a Service that stores medications in repo.
func NewService(repo Repository, opts ...Option) *Service {
	s := &Service{
		repo:  repo,
		now:   defaultClock,
		newID: uuid.NewV7,
	}
	for _, opt := range opts {
		opt(s)
	}

	return s
}

// defaultClock returns the current time in UTC, truncated to microseconds
// because that is the precision PostgreSQL stores. Truncating here makes
// the in-memory and PostgreSQL repositories return identical values.
func defaultClock() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// Create validates attrs and stores a new medication.
func (s *Service) Create(ctx context.Context, attrs Attributes) (Medication, error) {
	m, err := New(s.newID(), attrs, s.now())
	if err != nil {
		return Medication{}, err
	}

	if err := s.repo.Create(ctx, m); err != nil {
		return Medication{}, fmt.Errorf("create medication: %w", err)
	}

	return m, nil
}

// Get returns the medication with the given ID.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Medication, error) {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return Medication{}, fmt.Errorf("get medication %s: %w", id, err)
	}

	return m, nil
}

// Page is one page of List results.
type Page struct {
	Items []Medication

	// HasMore reports whether another page exists after this one.
	HasMore bool

	// NextCursor is the value to pass as ListFilter.After to fetch the
	// next page. It is only meaningful when HasMore is true.
	NextCursor uuid.UUID
}

// List returns one page of medications matching filter.
//
// A Limit of 0 means DefaultPageSize, and values above MaxPageSize are
// capped. The HTTP layer validates user input; capping here is a second
// line of defense so no caller can ask the database for unbounded rows.
func (s *Service) List(ctx context.Context, filter ListFilter) (Page, error) {
	pageSize := filter.Limit
	switch {
	case pageSize <= 0:
		pageSize = DefaultPageSize
	case pageSize > MaxPageSize:
		pageSize = MaxPageSize
	}

	filter.Name = strings.TrimSpace(filter.Name)
	filter.Form = ParseForm(string(filter.Form))

	// Ask for one extra row: if it comes back, there is a next page.
	// This avoids a separate COUNT query.
	filter.Limit = pageSize + 1

	items, err := s.repo.List(ctx, filter)
	if err != nil {
		return Page{}, fmt.Errorf("list medications: %w", err)
	}

	page := Page{Items: items}
	if len(items) > pageSize {
		page.Items = items[:pageSize]
		page.HasMore = true
		page.NextCursor = page.Items[pageSize-1].ID
	}

	return page, nil
}

// Update replaces the editable fields of a medication.
//
// If expectedVersion is not AnyVersion, the update only happens when the
// stored version matches it; otherwise ErrVersionMismatch is returned.
// Even with AnyVersion, the repository rejects the write if another
// request changed the medication between our read and our write, so
// concurrent updates are never silently lost.
func (s *Service) Update(ctx context.Context, id uuid.UUID, attrs Attributes, expectedVersion int64) (Medication, error) {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return Medication{}, fmt.Errorf("update medication %s: %w", id, err)
	}

	// The precondition is checked before the new content is validated, as
	// HTTP requires (RFC 9110 §13.2): a stale If-Match is reported as such
	// even if the body is also invalid.
	if expectedVersion != AnyVersion && m.Version != expectedVersion {
		return Medication{}, fmt.Errorf("update medication %s: %w", id, ErrVersionMismatch)
	}

	if err := m.Update(attrs, s.now()); err != nil {
		return Medication{}, err
	}

	if err := s.repo.Update(ctx, m); err != nil {
		return Medication{}, fmt.Errorf("update medication %s: %w", id, err)
	}

	m.Version++

	return m, nil
}

// Delete removes the medication with the given ID.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete medication %s: %w", id, err)
	}

	return nil
}
