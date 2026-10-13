package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"uuid"

	"github.com/arielcr/medication-api/internal/adapter/httpapi"
	"github.com/arielcr/medication-api/internal/adapter/memory"
	"github.com/arielcr/medication-api/internal/medication"
)

// These tests exercise the real stack (router, middleware, handlers,
// service, in-memory repository) through HTTP, like a client would.
// Only the database is replaced, so they are fast and deterministic.

type testAPI struct {
	t       *testing.T
	handler http.Handler
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()

	svc := medication.NewService(memory.NewRepository())
	h := httpapi.NewHandler(httpapi.Options{Service: svc, Logger: discardLogger()})

	return &testAPI{t: t, handler: h}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// do sends a request and returns the recorded response.
func (a *testAPI) do(method, target, body string, headers ...string) *httptest.ResponseRecorder {
	a.t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(a.t.Context(), method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)

	return rec
}

type medicationJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Dosage    string `json:"dosage"`
	Form      string `json:"form"`
	Version   int64  `json:"version"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type listJSON struct {
	Data       []medicationJSON `json:"data"`
	NextCursor string           `json:"next_cursor"`
}

type problemJSON struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	Instance  string `json:"instance"`
	RequestID string `json:"request_id"`
	Errors    []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"errors"`
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()

	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}

	return v
}

func (a *testAPI) create(name, dosage, form string) medicationJSON {
	a.t.Helper()

	body := `{"name":` + strconv.Quote(name) + `,"dosage":` + strconv.Quote(dosage) + `,"form":` + strconv.Quote(form) + `}`
	rec := a.do(http.MethodPost, "/v1/medications", body)
	if rec.Code != http.StatusCreated {
		a.t.Fatalf("create %s: status = %d, body = %s", name, rec.Code, rec.Body)
	}

	return decode[medicationJSON](a.t, rec)
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()

	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, want, rec.Body)
	}
}

func assertProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, fields ...string) problemJSON {
	t.Helper()

	assertStatus(t, rec, status)
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}

	p := decode[problemJSON](t, rec)
	if p.Status != status || p.Type != "about:blank" || p.Title == "" || p.Detail == "" {
		t.Errorf("problem = %+v, want status %d with type, title and detail", p, status)
	}
	if p.RequestID == "" || p.RequestID != rec.Header().Get("X-Request-ID") {
		t.Errorf("problem request_id = %q, want it to match the X-Request-ID header %q", p.RequestID, rec.Header().Get("X-Request-ID"))
	}

	got := make([]string, 0, len(p.Errors))
	for _, e := range p.Errors {
		got = append(got, e.Field)
	}
	if strings.Join(got, ",") != strings.Join(fields, ",") {
		t.Errorf("problem fields = %v, want %v", got, fields)
	}

	return p
}

