package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// readinessTimeout bounds how long GET /readyz waits for its checks, so
// a hanging database never makes the probe itself hang.
const readinessTimeout = 2 * time.Second

// healthHandler serves the probes used by orchestrators (Kubernetes,
// ECS, Docker) and load balancers.
type healthHandler struct {
	ready  ReadinessCheck
	logger *slog.Logger
}

type healthResponse struct {
	Status string `json:"status"`
}

// liveness handles GET /healthz (liveness): "is the process alive?". It does
// not check dependencies on purpose: if the database is down, restarting
// this process would not help, and the orchestrator might restart every
// replica at once.
func (h *healthHandler) liveness(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, h.logger, http.StatusOK, healthResponse{Status: "ok"})
}

// readiness handles GET /readyz (readiness): "should I receive traffic?".
// It returns 503 while the database is unreachable or the server is
// shutting down, so the load balancer stops routing requests here.
func (h *healthHandler) readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	if err := h.ready(ctx); err != nil {
		// The reason is logged, not returned: probe endpoints are often
		// reachable from outside and should not reveal internals.
		h.logger.WarnContext(r.Context(), "readiness check failed", slog.Any("error", err))
		writeJSON(w, r, h.logger, http.StatusServiceUnavailable, healthResponse{Status: "unavailable"})

		return
	}

	writeJSON(w, r, h.logger, http.StatusOK, healthResponse{Status: "ok"})
}
