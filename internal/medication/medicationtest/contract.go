// Package medicationtest contains test helpers for the medication domain.
//
// Its main export, RunRepositoryContract, is a reusable test suite: every
// medication.Repository implementation (in-memory, PostgreSQL, ...) runs
// it, which guarantees all adapters behave exactly the same way.
package medicationtest

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/arielcr/medication-api/internal/medication"
)

// NewRepositoryFunc returns a new, empty repository for one test.
type NewRepositoryFunc func(t *testing.T) medication.Repository

// RunRepositoryContract runs the behavior every medication.Repository
// must satisfy. newRepo is called once per subtest so subtests are
// isolated and can run in parallel.
func RunRepositoryContract(t *testing.T, newRepo NewRepositoryFunc) {
	t.Helper()

	t.Run("create then get returns the same medication", func(t *testing.T) {
		t.Parallel()
		ctx, repo := t.Context(), newRepo(t)

		want := NewMedication(t, "Paracetamol", "500 mg", medication.FormTablet)
		mustCreate(ctx, t, repo, want)

		got, err := repo.Get(ctx, want.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		AssertEqual(t, got, want)
	})

	t.Run("get unknown id returns ErrNotFound", func(t *testing.T) {
		t.Parallel()
		ctx, repo := t.Context(), newRepo(t)

		_, err := repo.Get(ctx, uuid.NewV7())
		if !errors.Is(err, medication.ErrNotFound) {
			t.Fatalf("Get() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("create duplicate returns ErrAlreadyExists (case-insensitive)", func(t *testing.T) {
		t.Parallel()
		ctx, repo := t.Context(), newRepo(t)

		mustCreate(ctx, t, repo, NewMedication(t, "Paracetamol", "500 mg", medication.FormTablet))

		dup := NewMedication(t, "PARACETAMOL", "500 MG", medication.FormTablet)
		if err := repo.Create(ctx, dup); !errors.Is(err, medication.ErrAlreadyExists) {
			t.Fatalf("Create(duplicate) error = %v, want ErrAlreadyExists", err)
		}

		// Same name and dosage with a different form is a different medication.
		other := NewMedication(t, "Paracetamol", "500 mg", medication.FormCapsule)
		mustCreate(ctx, t, repo, other)
	})

	t.Run("list returns pages in ID order", func(t *testing.T) {
		t.Parallel()
		ctx, repo := t.Context(), newRepo(t)

		var created []medication.Medication
		for _, name := range []string{"Aspirin", "Ibuprofen", "Naproxen"} {
			m := NewMedication(t, name, "100 mg", medication.FormTablet)
			mustCreate(ctx, t, repo, m)
			created = append(created, m)
		}

		first, err := repo.List(ctx, medication.ListFilter{Limit: 2})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		assertIDs(t, first, created[0].ID, created[1].ID)

		second, err := repo.List(ctx, medication.ListFilter{Limit: 2, After: first[1].ID})
		if err != nil {
			t.Fatalf("List(after) error = %v", err)
		}
		assertIDs(t, second, created[2].ID)
	})

	t.Run("list filters by form and name", func(t *testing.T) {
		t.Parallel()
		ctx, repo := t.Context(), newRepo(t)

		para := NewMedication(t, "Paracetamol", "500 mg", medication.FormTablet)
		paraSyrup := NewMedication(t, "Paracetamol Kids", "120 mg/5 ml", medication.FormLiquid)
		ibu := NewMedication(t, "Ibuprofen", "200 mg", medication.FormTablet)
		pct := NewMedication(t, "Zinc 100% oxide", "10 g", medication.FormCream)
		for _, m := range []medication.Medication{para, paraSyrup, ibu, pct} {
			mustCreate(ctx, t, repo, m)
		}

		tests := []struct {
			name   string
			filter medication.ListFilter
			want   []uuid.UUID
		}{
			{"by form", medication.ListFilter{Form: medication.FormTablet}, []uuid.UUID{para.ID, ibu.ID}},
			{"by name, case-insensitive", medication.ListFilter{Name: "PARACET"}, []uuid.UUID{para.ID, paraSyrup.ID}},
			{"by name and form", medication.ListFilter{Name: "para", Form: medication.FormLiquid}, []uuid.UUID{paraSyrup.ID}},
			{"wildcards are literal", medication.ListFilter{Name: "%"}, []uuid.UUID{pct.ID}},
			{"no match", medication.ListFilter{Name: "zzz"}, nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				tt.filter.Limit = 10
				got, err := repo.List(ctx, tt.filter)
				if err != nil {
					t.Fatalf("List() error = %v", err)
				}
				assertIDs(t, got, tt.want...)
			})
		}
	})

	t.Run("update increments the version", func(t *testing.T) {
		t.Parallel()
		ctx, repo := t.Context(), newRepo(t)

		m := NewMedication(t, "Paracetamol", "500 mg", medication.FormTablet)
		mustCreate(ctx, t, repo, m)

		if err := m.Update(medication.Attributes{Name: "Paracetamol", Dosage: "1 g", Form: "tablet"}, m.UpdatedAt.Add(time.Minute)); err != nil {
			t.Fatalf("Medication.Update() error = %v", err)
		}
		if err := repo.Update(ctx, m); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		got, err := repo.Get(ctx, m.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		want := m
		want.Version = 2
		AssertEqual(t, got, want)
	})

	t.Run("update with a stale version returns ErrVersionMismatch", func(t *testing.T) {
		t.Parallel()
		ctx, repo := t.Context(), newRepo(t)

		m := NewMedication(t, "Paracetamol", "500 mg", medication.FormTablet)
		mustCreate(ctx, t, repo, m)

		// First writer wins and moves the stored version to 2.
		if err := repo.Update(ctx, m); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		// Second writer still holds version 1.
		if err := repo.Update(ctx, m); !errors.Is(err, medication.ErrVersionMismatch) {
			t.Fatalf("Update(stale) error = %v, want ErrVersionMismatch", err)
		}
	})

	t.Run("update unknown id returns ErrNotFound", func(t *testing.T) {
		t.Parallel()
		ctx, repo := t.Context(), newRepo(t)

		m := NewMedication(t, "Paracetamol", "500 mg", medication.FormTablet)
		if err := repo.Update(ctx, m); !errors.Is(err, medication.ErrNotFound) {
			t.Fatalf("Update() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("update into a duplicate returns ErrAlreadyExists", func(t *testing.T) {
		t.Parallel()
		ctx, repo := t.Context(), newRepo(t)

		mustCreate(ctx, t, repo, NewMedication(t, "Paracetamol", "500 mg", medication.FormTablet))
		ibu := NewMedication(t, "Ibuprofen", "200 mg", medication.FormTablet)
		mustCreate(ctx, t, repo, ibu)

		if err := ibu.Update(medication.Attributes{Name: "paracetamol", Dosage: "500 mg", Form: "tablet"}, ibu.UpdatedAt); err != nil {
			t.Fatalf("Medication.Update() error = %v", err)
		}
		if err := repo.Update(ctx, ibu); !errors.Is(err, medication.ErrAlreadyExists) {
			t.Fatalf("Update() error = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("delete removes the medication", func(t *testing.T) {
		t.Parallel()
		ctx, repo := t.Context(), newRepo(t)

		m := NewMedication(t, "Paracetamol", "500 mg", medication.FormTablet)
		mustCreate(ctx, t, repo, m)

		if err := repo.Delete(ctx, m.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if _, err := repo.Get(ctx, m.ID); !errors.Is(err, medication.ErrNotFound) {
			t.Fatalf("Get() after Delete() error = %v, want ErrNotFound", err)
		}
		if err := repo.Delete(ctx, m.ID); !errors.Is(err, medication.ErrNotFound) {
			t.Fatalf("second Delete() error = %v, want ErrNotFound", err)
		}
	})
}

// NewMedication builds a valid medication for tests. Times are UTC and
// truncated to microseconds, matching what the service produces.
func NewMedication(t *testing.T, name, dosage string, form medication.Form) medication.Medication {
	t.Helper()

	now := time.Now().UTC().Truncate(time.Microsecond)
	m, err := medication.New(uuid.NewV7(), medication.Attributes{Name: name, Dosage: dosage, Form: form}, now)
	if err != nil {
		t.Fatalf("medication.New() error = %v", err)
	}

	return m
}

// AssertEqual fails the test if got and want differ. Times are compared
// with time.Time.Equal, which ignores the location: a value read back
// from PostgreSQL may carry a different *time.Location than the one
// that was written.
func AssertEqual(t *testing.T, got, want medication.Medication) {
	t.Helper()

	sameTimes := got.CreatedAt.Equal(want.CreatedAt) && got.UpdatedAt.Equal(want.UpdatedAt)
	got.CreatedAt, got.UpdatedAt = want.CreatedAt, want.UpdatedAt

	if got != want || !sameTimes {
		t.Errorf("medication mismatch:\n  got  %+v\n  want %+v", got, want)
	}
}

func mustCreate(ctx context.Context, t *testing.T, repo medication.Repository, m medication.Medication) {
	t.Helper()

	if err := repo.Create(ctx, m); err != nil {
		t.Fatalf("Create(%q) error = %v", m.Name, err)
	}
}

func assertIDs(t *testing.T, got []medication.Medication, want ...uuid.UUID) {
	t.Helper()

	gotIDs := make([]uuid.UUID, len(got))
	for i, m := range got {
		gotIDs[i] = m.ID
	}

	if len(gotIDs) != len(want) {
		t.Fatalf("got %d medications %v, want %d %v", len(gotIDs), gotIDs, len(want), want)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("medication[%d] = %s, want %s (got %v, want %v)", i, gotIDs[i], want[i], gotIDs, want)
		}
	}
}