func TestCreateMedication(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	rec := api.do(http.MethodPost, "/v1/medications", `{"name":"  Paracetamol ","dosage":"500 mg","form":"Tablet"}`)

	assertStatus(t, rec, http.StatusCreated)
	m := decode[medicationJSON](t, rec)

	if m.Name != "Paracetamol" || m.Dosage != "500 mg" || m.Form != "tablet" || m.Version != 1 {
		t.Errorf("created medication = %+v, want normalized fields and version 1", m)
	}
	if _, err := uuid.Parse(m.ID); err != nil {
		t.Errorf("id %q is not a UUID: %v", m.ID, err)
	}
	if loc := rec.Header().Get("Location"); loc != "/v1/medications/"+m.ID {
		t.Errorf("Location = %q, want /v1/medications/%s", loc, m.ID)
	}
	if etag := rec.Header().Get("ETag"); etag != `"1"` {
		t.Errorf("ETag = %s, want \"1\"", etag)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestCreateMedication_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantFields  []string
	}{
		{"malformed JSON", `{"name":`, "application/json", http.StatusBadRequest, nil},
		{"empty body", ``, "application/json", http.StatusBadRequest, nil},
		{"unknown field", `{"name":"A","dosage":"1 mg","form":"tablet","price":3}`, "application/json", http.StatusBadRequest, nil},
		{"wrong JSON type", `{"name":123,"dosage":"1 mg","form":"tablet"}`, "application/json", http.StatusBadRequest, nil},
		{"two JSON objects", `{"name":"A","dosage":"1 mg","form":"tablet"}{}`, "application/json", http.StatusBadRequest, nil},
		{"wrong content type", `name=A`, "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType, nil},
		{"validation errors", `{"name":"","dosage":"","form":"gummy"}`, "application/json", http.StatusUnprocessableEntity, []string{"name", "dosage", "form"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := newTestAPI(t)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/medications", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			rec := httptest.NewRecorder()
			api.handler.ServeHTTP(rec, req)

			assertProblem(t, rec, tt.wantStatus, tt.wantFields...)
		})
	}
}

func TestCreateMedication_BodyTooLarge(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	huge := `{"name":"` + strings.Repeat("a", 2<<20) + `","dosage":"1 mg","form":"tablet"}`

	assertProblem(t, api.do(http.MethodPost, "/v1/medications", huge), http.StatusRequestEntityTooLarge)
}

func TestCreateMedication_Duplicate(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	api.create("Paracetamol", "500 mg", "tablet")

	rec := api.do(http.MethodPost, "/v1/medications", `{"name":"PARACETAMOL","dosage":"500 mg","form":"tablet"}`)
	assertProblem(t, rec, http.StatusConflict)
}

func TestGetMedication(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	created := api.create("Ibuprofen", "200 mg", "capsule")

	rec := api.do(http.MethodGet, "/v1/medications/"+created.ID, "")
	assertStatus(t, rec, http.StatusOK)
	if got := decode[medicationJSON](t, rec); got != created {
		t.Errorf("GET = %+v, want %+v", got, created)
	}
	if etag := rec.Header().Get("ETag"); etag != `"1"` {
		t.Errorf("ETag = %s, want \"1\"", etag)
	}

	assertProblem(t, api.do(http.MethodGet, "/v1/medications/not-a-uuid", ""), http.StatusBadRequest)
	assertProblem(t, api.do(http.MethodGet, "/v1/medications/"+uuid.NewV7().String(), ""), http.StatusNotFound)
}

func TestListMedications_Pagination(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	a := api.create("Aspirin", "100 mg", "tablet")
	b := api.create("Ibuprofen", "200 mg", "tablet")
	c := api.create("Naproxen", "250 mg", "tablet")

	first := decode[listJSON](t, api.do(http.MethodGet, "/v1/medications?limit=2", ""))
	if len(first.Data) != 2 || first.Data[0].ID != a.ID || first.Data[1].ID != b.ID || first.NextCursor == "" {
		t.Fatalf("first page = %+v, want [Aspirin, Ibuprofen] and a cursor", first)
	}

	second := decode[listJSON](t, api.do(http.MethodGet, "/v1/medications?limit=2&cursor="+first.NextCursor, ""))
	if len(second.Data) != 1 || second.Data[0].ID != c.ID || second.NextCursor != "" {
		t.Fatalf("second page = %+v, want [Naproxen] and no cursor", second)
	}
}

func TestListMedications_Filters(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	api.create("Paracetamol", "500 mg", "tablet")
	syrup := api.create("Paracetamol Kids", "120 mg/5 ml", "liquid")
	api.create("Ibuprofen", "200 mg", "tablet")

	got := decode[listJSON](t, api.do(http.MethodGet, "/v1/medications?name=paracetamol&form=LIQUID", ""))
	if len(got.Data) != 1 || got.Data[0].ID != syrup.ID {
		t.Fatalf("filtered list = %+v, want only the syrup", got)
	}

	empty := api.do(http.MethodGet, "/v1/medications?name=zzz", "")
	assertStatus(t, empty, http.StatusOK)
	if !strings.Contains(empty.Body.String(), `"data":[]`) {
		t.Errorf("empty list body = %s, want \"data\":[] (not null)", empty.Body)
	}
}

func TestListMedications_InvalidQuery(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	tests := []struct {
		query  string
		fields []string
	}{
		{"limit=0", []string{"limit"}},
		{"limit=101", []string{"limit"}},
		{"limit=abc", []string{"limit"}},
		{"cursor=%21%21", []string{"cursor"}},
		{"cursor=AAAA", []string{"cursor"}},
		{"form=gummy", []string{"form"}},
		{"name=" + strings.Repeat("a", 201), []string{"name"}},
		{"name=a%00b", []string{"name"}},
		{"name=%FF", []string{"name"}},
		{"limit=-1&form=gummy", []string{"limit", "form"}},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			t.Parallel()
			assertProblem(t, api.do(http.MethodGet, "/v1/medications?"+tt.query, ""), http.StatusBadRequest, tt.fields...)
		})
	}
}

