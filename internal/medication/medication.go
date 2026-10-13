// Package medication contains the domain model and business rules
// for medication records. It knows nothing about HTTP or databases.
package medication

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
	"uuid"
)

const (
	maxNameLength   = 200
	maxDosageLength = 100
)

// Medication is a medication record.
type Medication struct {
	ID     uuid.UUID
	Name   string
	Dosage string
	Form   Form
	// Version starts at 1 and is incremented by the repository on every
	// successful update. It drives optimistic concurrency control and is
	// exposed to HTTP clients as the ETag.
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Attributes are the fields a client can set when creating or
// updating a medication.
type Attributes struct {
	Name   string
	Dosage string
	Form   Form
}

// New builds a valid Medication. The caller provides the ID and the
// current time so this function stays deterministic and easy to test.
func New(id uuid.UUID, attrs Attributes, now time.Time) (Medication, error) {
	attrs = attrs.normalized()
	if err := attrs.validate(); err != nil {
		return Medication{}, err
	}

	return Medication{
		ID:        id,
		Name:      attrs.Name,
		Dosage:    attrs.Dosage,
		Form:      attrs.Form,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// Update replaces the editable fields. If attrs is invalid, it returns
// an error and leaves the medication unchanged.
//
// Update does not touch Version: the repository increments it when the
// change is actually persisted.
func (m *Medication) Update(attrs Attributes, now time.Time) error {
	attrs = attrs.normalized()
	if err := attrs.validate(); err != nil {
		return err
	}

	m.Name = attrs.Name
	m.Dosage = attrs.Dosage
	m.Form = attrs.Form
	m.UpdatedAt = now

	return nil
}

// normalized returns a copy with surrounding spaces removed and the
// form lowercased, so "  Tablet " and "tablet" are treated the same.
func (a Attributes) normalized() Attributes {
	return Attributes{
		Name:   strings.TrimSpace(a.Name),
		Dosage: strings.TrimSpace(a.Dosage),
		Form:   ParseForm(string(a.Form)),
	}
}

// validate checks every field and reports all problems together.
func (a Attributes) validate() error {
	var fields []FieldError

	if msg := checkText(a.Name, maxNameLength); msg != "" {
		fields = append(fields, FieldError{Field: "name", Message: msg})
	}
	if msg := checkText(a.Dosage, maxDosageLength); msg != "" {
		fields = append(fields, FieldError{Field: "dosage", Message: msg})
	}

	switch {
	case a.Form == "":
		fields = append(fields, FieldError{Field: "form", Message: "is required"})
	case !a.Form.IsValid():
		fields = append(fields, FieldError{Field: "form", Message: "must be one of: " + formList()})
	}

	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}

	return nil
}

// checkText validates a required free-text field and returns what is
// wrong with it, or "" if it is valid. The first failing rule wins (an
// empty value is not also reported as "too short").
func checkText(value string, maxLength int) string {
	switch {
	case value == "":
		return "is required"
	case !utf8.ValidString(value):
		return "must be valid UTF-8 text"
	case strings.ContainsFunc(value, unicode.IsControl):
		// Control characters (NUL, newlines, escape codes...) have no
		// place in a name, break log lines, and PostgreSQL rejects NUL.
		return "must not contain control characters"
	case utf8.RuneCountInString(value) > maxLength:
		return fmt.Sprintf("must be at most %d characters", maxLength)
	default:
		return ""
	}
}
