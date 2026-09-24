// Package hintapi is the P9 trusted Hint HTTP transport. It is a separate,
// loopback-only, bearer-authenticated listener that maps one strict JSON request
// to exactly one P8 Mutation Hint ingestion call.
//
// It holds no Store, pool, SQL, Scan, P4/P5/P6, or Kernel capability: only the
// narrow P8 Ingester, a token, and a logger.
package hintapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalhint"
)

const (
	// MaxBodyBytes is the hard P9 request body limit.
	MaxBodyBytes = 4096
	// MaxInFlight is the hard cap on concurrent authenticated ingestion calls.
	MaxInFlight = 4
	// IngestTimeout bounds one P8 IngestOne call.
	IngestTimeout = 5 * time.Second

	HintPath = "/internal/v1/mutation-hints"
)

// ingestTimeout is the per-call P8 timeout. It is a variable only so tests can
// shorten it; production always uses IngestTimeout.
var ingestTimeout = IngestTimeout

// Ingester is the narrow P8 ingress interface. The transport receives only this
// capability, never Store/pool/SQL/Scan/P4/P5/P6/Kernel.
type Ingester interface {
	IngestOne(ctx context.Context, req incrementalhint.Request) (state.DirtyScopeWork, error)
}

// Deps are the Hint transport dependencies.
type Deps struct {
	Ingester Ingester
	Token    string
	Logger   *slog.Logger
}

// Server is the dedicated Hint HTTP transport.
type Server struct {
	deps Deps
	http *http.Server
	sem  chan struct{}
	wg   sync.WaitGroup
}

// New builds the Hint transport. It fails closed on missing dependencies or a
// too-short token.
func New(deps Deps) (*Server, error) {
	if deps.Ingester == nil {
		return nil, errors.New("hintapi: ingester is required")
	}
	if len(deps.Token) < 32 {
		return nil, errors.New("hintapi: token must be at least 32 bytes")
	}
	s := &Server{deps: deps, sem: make(chan struct{}, MaxInFlight)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+HintPath, s.handleIngest)
	s.http = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return s, nil
}

// Serve serves the transport on an already-bound listener.
func (s *Server) Serve(l net.Listener) error {
	return s.http.Serve(l)
}

// Shutdown stops accepting new requests and waits for in-flight requests until
// ctx is done (standard http.Server.Shutdown semantics).
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// WaitHandlers blocks until every in-flight ingestion handler has returned. It is
// the writer-lock safety gate: the caller must not release the writer advisory
// lock while a handler may still call P8.
func (s *Server) WaitHandlers() { s.wg.Wait() }

type hintRequest struct {
	RootID   string `json:"root_id"`
	ScopeKey string `json:"scope_key"`
	Reason   string `json:"reason"`
}

type hintResponse struct {
	Status    string `json:"status"`
	RootID    string `json:"root_id"`
	ScopeKey  string `json:"scope_key"`
	WorkState string `json:"work_state"`
	SignalSeq int64  `json:"signal_seq"`
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if !s.authenticate(w, r) {
		return
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}

	body := http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	var req hintRequest
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request_too_large")
			return
		}
		writeErr(w, http.StatusBadRequest, "invalid_request")
		return
	}
	// Reject trailing/multiple JSON values: exactly one request object.
	if dec.More() {
		writeErr(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid_request")
		return
	}

	p8req, ok := validateRequest(req)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_request")
		return
	}

	// Bounded ingress concurrency: no queue, immediate 429 when full.
	select {
	case s.sem <- struct{}{}:
	default:
		writeErr(w, http.StatusTooManyRequests, "busy")
		return
	}
	s.wg.Add(1)
	defer func() {
		<-s.sem
		s.wg.Done()
	}()

	ctx, cancel := context.WithTimeout(r.Context(), ingestTimeout)
	defer cancel()
	work, err := s.deps.Ingester.IngestOne(ctx, p8req)
	if err != nil {
		// Never echo Store/P8/PostgreSQL error text to the caller.
		if s.deps.Logger != nil {
			s.deps.Logger.Warn("hint ingest failed", "error_class", "ingest")
		}
		writeErr(w, http.StatusServiceUnavailable, "ingest_unavailable")
		return
	}
	writeJSON(w, http.StatusAccepted, hintResponse{
		Status:    "accepted",
		RootID:    p8req.RootID,
		ScopeKey:  p8req.ScopeKey,
		WorkState: string(work.WorkState),
		SignalSeq: work.SignalSeq,
	})
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) bool {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) ||
		subtle.ConstantTimeCompare([]byte(auth[len(prefix):]), []byte(s.deps.Token)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

// validateRequest mirrors the P8 request contract for a stable 400 response.
// P8 validates again and remains authoritative. No path.Clean, no file->parent
// reinterpretation.
func validateRequest(req hintRequest) (incrementalhint.Request, bool) {
	if strings.TrimSpace(req.RootID) == "" {
		return incrementalhint.Request{}, false
	}
	if err := state.ValidateScopeKey(req.ScopeKey); err != nil {
		return incrementalhint.Request{}, false
	}
	reason := state.TriggerReason(strings.TrimSpace(req.Reason))
	if !allowedReason(reason) {
		return incrementalhint.Request{}, false
	}
	return incrementalhint.Request{RootID: req.RootID, ScopeKey: req.ScopeKey, Reason: reason}, true
}

func allowedReason(r state.TriggerReason) bool {
	switch r {
	case "", state.ReasonPossibleChange, state.ReasonDeleteHint,
		state.ReasonMoveUncertain, state.ReasonMetadataUncertain:
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, code string) {
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}
	writeJSON(w, status, map[string]string{"error": code})
}
