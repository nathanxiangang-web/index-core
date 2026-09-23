// Package httpapi is the Gate 3 read-only HTTP transport. It exposes the frozen
// Query Contract over standard-library net/http and never holds Store mutation
// capabilities (Gate 3 P6). Transport structs stay separate from Domain structs.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
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
	Ready   func() bool
	Version string
}

// New builds the read-only HTTP transport.
func New(deps Deps) *http.Server {
	h := &handler{deps: deps}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)

	mux.HandleFunc("GET /v1/roots", h.listRoots)
	mux.HandleFunc("GET /v1/roots/{root_id}", h.getRoot)
	mux.HandleFunc("GET /v1/roots/{root_id}/status", h.rootStatus)
	mux.HandleFunc("GET /v1/roots/{root_id}/resources", h.listResources)
	mux.HandleFunc("GET /v1/roots/{root_id}/active", h.listActive)
	mux.HandleFunc("GET /v1/roots/{root_id}/removed", h.listRemoved)
	mux.HandleFunc("GET /v1/roots/{root_id}/resolve", h.resolvePath)
	mux.HandleFunc("GET /v1/roots/{root_id}/journal", h.readJournal)
	mux.HandleFunc("GET /v1/resources/{resource_id}", h.getResource)

	return &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
}

type handler struct{ deps Deps }

func (h *handler) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "alive", "version": h.deps.Version})
}

func (h *handler) readyz(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := h.deps.Pool.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "database_unreachable")
		return
	}
	applied, required, missing, err := postgres.SchemaStatus(ctx, h.deps.Pool)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "schema_check_failed")
		return
	}
	if len(missing) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "not_ready", "reason": "schema_incompatible", "applied": applied, "required": required, "missing": missing})
		return
	}
	if h.deps.Ready != nil && !h.deps.Ready() {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "runtime_not_initialized")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "schema_applied": applied})
}

func (h *handler) listRoots(w http.ResponseWriter, r *http.Request) {
	roots, err := h.deps.Query.ListRoots(r.Context(), boolParam(r, "include_deprecated"), boolParam(r, "include_deleted"))
	if err != nil {
		h.fail(w, err)
		return
	}
	out := make([]rootDTO, 0, len(roots))
	for _, v := range roots {
		out = append(out, toRootDTO(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *handler) getRoot(w http.ResponseWriter, r *http.Request) {
	v, err := h.deps.Query.GetRoot(r.Context(), r.PathValue("root_id"), readOptions(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	if v == nil {
		writeError(w, http.StatusNotFound, "not_found", "root not visible")
		return
	}
	writeJSON(w, http.StatusOK, toRootDTO(*v))
}

func (h *handler) rootStatus(w http.ResponseWriter, r *http.Request) {
	st, err := h.deps.Query.RootStatus(r.Context(), r.PathValue("root_id"), readOptions(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rootStatusDTO{RootID: st.RootID, LifecycleState: string(st.LifecycleState),
		CurrentGeneration: st.CurrentGeneration, LastAppliedAdmissionSeq: st.LastAppliedAdmissionSeq})
}

func (h *handler) listResources(w http.ResponseWriter, r *http.Request) {
	var parent *string
	if p := r.URL.Query().Get("parent_id"); p != "" {
		parent = &p
	}
	cur, ok := h.cursor(w, r)
	if !ok {
		return
	}
	page, err := h.deps.Query.ListResources(r.Context(), r.PathValue("root_id"), parent, readOptions(r), cur, limitParam(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	h.writePage(w, page)
}

func (h *handler) listActive(w http.ResponseWriter, r *http.Request) {
	cur, ok := h.cursor(w, r)
	if !ok {
		return
	}
	page, err := h.deps.Query.ListActivePage(r.Context(), r.PathValue("root_id"), cur, limitParam(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	h.writePage(w, page)
}

func (h *handler) listRemoved(w http.ResponseWriter, r *http.Request) {
	cur, ok := h.cursor(w, r)
	if !ok {
		return
	}
	page, err := h.deps.Query.ListRemovedPage(r.Context(), r.PathValue("root_id"), cur, limitParam(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	h.writePage(w, page)
}

func (h *handler) resolvePath(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "path is required")
		return
	}
	res, err := h.deps.Query.ResolvePath(r.Context(), r.PathValue("root_id"), path, readOptions(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	items := make([]resourceDTO, 0, len(res.Matches))
	for _, v := range res.Matches {
		items = append(items, toResourceDTO(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": items, "ambiguous": res.Ambiguous})
}

func (h *handler) readJournal(w http.ResponseWriter, r *http.Request) {
	after := int64Param(r, "after_seq", 0)
	events, err := h.deps.Query.ReadJournal(r.Context(), r.PathValue("root_id"), after, limitParam(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	items := make([]journalDTO, 0, len(events))
	for _, v := range events {
		items = append(items, toJournalDTO(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) getResource(w http.ResponseWriter, r *http.Request) {
	v, err := h.deps.Query.GetResource(r.Context(), r.PathValue("resource_id"), readOptions(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	if v == nil {
		writeError(w, http.StatusNotFound, "not_found", "resource not visible")
		return
	}
	writeJSON(w, http.StatusOK, toResourceDTO(*v))
}

func (h *handler) writePage(w http.ResponseWriter, page query.ResourcePage) {
	items := make([]resourceDTO, 0, len(page.Items))
	for _, v := range page.Items {
		items = append(items, toResourceDTO(v))
	}
	writeJSON(w, http.StatusOK, pageDTO{Items: items, Next: encodeCursor(page.Next)})
}

func (h *handler) cursor(w http.ResponseWriter, r *http.Request) (*query.Cursor, bool) {
	cur, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "cursor is not decodable")
		return nil, false
	}
	return cur, true
}

// fail maps Query errors to stable transport errors.
func (h *handler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, query.ErrStaleCursor):
		writeError(w, http.StatusConflict, "stale_cursor", "cursor generation is no longer current")
	case errors.Is(err, postgres.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	default:
		if h.deps.Logger != nil {
			h.deps.Logger.Warn("query transport error", "error_class", "query")
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "query failed")
	}
}

func readOptions(r *http.Request) query.ReadOptions {
	return query.ReadOptions{
		IncludeRemoved:        boolParam(r, "include_removed"),
		IncludeDeprecatedRoot: boolParam(r, "include_deprecated_root"),
		IncludeDeletedRoot:    boolParam(r, "include_deleted_root"),
	}
}

func boolParam(r *http.Request, name string) bool {
	v := r.URL.Query().Get(name)
	return v == "1" || v == "true"
}

func limitParam(r *http.Request) int {
	return int(int64Param(r, "limit", 0))
}

func int64Param(r *http.Request, name string, def int64) int64 {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": code, "message": message})
}
