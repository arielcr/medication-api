package medication_test

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/arielcr/medication-api/internal/medication"
)

// fakeRepo is a hand-written test double for medication.Repository.
// Each method delegates to an optional function field, so every test
// configures only the behavior it needs. Calls to methods that were not
// configured fail the test, which catches unexpected interactions.
type fakeRepo struct {
	t        *testing.T
	createFn func(context.Context, medication.Medication) error
	getFn    func(context.Context, uuid.UUID) (medication.Medication, error)
	listFn   func(context.Context, medication.ListFilter) ([]medication.Medication, error)
	updateFn func(context.Context, medication.Medication) error
	deleteFn func(context.Context, uuid.UUID) error
}

func (f *fakeRepo) Create(ctx context.Context, m medication.Medication) error {
	if f.createFn == nil {
		f.t.Fatal("unexpected call to Create")
	}
	return f.createFn(ctx, m)
}

func (f *fakeRepo) Get(ctx context.Context, id uuid.UUID) (medication.Medication, error) {
	if f.getFn == nil {
		f.t.Fatal("unexpected call to Get")
	}
	return f.getFn(ctx, id)
}

func (f *fakeRepo) List(ctx context.Context, filter medication.ListFilter) ([]medication.Medication, error) {
	if f.listFn == nil {
		f.t.Fatal("unexpected call to List")
	}
	return f.listFn(ctx, filter)
}

func (f *fakeRepo) Update(ctx context.Context, m medication.Medication) error {
	if f.updateFn == nil {
		f.t.Fatal("unexpected call to Update")
	}
	return f.updateFn(ctx, m)
}

func (f *fakeRepo) Delete(ctx context.Context, id uuid.UUID) error {
	if f.deleteFn == nil {
		f.t.Fatal("unexpected call to Delete")
	}
	return f.deleteFn(ctx, id)
}

var (
	fixedNow = time.Date(2026, 10, 12, 15, 4, 5, 0, time.UTC)
	fixedID  = uuid.MustParse("0199d2a1-7c4e-7b2a-9f10-3c5d8e1a2b4c")
	errDB    = errors.New("connection refused")
)

func newTestService(repo medication.Repository) *medication.Service {
	return medication.NewService(repo,
		medication.WithClock(func() time.Time { return fixedNow }),
		medication.WithIDGenerator(func() uuid.UUID { return fixedID }),
	)
}

func validAttrs() medication.Attributes {
	return medication.Attributes{Name: "Paracetamol", Dosage: "500 mg", Form: medication.FormTablet}
}

func storedMedication(t *testing.T, version int64) medication.Medication {
	t.Helper()

	m, err := medication.New(fixedID, validAttrs(), fixedNow.Add(-time.Hour))
	if err != nil {
		t.Fatalf("medication.New() error = %v", err)
	}
	m.Version = version

	return m
}

func TestService_Create(t *testing.T) {
	t.Parallel()

	var saved medication.Medication
	repo := &fakeRepo{t: t, createFn: func(_ context.Context, m medication.Medication) error {
		saved = m
		return nil
	}}

	got, err := newTestService(repo).Create(t.Context(), validAttrs())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	want := medication.Medication{
		ID: fixedID, Name: "Paracetamol", Dosage: "500 mg", Form: medication.FormTablet,
		Version: 1, CreatedAt: fixedNow, UpdatedAt: fixedNow,
	}
	if got != want {
		t.Errorf("Create() =\n  %+v\nwant\n  %+v", got, want)
	}
	if saved != want {
		t.Errorf("repository received %+v, want %+v", saved, want)
	}
}

