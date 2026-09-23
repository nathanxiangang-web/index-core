// Package httpapi is the Gate 3 read-only HTTP transport. It exposes the frozen
// Query Contract over standard-library net/http and never holds Store mutation
// capabilities (Gate 3 P6). Transport structs stay separate from Domain structs.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// Deps are the read-only dependencies the transport may hold.
type Deps struct {
	Pool    *pgxpool.Pool
	Query   query.Reader
	Logger  *slog.Logger
	Ready   func() bool // optional runtime-ready probe
	Version string
}

// New builds the read-only HTTP transport.
func New(deps Deps) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "alive", "version": deps.Version})
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if err := deps.Pool.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "reason": "database_unreachable"})
			return
		}
		applied, required, missing, err := postgres.SchemaStatus(ctx, deps.Pool)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "reason": "schema_check_failed"})
			return
		}
		if len(missing) > 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "not_ready", "reason": "schema_incompatible",
				"applied": applied, "required": required, "missing": missing,
			})
			return
		}
		if deps.Ready != nil && !deps.Ready() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "reason": "runtime_not_initialized"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "schema_applied": applied})
	})

	// /v1 read endpoints are implemented in Gate 3 P6.
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "query transport is implemented in P6"})
	})

	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