func TestUpdateMedication(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	created := api.create("Paracetamol", "500 mg", "tablet")
	path := "/v1/medications/" + created.ID
	body := `{"name":"Paracetamol","dosage":"1 g","form":"tablet"}`

	// Unconditional update.
	rec := api.do(http.MethodPut, path, body)
	assertStatus(t, rec, http.StatusOK)
	updated := decode[medicationJSON](t, rec)
	if updated.Dosage != "1 g" || updated.Version != 2 || updated.CreatedAt != created.CreatedAt {
		t.Errorf("updated = %+v, want dosage 1 g, version 2, same created_at", updated)
	}
	if etag := rec.Header().Get("ETag"); etag != `"2"` {
		t.Errorf("ETag = %s, want \"2\"", etag)
	}

	// Conditional update with the current ETag succeeds...
	assertStatus(t, api.do(http.MethodPut, path, body, "If-Match", `"2"`), http.StatusOK)

	// ...and with a stale ETag fails with 412 Precondition Failed.
	assertProblem(t, api.do(http.MethodPut, path, body, "If-Match", `"2"`), http.StatusPreconditionFailed)

	// "*" means "any version".
	assertStatus(t, api.do(http.MethodPut, path, body, "If-Match", `*`), http.StatusOK)
}

func TestUpdateMedication_Errors(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	created := api.create("Paracetamol", "500 mg", "tablet")
	api.create("Ibuprofen", "200 mg", "tablet")
	path := "/v1/medications/" + created.ID
	valid := `{"name":"Paracetamol","dosage":"1 g","form":"tablet"}`

	tests := []struct {
		name    string
		path    string
		body    string
		headers []string
		status  int
		fields  []string
	}{
		{"invalid id", "/v1/medications/123", valid, nil, http.StatusBadRequest, nil},
		{"unknown id", "/v1/medications/" + uuid.NewV7().String(), valid, nil, http.StatusNotFound, nil},
		{"weak etag", path, valid, []string{"If-Match", `W/"1"`}, http.StatusBadRequest, nil},
		{"unquoted etag", path, valid, []string{"If-Match", `1`}, http.StatusBadRequest, nil},
		{"etag list", path, valid, []string{"If-Match", `"1", "2"`}, http.StatusBadRequest, nil},
		{"invalid body", path, `{"name":""}`, nil, http.StatusUnprocessableEntity, []string{"name", "dosage", "form"}},
		{"duplicate", path, `{"name":"ibuprofen","dosage":"200 mg","form":"tablet"}`, nil, http.StatusConflict, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertProblem(t, api.do(http.MethodPut, tt.path, tt.body, tt.headers...), tt.status, tt.fields...)
		})
	}
}

// conflictingService simulates another request winning the race between
// our read and our write: the domain reports a version mismatch even
// though the client sent no If-Match.
type conflictingService struct{ httpapi.MedicationService }

func (conflictingService) Update(context.Context, uuid.UUID, medication.Attributes, int64) (medication.Medication, error) {
	return medication.Medication{}, fmt.Errorf("update: %w", medication.ErrVersionMismatch)
}

func TestUpdateMedication_ConcurrentModificationWithoutIfMatchIs409(t *testing.T) {
	t.Parallel()

	h := httpapi.NewHandler(httpapi.Options{Service: conflictingService{}, Logger: discardLogger()})
	api := &testAPI{t: t, handler: h}

	rec := api.do(http.MethodPut, "/v1/medications/"+uuid.NewV7().String(), `{"name":"A","dosage":"1 mg","form":"tablet"}`)
	assertProblem(t, rec, http.StatusConflict)
}

func TestDeleteMedication(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	created := api.create("Paracetamol", "500 mg", "tablet")
	path := "/v1/medications/" + created.ID

	rec := api.do(http.MethodDelete, path, "")
	assertStatus(t, rec, http.StatusNoContent)
	if rec.Body.Len() != 0 {
		t.Errorf("204 response has a body: %q", rec.Body)
	}

	assertProblem(t, api.do(http.MethodGet, path, ""), http.StatusNotFound)
	assertProblem(t, api.do(http.MethodDelete, path, ""), http.StatusNotFound)
	assertProblem(t, api.do(http.MethodDelete, "/v1/medications/x", ""), http.StatusBadRequest)
}

// failingService returns an unexpected error from every method.
type failingService struct{ httpapi.MedicationService }

func (failingService) Get(context.Context, uuid.UUID) (medication.Medication, error) {
	return medication.Medication{}, errors.New("pq: password authentication failed for user admin")
}

func TestUnexpectedErrorsDoNotLeakDetails(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := httpapi.NewHandler(httpapi.Options{Service: failingService{}, Logger: logger})
	api := &testAPI{t: t, handler: h}

	rec := api.do(http.MethodGet, "/v1/medications/"+uuid.NewV7().String(), "")
	p := assertProblem(t, rec, http.StatusInternalServerError)

	if strings.Contains(p.Detail, "password") {
		t.Errorf("500 response leaks internal error: %q", p.Detail)
	}
	if !strings.Contains(logs.String(), "password authentication failed") {
		t.Errorf("the internal error must be logged for operators; logs = %s", logs.String())
	}
}
