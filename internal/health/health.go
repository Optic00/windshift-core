// Package health provides the process and dependency probes used by
// orchestrators and the scratch container image.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const readinessTimeout = 2 * time.Second

// Pinger is the database capability required by the readiness probe.
type Pinger interface {
	PingContext(context.Context) error
}

// Handler serves the liveness and readiness contracts.
type Handler struct {
	database Pinger
}

type livenessResponse struct {
	Status string `json:"status"`
}

type readinessResponse struct {
	Status   string `json:"status"`
	Database string `json:"database"`
}

// NewHandler creates health handlers backed by the supplied database.
func NewHandler(database Pinger) *Handler {
	return &Handler{database: database}
}

// Liveness reports whether the HTTP process and router are serving requests.
//
// @Summary      Check server liveness
// @Description  Reports whether the Windshift HTTP process and router are serving requests. Public; no authentication required.
// @Tags         operations
// @Produce      json
// @Success      200  {object}  livenessResponse
// @Router       /healthz [get]
func (h *Handler) Liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, livenessResponse{Status: "ok"})
}

// Readiness reports whether the application can reach its database.
//
// @Summary      Check server readiness
// @Description  Reports whether Windshift can serve requests and reach its database. Public; no authentication required.
// @Tags         operations
// @Produce      json
// @Success      200  {object}  readinessResponse
// @Failure      503  {object}  readinessResponse
// @Router       /readyz [get]
func (h *Handler) Readiness(w http.ResponseWriter, r *http.Request) {
	if h.database == nil {
		writeUnavailable(w)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()
	if err := h.database.PingContext(ctx); err != nil {
		writeUnavailable(w)
		return
	}

	writeJSON(w, http.StatusOK, readinessResponse{
		Status:   "ready",
		Database: "ok",
	})
}

func writeUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, readinessResponse{
		Status:   "not_ready",
		Database: "unavailable",
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Probe performs the HTTP check used by the container's healthcheck command.
func Probe(ctx context.Context, target string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return fmt.Errorf("create readiness request: %w", err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("request readiness endpoint: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness endpoint returned %s", response.Status)
	}
	return nil
}

// DefaultProbeTarget returns the in-container readiness URL for the configured
// HTTP port and optional public context path.
func DefaultProbeTarget(port, contextPath string) string {
	if port == "" {
		port = "8080"
	}
	contextPath = strings.TrimSpace(contextPath)
	if contextPath == "" || contextPath == "/" {
		contextPath = ""
	} else {
		contextPath = "/" + strings.Trim(contextPath, "/")
	}
	return "http://127.0.0.1:" + port + contextPath + "/readyz"
}