func TestService_Create_Errors(t *testing.T) {
	t.Parallel()

	t.Run("invalid attributes never reach the repository", func(t *testing.T) {
		t.Parallel()

		svc := newTestService(&fakeRepo{t: t}) // any repository call fails the test
		_, err := svc.Create(t.Context(), medication.Attributes{})

		var vErr *medication.ValidationError
		if !errors.As(err, &vErr) {
			t.Fatalf("Create() error = %v, want *ValidationError", err)
		}
	})

	t.Run("repository errors are wrapped, not replaced", func(t *testing.T) {
		t.Parallel()

		repo := &fakeRepo{t: t, createFn: func(context.Context, medication.Medication) error {
			return medication.ErrAlreadyExists
		}}
		_, err := newTestService(repo).Create(t.Context(), validAttrs())
		if !errors.Is(err, medication.ErrAlreadyExists) {
			t.Fatalf("Create() error = %v, want ErrAlreadyExists", err)
		}
	})
}

func TestService_Get(t *testing.T) {
	t.Parallel()

	stored := storedMedication(t, 3)
	repo := &fakeRepo{t: t, getFn: func(_ context.Context, id uuid.UUID) (medication.Medication, error) {
		if id != stored.ID {
			return medication.Medication{}, medication.ErrNotFound
		}
		return stored, nil
	}}
	svc := newTestService(repo)

	got, err := svc.Get(t.Context(), stored.ID)
	if err != nil || got != stored {
		t.Fatalf("Get() = %+v, %v; want %+v, nil", got, err, stored)
	}

	_, err = svc.Get(t.Context(), uuid.NewV7())
	if !errors.Is(err, medication.ErrNotFound) {
		t.Fatalf("Get(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestService_List(t *testing.T) {
	t.Parallel()

	items := make([]medication.Medication, 5)
	for i := range items {
		items[i] = medication.Medication{ID: uuid.NewV7(), Name: "M", Dosage: "1 mg", Form: medication.FormTablet, Version: 1}
	}

	tests := []struct {
		name          string
		filter        medication.ListFilter
		repoReturns   int // how many rows the fake repository returns
		wantRepoLimit int
		wantItems     int
		wantHasMore   bool
	}{
		{"default page size", medication.ListFilter{}, 5, medication.DefaultPageSize + 1, 5, false},
		{"limit is capped", medication.ListFilter{Limit: 1000}, 5, medication.MaxPageSize + 1, 5, false},
		{"extra row means there is another page", medication.ListFilter{Limit: 2}, 3, 3, 2, true},
		{"exact page has no next page", medication.ListFilter{Limit: 2}, 2, 3, 2, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotFilter medication.ListFilter
			repo := &fakeRepo{t: t, listFn: func(_ context.Context, f medication.ListFilter) ([]medication.Medication, error) {
				gotFilter = f
				return items[:tt.repoReturns], nil
			}}

			page, err := newTestService(repo).List(t.Context(), tt.filter)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if gotFilter.Limit != tt.wantRepoLimit {
				t.Errorf("repository limit = %d, want %d", gotFilter.Limit, tt.wantRepoLimit)
			}
			if len(page.Items) != tt.wantItems || page.HasMore != tt.wantHasMore {
				t.Errorf("page = %d items, HasMore=%v; want %d items, HasMore=%v",
					len(page.Items), page.HasMore, tt.wantItems, tt.wantHasMore)
			}
			if tt.wantHasMore && page.NextCursor != page.Items[len(page.Items)-1].ID {
				t.Errorf("NextCursor = %s, want ID of the last item", page.NextCursor)
			}
		})
	}

	t.Run("filter is normalized", func(t *testing.T) {
		t.Parallel()

		var gotFilter medication.ListFilter
		repo := &fakeRepo{t: t, listFn: func(_ context.Context, f medication.ListFilter) ([]medication.Medication, error) {
			gotFilter = f
			return nil, nil
		}}
		_, err := newTestService(repo).List(t.Context(), medication.ListFilter{Name: "  para ", Form: " Tablet"})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if gotFilter.Name != "para" || gotFilter.Form != medication.FormTablet {
			t.Errorf("repository filter = %+v, want trimmed name and lowercase form", gotFilter)
		}
	})

	t.Run("repository errors are wrapped", func(t *testing.T) {
		t.Parallel()

		repo := &fakeRepo{t: t, listFn: func(context.Context, medication.ListFilter) ([]medication.Medication, error) {
			return nil, errDB
		}}
		if _, err := newTestService(repo).List(t.Context(), medication.ListFilter{}); !errors.Is(err, errDB) {
			t.Fatalf("List() error = %v, want it to wrap %v", err, errDB)
		}
	})
}

func TestService_Update(t *testing.T) {
	t.Parallel()

	newAttrs := medication.Attributes{Name: "Paracetamol", Dosage: "1 g", Form: medication.FormTablet}

	tests := []struct {
		name            string
		attrs           medication.Attributes
		expectedVersion int64
		getErr          error
		updateErr       error
		wantErr         error // checked with errors.Is; nil means success
		wantValidation  bool
		wantRepoUpdate  bool
	}{
		{name: "any version", attrs: newAttrs, expectedVersion: medication.AnyVersion, wantRepoUpdate: true},
		{name: "matching version", attrs: newAttrs, expectedVersion: 3, wantRepoUpdate: true},
		{name: "stale version", attrs: newAttrs, expectedVersion: 2, wantErr: medication.ErrVersionMismatch},
		{name: "not found", attrs: newAttrs, getErr: medication.ErrNotFound, wantErr: medication.ErrNotFound},
		{name: "lost race in repository", attrs: newAttrs, updateErr: medication.ErrVersionMismatch, wantErr: medication.ErrVersionMismatch, wantRepoUpdate: true},
		{name: "invalid attributes", attrs: medication.Attributes{}, wantValidation: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stored := storedMedication(t, 3)
			var updated *medication.Medication
			repo := &fakeRepo{
				t: t,
				getFn: func(context.Context, uuid.UUID) (medication.Medication, error) {
					if tt.getErr != nil {
						return medication.Medication{}, tt.getErr
					}
					return stored, nil
				},
				updateFn: func(_ context.Context, m medication.Medication) error {
					updated = &m
					return tt.updateErr
				},
			}

			got, err := newTestService(repo).Update(t.Context(), stored.ID, tt.attrs, tt.expectedVersion)

			if tt.wantValidation {
				var vErr *medication.ValidationError
				if !errors.As(err, &vErr) {
					t.Fatalf("Update() error = %v, want *ValidationError", err)
				}
				return
			}
			if (updated != nil) != tt.wantRepoUpdate {
				t.Errorf("repository Update called = %v, want %v", updated != nil, tt.wantRepoUpdate)
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Update() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}

			// The repository receives the version it must match...
			if updated.Version != 3 {
				t.Errorf("repository received version %d, want 3", updated.Version)
			}
			// ...and the caller gets the new version back.
			if got.Version != 4 || got.Dosage != "1 g" || !got.UpdatedAt.Equal(fixedNow) {
				t.Errorf("Update() = %+v, want version 4, dosage 1 g, updated now", got)
			}
		})
	}
}

func TestService_Delete(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{t: t, deleteFn: func(context.Context, uuid.UUID) error {
		return medication.ErrNotFound
	}}
	err := newTestService(repo).Delete(t.Context(), fixedID)
	if !errors.Is(err, medication.ErrNotFound) {
		t.Fatalf("Delete() error = %v, want ErrNotFound", err)
	}

	repo.deleteFn = func(context.Context, uuid.UUID) error { return nil }
	if err := newTestService(repo).Delete(t.Context(), fixedID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestNewService_DefaultClockIsUTCMicroseconds(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{t: t, createFn: func(context.Context, medication.Medication) error { return nil }}
	m, err := medication.NewService(repo).Create(t.Context(), validAttrs())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if m.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt location = %v, want UTC", m.CreatedAt.Location())
	}
	if m.CreatedAt.Nanosecond()%1000 != 0 {
		t.Errorf("CreatedAt = %v, want microsecond precision", m.CreatedAt)
	}
	if m.ID == (uuid.UUID{}) {
		t.Error("ID is the zero UUID, want a generated UUIDv7")
	}
}
