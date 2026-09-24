// Package incrementalhint is the P8 trusted in-process Mutation Hint ingestion
// prototype. One IngestOne call validates a canonical directory scope and a
// mutation-hint reason, then durably merges exactly one MUTATION_HINT DirtySignal
// through the accepted Store.MergeSignal primitive.
//
// It is ingress only: no provider traversal, no execution, no Canonical write,
// no second writer, and no transport.
package incrementalhint

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

// Store is the narrow slice of the accepted P3 operational-state primitive the
// service needs. *postgres.Store satisfies it. P8 never reimplements the merge
// logic.
type Store interface {
	MergeSignal(ctx context.Context, sig state.DirtySignal) (state.DirtyScopeWork, error)
}

// Request is one trusted hint. Source, priority, seen time, and not-before are
// P8-owned and cannot be supplied by the caller.
type Request struct {
	RootID   string
	ScopeKey string
	Reason   state.TriggerReason
}

// Service ingests one Mutation Hint at a time. It is callable only from trusted
// in-process code already inside the active single-writer runtime boundary, and
// it never acquires the global writer advisory lock itself.
type Service struct {
	store Store
	now   func() time.Time
}

// New builds the ingestion service. now defaults to time.Now().UTC().
func New(store Store, now func() time.Time) (*Service, error) {
	if store == nil {
		return nil, errors.New("incrementalhint: store is required")
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{store: store, now: now}, nil
}

// IngestOne validates the request and merges exactly one MUTATION_HINT signal.
//
// It maps the accepted hint to a DirtySignal with Source=MUTATION_HINT,
// Priority=HIGH, SeenAt=accepted_at, and NotBefore=accepted_at, leaving all
// work-state transitions to the existing Store.MergeSignal semantics.
func (s *Service) IngestOne(ctx context.Context, req Request) (state.DirtyScopeWork, error) {
	if strings.TrimSpace(req.RootID) == "" {
		return state.DirtyScopeWork{}, errors.New("incrementalhint: root_id is required")
	}
	// Strict canonical scope: no path.Clean, no silent reinterpretation.
	if err := state.ValidateScopeKey(req.ScopeKey); err != nil {
		return state.DirtyScopeWork{}, fmt.Errorf("incrementalhint: invalid scope: %w", err)
	}
	reason, err := normalizeHintReason(req.Reason)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}

	acceptedAt := s.now()
	sig := state.DirtySignal{
		RootID:    req.RootID,
		ScopeKey:  req.ScopeKey,
		Source:    state.SourceMutationHint,
		Reason:    reason,
		Priority:  state.PriorityHigh,
		SeenAt:    acceptedAt,
		NotBefore: &acceptedAt,
	}
	return s.store.MergeSignal(ctx, sig)
}

// normalizeHintReason accepts only the four mutation-hint reasons; an empty
// reason defaults to POSSIBLE_CHANGE.
func normalizeHintReason(r state.TriggerReason) (state.TriggerReason, error) {
	switch r {
	case "":
		return state.ReasonPossibleChange, nil
	case state.ReasonPossibleChange, state.ReasonDeleteHint,
		state.ReasonMoveUncertain, state.ReasonMetadataUncertain:
		return r, nil
	default:
		return "", fmt.Errorf("incrementalhint: reason %q is not a mutation-hint reason", r)
	}
}
