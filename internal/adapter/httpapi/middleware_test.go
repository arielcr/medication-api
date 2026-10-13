package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/arielcr/medication-api/internal/adapter/httpapi"
	"github.com/arielcr/medication-api/internal/adapter/memory"
	"github.com/arielcr/medication-api/internal/medication"
)

func TestRequestID(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)

	// A valid ID sent by the client is propagated.
	rec := api.do(http.MethodGet, "/healthz", "", "X-Request-ID", "abc-123")
	if got := rec.Header().Get("X-Request-ID"); got != "abc-123" {
		t.Errorf("X-Request-ID = %q, want abc-123", got)
	}

	// Missing or unsafe IDs are replaced by a generated UUID.
	for _, sent := range []string{"", "bad id with spaces", strings.Repeat("a", 129), "line\nbreak"} {
		rec := api.do(http.MethodGet, "/healthz", "", "X-Request-ID", sent)
		got := rec.Header().Get("X-Request-ID")
		if _, err := uuid.Parse(got); err != nil {
			t.Errorf("sent %q: X-Request-ID = %q, want a generated UUID", sent, got)
		}
	}
}

func TestAccessLog(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := httpapi.NewHandler(httpapi.Options{
		Service:        medication.NewService(memory.NewRepository()),
		Logger:         logger,
		RequestTimeout: time.Second, // the route must survive every middleware that copies the request
	})
	api := &testAPI{t: t, handler: h}

	api.do(http.MethodGet, "/v1/medications/"+uuid.NewV7().String(), "", "X-Request-ID", "req-1")

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("access log is not a single JSON line: %q (%v)", logs.String(), err)
	}

	want := map[string]any{
		"msg":        "http request",
		"method":     "GET",
		"route":      "GET /v1/medications/{id}",
		"status":     float64(http.StatusNotFound),
		"request_id": "req-1",
	}
	for k, v := range want {
		if entry[k] != v {
			t.Errorf("log[%q] = %v, want %v", k, entry[k], v)
		}
	}
}

// panickingService panics in Get to test the recovery middleware.
type panickingService struct{ httpapi.MedicationService }

func (panickingService) Get(context.Context, uuid.UUID) (medication.Medication, error) {
	panic("boom")
}

func TestRecoverPanics(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := httpapi.NewHandler(httpapi.Options{Service: panickingService{}, Logger: logger})
	api := &testAPI{t: t, handler: h}

	rec := api.do(http.MethodGet, "/v1/medications/"+uuid.NewV7().String(), "")

	assertProblem(t, rec, http.StatusInternalServerError)
	if !strings.Contains(logs.String(), "panic recovered") || !strings.Contains(logs.String(), `"status":500`) {
		t.Errorf("logs = %s, want the panic and a 500 access log", logs.String())
	}
}

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()

	ready := true
	h := httpapi.NewHandler(httpapi.Options{
		Service: medication.NewService(memory.NewRepository()),
		Logger:  discardLogger(),
		Ready: func(context.Context) error {
			if !ready {
				return errors.New("database unreachable")
			}
			return nil
		},
	})
	api := &testAPI{t: t, handler: h}

	assertStatus(t, api.do(http.MethodGet, "/healthz", ""), http.StatusOK)
	assertStatus(t, api.do(http.MethodGet, "/readyz", ""), http.StatusOK)

	ready = false
	rec := api.do(http.MethodGet, "/readyz", "")
	assertStatus(t, rec, http.StatusServiceUnavailable)
	if strings.Contains(rec.Body.String(), "database") {
		t.Errorf("readiness response leaks the reason: %s", rec.Body)
	}

	// Liveness does not depend on the database.
	assertStatus(t, api.do(http.MethodGet, "/healthz", ""), http.StatusOK)
}

func TestUnknownRouteAndMethod(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)

	assertProblem(t, api.do(http.MethodGet, "/nope", ""), http.StatusNotFound)

	rec := api.do(http.MethodPatch, "/v1/medications/"+uuid.NewV7().String(), `{}`)
	assertProblem(t, rec, http.StatusMethodNotAllowed)
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, http.MethodPut) {
		t.Errorf("Allow = %q, want it to list PUT", allow)
	}
}

// slowService blocks until the request context is done, like a database
// query that hangs.
type slowService struct{ httpapi.MedicationService }

func (slowService) Get(ctx context.Context, _ uuid.UUID) (medication.Medication, error) {
	<-ctx.Done()
	return medication.Medication{}, ctx.Err()
}

func TestRequestTimeout(t *testing.T) {
	t.Parallel()

	h := httpapi.NewHandler(httpapi.Options{
		Service:        slowService{},
		Logger:         discardLogger(),
		RequestTimeout: 20 * time.Millisecond,
	})
	api := &testAPI{t: t, handler: h}

	assertProblem(t, api.do(http.MethodGet, "/v1/medications/"+uuid.NewV7().String(), ""), http.StatusServiceUnavailable)
}
