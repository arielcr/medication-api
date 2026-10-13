package medication

import (
	"slices"
	"strings"
)

// Form is the physical presentation of a medication (tablet, liquid, ...).
type Form string

// Supported medication forms.
const (
	FormTablet    Form = "tablet"
	FormCapsule   Form = "capsule"
	FormLiquid    Form = "liquid"
	FormInjection Form = "injection"
	FormCream     Form = "cream"
	FormInhaler   Form = "inhaler"
)

// supportedForms is the single source of truth for valid forms.
var supportedForms = []Form{
	FormTablet,
	FormCapsule,
	FormLiquid,
	FormInjection,
	FormCream,
	FormInhaler,
}

// SupportedForms returns every valid form. It returns a copy so callers
// cannot modify the package's internal list.
func SupportedForms() []Form {
	return slices.Clone(supportedForms)
}

// ParseForm normalizes s (trims spaces, lowercases it) and returns it as
// a Form. The result may still be invalid: check it with IsValid.
func ParseForm(s string) Form {
	return Form(strings.ToLower(strings.TrimSpace(s)))
}

// IsValid reports whether f is one of the supported forms.
func (f Form) IsValid() bool {
	return slices.Contains(supportedForms, f)
}

// formList returns the supported forms as a comma-separated string,
// used in validation messages.
func formList() string {
	names := make([]string, len(supportedForms))
	for i, f := range supportedForms {
		names[i] = string(f)
	}

	return strings.Join(names, ", ")
}
