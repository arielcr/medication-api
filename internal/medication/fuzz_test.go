package medication_test

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
	"uuid"

	"github.com/arielcr/medication-api/internal/medication"
)

// FuzzNew feeds random input to New and checks its invariants: it must
// never panic, and whenever it succeeds the result must be normalized and
// within the length limits. Run it with:
//
//	go test -fuzz=FuzzNew -fuzztime=30s ./internal/medication/
func FuzzNew(f *testing.F) {
	f.Add("Paracetamol", "500 mg", "Tablet")
	f.Add("", "", "")
	f.Add("  á  ", "1", "LIQUID")
	f.Add(strings.Repeat("x", 201), "1 mg", "gummy")

	f.Fuzz(func(t *testing.T, name, dosage, form string) {
		m, err := medication.New(uuid.NewV7(), medication.Attributes{
			Name:   name,
			Dosage: dosage,
			Form:   medication.Form(form),
		}, time.Now())
		if err != nil {
			var vErr *medication.ValidationError
			if !errors.As(err, &vErr) || len(vErr.Fields) == 0 {
				t.Fatalf("New() returned a non-validation error: %v", err)
			}
			return
		}

		if m.Name != strings.TrimSpace(m.Name) || m.Name == "" || !utf8.ValidString(m.Name) || utf8.RuneCountInString(m.Name) > 200 {
			t.Errorf("invalid name accepted: %q", m.Name)
		}
		if m.Dosage != strings.TrimSpace(m.Dosage) || m.Dosage == "" || !utf8.ValidString(m.Dosage) || utf8.RuneCountInString(m.Dosage) > 100 {
			t.Errorf("invalid dosage accepted: %q", m.Dosage)
		}
		if strings.ContainsFunc(m.Name+m.Dosage, unicode.IsControl) {
			t.Errorf("control characters accepted: %q / %q", m.Name, m.Dosage)
		}
		if !m.Form.IsValid() {
			t.Errorf("invalid form accepted: %q", m.Form)
		}
		if m.Version != 1 {
			t.Errorf("Version = %d, want 1", m.Version)
		}
	})
}
