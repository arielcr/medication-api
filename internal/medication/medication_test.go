package medication_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/arielcr/medication-api/internal/medication"
)

func TestNew(t *testing.T) {
	t.Parallel()

	id := uuid.NewV7()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	got, err := medication.New(id, medication.Attributes{
		Name:   "  Paracetamol ",
		Dosage: "500 mg",
		Form:   "Tablet",
	}, now)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}

	want := medication.Medication{
		ID:        id,
		Name:      "Paracetamol",
		Dosage:    "500 mg",
		Form:      medication.FormTablet,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if got != want {
		t.Errorf("New() =\n  %+v\nwant\n  %+v", got, want)
	}
}

func TestNew_CountsCharactersNotBytes(t *testing.T) {
	t.Parallel()

	// "á" takes 2 bytes in UTF-8: 200 characters = 400 bytes.
	name := strings.Repeat("á", 200)

	_, err := medication.New(uuid.NewV7(), medication.Attributes{
		Name:   name,
		Dosage: "500 mg",
		Form:   medication.FormTablet,
	}, time.Now())
	if err != nil {
		t.Fatalf("New() with a 200-character name returned error: %v", err)
	}
}

func TestNew_InvalidAttributes(t *testing.T) {
	t.Parallel()

	valid := medication.Attributes{Name: "Ibuprofen", Dosage: "200 mg", Form: medication.FormCapsule}

	tests := []struct {
		name       string
		attrs      medication.Attributes
		wantFields []string
	}{
		{
			name:       "missing name",
			attrs:      medication.Attributes{Name: "", Dosage: valid.Dosage, Form: valid.Form},
			wantFields: []string{"name"},
		},
		{
			name:       "name with only spaces",
			attrs:      medication.Attributes{Name: "   ", Dosage: valid.Dosage, Form: valid.Form},
			wantFields: []string{"name"},
		},
		{
			name:       "name too long",
			attrs:      medication.Attributes{Name: strings.Repeat("a", 201), Dosage: valid.Dosage, Form: valid.Form},
			wantFields: []string{"name"},
		},
		{
			name:       "name is not valid UTF-8",
			attrs:      medication.Attributes{Name: "Para\xffcetamol", Dosage: valid.Dosage, Form: valid.Form},
			wantFields: []string{"name"},
		},
		{
			name:       "name with a control character",
			attrs:      medication.Attributes{Name: "Para\x00cetamol", Dosage: valid.Dosage, Form: valid.Form},
			wantFields: []string{"name"},
		},
		{
			name:       "dosage with a newline",
			attrs:      medication.Attributes{Name: valid.Name, Dosage: "500\nmg", Form: valid.Form},
			wantFields: []string{"dosage"},
		},
		{
			name:       "missing dosage",
			attrs:      medication.Attributes{Name: valid.Name, Dosage: "", Form: valid.Form},
			wantFields: []string{"dosage"},
		},
		{
			name:       "dosage too long",
			attrs:      medication.Attributes{Name: valid.Name, Dosage: strings.Repeat("9", 101), Form: valid.Form},
			wantFields: []string{"dosage"},
		},
		{
			name:       "dosage is not valid UTF-8",
			attrs:      medication.Attributes{Name: valid.Name, Dosage: "5\xfe mg", Form: valid.Form},
			wantFields: []string{"dosage"},
		},
		{
			name:       "unknown form",
			attrs:      medication.Attributes{Name: valid.Name, Dosage: valid.Dosage, Form: "gummy"},
			wantFields: []string{"form"},
		},
		{
			name:       "everything missing",
			attrs:      medication.Attributes{},
			wantFields: []string{"name", "dosage", "form"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := medication.New(uuid.NewV7(), tt.attrs, time.Now())

			var vErr *medication.ValidationError
			if !errors.As(err, &vErr) {
				t.Fatalf("New() error = %v, want *medication.ValidationError", err)
			}

			gotFields := make([]string, 0, len(vErr.Fields))
			for _, f := range vErr.Fields {
				gotFields = append(gotFields, f.Field)
			}

			if !slices.Equal(gotFields, tt.wantFields) {
				t.Errorf("invalid fields = %v, want %v", gotFields, tt.wantFields)
			}
		})
	}
}

func TestMedication_Update(t *testing.T) {
	t.Parallel()

	m := newTestMedication(t)
	before := m
	later := m.CreatedAt.Add(time.Hour)

	err := m.Update(medication.Attributes{Name: "Ibuprofen", Dosage: "400 mg", Form: "capsule"}, later)
	if err != nil {
		t.Fatalf("Update() unexpected error: %v", err)
	}

	if m.Name != "Ibuprofen" || m.Dosage != "400 mg" || m.Form != medication.FormCapsule {
		t.Errorf("Update() did not apply the new attributes: %+v", m)
	}
	if !m.UpdatedAt.Equal(later) {
		t.Errorf("UpdatedAt = %v, want %v", m.UpdatedAt, later)
	}
	if m.ID != before.ID || !m.CreatedAt.Equal(before.CreatedAt) || m.Version != before.Version {
		t.Errorf("Update() must not change ID, CreatedAt or Version: got %+v, before %+v", m, before)
	}
}

func TestMedication_Update_InvalidLeavesMedicationUnchanged(t *testing.T) {
	t.Parallel()

	m := newTestMedication(t)
	before := m

	err := m.Update(medication.Attributes{Name: "", Dosage: "400 mg", Form: "capsule"}, time.Now())

	var vErr *medication.ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("Update() error = %v, want *medication.ValidationError", err)
	}
	if m != before {
		t.Errorf("Update() modified the medication on error:\n  got  %+v\n  want %+v", m, before)
	}
}

func TestForm_IsValid(t *testing.T) {
	t.Parallel()

	for _, f := range medication.SupportedForms() {
		if !f.IsValid() {
			t.Errorf("Form(%q).IsValid() = false, want true", f)
		}
	}

	if medication.Form("gummy").IsValid() {
		t.Error(`Form("gummy").IsValid() = true, want false`)
	}
}

func TestParseForm(t *testing.T) {
	t.Parallel()

	if got := medication.ParseForm("  Tablet "); got != medication.FormTablet {
		t.Errorf("ParseForm() = %q, want %q", got, medication.FormTablet)
	}
}

func TestValidationError_Error(t *testing.T) {
	t.Parallel()

	err := &medication.ValidationError{Fields: []medication.FieldError{
		{Field: "name", Message: "is required"},
		{Field: "form", Message: "is required"},
	}}

	want := "invalid medication: name is required; form is required"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// newTestMedication returns a valid medication for tests that need one.
func newTestMedication(t *testing.T) medication.Medication {
	t.Helper()

	created := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

	m, err := medication.New(uuid.NewV7(), medication.Attributes{
		Name:   "Paracetamol",
		Dosage: "500 mg",
		Form:   medication.FormTablet,
	}, created)
	if err != nil {
		t.Fatalf("newTestMedication: %v", err)
	}

	return m
}
