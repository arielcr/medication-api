package httpapi

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
	"uuid"

	"github.com/arielcr/medication-api/internal/medication"
)

// medicationRequest is the JSON body of POST and PUT requests.
//
// Request and response types (DTOs) live in the HTTP layer, not in the
// domain: the JSON contract can evolve without touching business rules,
// and the domain never needs struct tags.
type medicationRequest struct {
	Name   string `json:"name"`
	Dosage string `json:"dosage"`
	Form   string `json:"form"`
}

func (r medicationRequest) attributes() medication.Attributes {
	return medication.Attributes{Name: r.Name, Dosage: r.Dosage, Form: medication.Form(r.Form)}
}

// medicationResponse is the JSON representation of a medication.
type medicationResponse struct {
	ID        uuid.UUID `json:"id"` // uuid.UUID implements encoding.TextMarshaler: encoded as a string
	Name      string    `json:"name"`
	Dosage    string    `json:"dosage"`
	Form      string    `json:"form"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toResponse(m medication.Medication) medicationResponse {
	return medicationResponse{
		ID:        m.ID,
		Name:      m.Name,
		Dosage:    m.Dosage,
		Form:      string(m.Form),
		Version:   m.Version,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

// listResponse is the JSON body of GET /v1/medications.
type listResponse struct {
	Data []medicationResponse `json:"data"`
	// NextCursor is omitted on the last page.
	NextCursor string `json:"next_cursor,omitempty"`
}

const (
	maxNameFilterLength = 200
)

// medicationHandler groups the /v1/medications handlers and their
// dependencies.
type medicationHandler struct {
	svc    MedicationService
	logger *slog.Logger
}

// create handles POST /v1/medications.
func (h *medicationHandler) create(w http.ResponseWriter, r *http.Request) {
	var req medicationRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	m, err := h.svc.Create(r.Context(), req.attributes())
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	w.Header().Set("Location", "/v1/medications/"+m.ID.String())
	writeMedication(w, r, h.logger, http.StatusCreated, m)
}

// get handles GET /v1/medications/{id}.
func (h *medicationHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	m, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	writeMedication(w, r, h.logger, http.StatusOK, m)
}

// list handles GET /v1/medications?limit=&cursor=&form=&name=.
func (h *medicationHandler) list(w http.ResponseWriter, r *http.Request) {
	filter, err := parseListFilter(r)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	page, err := h.svc.List(r.Context(), filter)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	resp := listResponse{Data: make([]medicationResponse, 0, len(page.Items))} // [] instead of null when empty
	for _, m := range page.Items {
		resp.Data = append(resp.Data, toResponse(m))
	}
	if page.HasMore {
		resp.NextCursor = encodeCursor(page.NextCursor)
	}

	writeJSON(w, r, h.logger, http.StatusOK, resp)
}

// update handles PUT /v1/medications/{id}. PUT replaces every editable
// field. Clients can send If-Match with the ETag they last saw to make
// the update conditional (optimistic concurrency control).
func (h *medicationHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	expectedVersion, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	var req medicationRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	m, err := h.svc.Update(r.Context(), id, req.attributes(), expectedVersion)
	if err != nil {
		// Without If-Match the client did not ask for a precondition, so a
		// concurrent modification is a plain 409 Conflict, not a 412.
		if expectedVersion == medication.AnyVersion && errors.Is(err, medication.ErrVersionMismatch) {
			err = &requestError{
				status: http.StatusConflict,
				detail: "the medication was modified by another request; fetch it again and retry",
			}
		}
		writeError(w, r, h.logger, err)
		return
	}

	writeMedication(w, r, h.logger, http.StatusOK, m)
}

// remove handles DELETE /v1/medications/{id}. (Named remove because
// delete is a Go builtin.)
func (h *medicationHandler) remove(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// writeMedication writes a single medication with its ETag header.
func writeMedication(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, m medication.Medication) {
	w.Header().Set("ETag", etag(m.Version))
	writeJSON(w, r, logger, status, toResponse(m))
}

// pathID parses the {id} path wildcard.
func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.UUID{}, &requestError{status: http.StatusBadRequest, detail: "id must be a valid UUID"}
	}

	return id, nil
}

// parseListFilter validates the query parameters of the list endpoint.
// Invalid query parameters are a client error in the URL, so they are
// reported as 400 Bad Request with one entry per invalid parameter.
func parseListFilter(r *http.Request) (medication.ListFilter, error) {
	q := r.URL.Query()
	var filter medication.ListFilter
	var fields []medication.FieldError

	if v := q.Get("limit"); v != "" {
		limit, err := strconv.Atoi(v)
		if err != nil || limit < 1 || limit > medication.MaxPageSize {
			fields = append(fields, medication.FieldError{
				Field:   "limit",
				Message: "must be an integer between 1 and " + strconv.Itoa(medication.MaxPageSize),
			})
		}
		filter.Limit = limit
	}

	if v := q.Get("cursor"); v != "" {
		after, err := decodeCursor(v)
		if err != nil {
			fields = append(fields, medication.FieldError{Field: "cursor", Message: "is not a valid cursor"})
		}
		filter.After = after
	}

	if v := q.Get("form"); v != "" {
		filter.Form = medication.ParseForm(v)
		if !filter.Form.IsValid() {
			fields = append(fields, medication.FieldError{Field: "form", Message: "is not a supported form"})
		}
	}

	if v := q.Get("name"); v != "" {
		switch {
		case !utf8.ValidString(v) || strings.ContainsFunc(v, unicode.IsControl):
			fields = append(fields, medication.FieldError{Field: "name", Message: "must be valid text without control characters"})
		case utf8.RuneCountInString(v) > maxNameFilterLength:
			fields = append(fields, medication.FieldError{
				Field:   "name",
				Message: "must be at most " + strconv.Itoa(maxNameFilterLength) + " characters",
			})
		}
		filter.Name = v
	}

	if len(fields) > 0 {
		return medication.ListFilter{}, &requestError{
			status: http.StatusBadRequest,
			detail: "invalid query parameters",
			fields: fields,
		}
	}

	return filter, nil
}

// Cursors are opaque to clients: the base64url encoding of the last ID of
// the previous page. Clients must not build or parse them, which lets the
// server change the pagination strategy later without breaking them.
func encodeCursor(id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(id[:])
}

func decodeCursor(s string) (uuid.UUID, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return uuid.UUID{}, err
	}
	if len(b) != len(uuid.UUID{}) {
		return uuid.UUID{}, errors.New("cursor has the wrong length")
	}

	return uuid.UUID(b), nil
}

// etag returns the strong ETag for a medication version, e.g. "3".
func etag(version int64) string {
	return `"` + strconv.FormatInt(version, 10) + `"`
}

// parseIfMatch converts an If-Match header into the expected version.
// An absent header or "*" means "no precondition" (AnyVersion).
func parseIfMatch(header string) (int64, error) {
	header = strings.TrimSpace(header)
	if header == "" || header == "*" {
		return medication.AnyVersion, nil
	}

	invalid := &requestError{
		status: http.StatusBadRequest,
		detail: `If-Match must be a single strong ETag returned by this API, for example "3"`,
	}

	// Weak ETags (W/"3") cannot be used with If-Match (RFC 9110 §13.1.1),
	// and we only support a single ETag, not a list.
	unquoted, ok := strings.CutPrefix(header, `"`)
	if !ok {
		return 0, invalid
	}
	unquoted, ok = strings.CutSuffix(unquoted, `"`)
	if !ok {
		return 0, invalid
	}

	version, err := strconv.ParseInt(unquoted, 10, 64)
	if err != nil || version < 1 {
		return 0, invalid
	}

	return version, nil
}
