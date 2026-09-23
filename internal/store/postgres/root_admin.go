package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
)

// RootLifecycleResult reports a frozen root-lifecycle transition.
type RootLifecycleResult struct {
	From       domain.RootLifecycleState
	To         domain.RootLifecycleState
	Generation int64
	EventSeq   *int64
}

// TransitionRootLifecycle applies a root lifecycle transition inside one
// per-root serialized transaction using the frozen generation + journal
// semantics (doc D Sec 5.1). root_id is immutable and never reused.
func (s *Store) TransitionRootLifecycle(ctx context.Context, rootID string, next domain.RootLifecycleState) (RootLifecycleResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RootLifecycleResult{}, err
	}
	defer tx.Rollback(ctx)

	var (
		currentGen int64
		currentLc  string
	)
	if err := tx.QueryRow(ctx,
		`SELECT current_generation, lifecycle_state FROM index_root WHERE root_id = $1::uuid FOR UPDATE`,
		rootID).Scan(&currentGen, &currentLc); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RootLifecycleResult{}, ErrNotFound
		}
		return RootLifecycleResult{}, err
	}
	from := domain.RootLifecycleState(currentLc)
	if from == next {
		return RootLifecycleResult{From: from, To: next, Generation: currentGen}, nil
	}
	if !from.CanTransitionTo(next) {
		return RootLifecycleResult{}, fmt.Errorf("invalid root lifecycle transition %s -> %s", from, next)
	}

	newGen := currentGen + 1
	if _, err := tx.Exec(ctx,
		`UPDATE index_root SET lifecycle_state = $2, current_generation = $3, updated_at = now()
		  WHERE root_id = $1::uuid`, rootID, string(next), newGen); err != nil {
		return RootLifecycleResult{}, err
	}
	summary, _ := json.Marshal(map[string]any{"root_lifecycle": string(next)})
	if _, err := tx.Exec(ctx,
		`INSERT INTO index_generation(root_id, generation_number, summary) VALUES ($1::uuid, $2, $3)`,
		rootID, newGen, summary); err != nil {
		return RootLifecycleResult{}, err
	}

	var eventSeq *int64
	if et, ok := rootLifecycleEvent(next); ok {
		seq, err := s.NextEventSeq(ctx, tx, rootID)
		if err != nil {
			return RootLifecycleResult{}, err
		}
		intra, err := s.NextIntraGenerationSeq(ctx, tx, rootID, newGen)
		if err != nil {
			return RootLifecycleResult{}, err
		}
		payload, _ := json.Marshal(map[string]any{"root_lifecycle": string(next)})
		if err := s.AppendJournalEvent(ctx, tx, domain.JournalEvent{
			RootID: rootID, EventSeq: seq, GenerationNumber: newGen,
			IntraGenerationSeq: intra, EventType: et, Payload: payload,
		}); err != nil {
			return RootLifecycleResult{}, err
		}
		eventSeq = &seq
	}

	if err := tx.Commit(ctx); err != nil {
		return RootLifecycleResult{}, err
	}
	return RootLifecycleResult{From: from, To: next, Generation: newGen, EventSeq: eventSeq}, nil
}

func rootLifecycleEvent(next domain.RootLifecycleState) (domain.EventType, bool) {
	switch next {
	case domain.RootDeprecated:
		return domain.EventRootDeprecated, true
	case domain.RootDeleted:
		return domain.EventRootDeleted, true
	default:
		return "", false
	}
}

// RootPolicy is the persisted per-root reconcile policy (index_root_config).
type RootPolicy struct {
	RemovalGracePeriod            time.Duration
	MoveRecognitionHorizon        time.Duration
	MinConsecutiveCompleteMissing int
	MinIndependentConfirmations   int
}

// ReconcileConfig converts persisted policy to a Kernel reconcile config.
func (p RootPolicy) ReconcileConfig() reconcile.Config {
	return reconcile.Config{
		RemovalGracePeriod:            p.RemovalGracePeriod,
		MoveRecognitionHorizon:        p.MoveRecognitionHorizon,
		MinConsecutiveCompleteMissing: p.MinConsecutiveCompleteMissing,
		MinIndependentConfirmations:   p.MinIndependentConfirmations,
	}
}

// UpsertRootPolicy persists per-root reconcile policy (grace >= horizon enforced
// by the schema CHECK; Validate is applied by RootReconcileConfig).
func (s *Store) UpsertRootPolicy(ctx context.Context, rootID string, p RootPolicy) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO index_root_config(root_id, removal_grace_period, move_recognition_horizon,
		        min_consecutive_complete_missing, min_independent_confirmations)
		 VALUES ($1::uuid, $2 * interval '1 second', $3 * interval '1 second', $4, $5)
		 ON CONFLICT (root_id) DO UPDATE SET
		     removal_grace_period = EXCLUDED.removal_grace_period,
		     move_recognition_horizon = EXCLUDED.move_recognition_horizon,
		     min_consecutive_complete_missing = EXCLUDED.min_consecutive_complete_missing,
		     min_independent_confirmations = EXCLUDED.min_independent_confirmations`,
		rootID, p.RemovalGracePeriod.Seconds(), p.MoveRecognitionHorizon.Seconds(),
		p.MinConsecutiveCompleteMissing, p.MinIndependentConfirmations)
	return err
}

// GetRootPolicy loads per-root reconcile policy (ErrNotFound if absent).
func (s *Store) GetRootPolicy(ctx context.Context, rootID string) (RootPolicy, error) {
	var (
		p          RootPolicy
		graceSec   float64
		horizonSec float64
	)
	err := s.pool.QueryRow(ctx,
		`SELECT extract(epoch from removal_grace_period)::float8,
		        extract(epoch from move_recognition_horizon)::float8,
		        min_consecutive_complete_missing, min_independent_confirmations
		   FROM index_root_config WHERE root_id = $1::uuid`, rootID).
		Scan(&graceSec, &horizonSec, &p.MinConsecutiveCompleteMissing, &p.MinIndependentConfirmations)
	if errors.Is(err, pgx.ErrNoRows) {
		return RootPolicy{}, ErrNotFound
	}
	if err != nil {
		return RootPolicy{}, err
	}
	p.RemovalGracePeriod = time.Duration(graceSec * float64(time.Second))
	p.MoveRecognitionHorizon = time.Duration(horizonSec * float64(time.Second))
	if p.MinConsecutiveCompleteMissing < 1 {
		p.MinConsecutiveCompleteMissing = 1
	}
	if p.MinIndependentConfirmations < 1 {
		p.MinIndependentConfirmations = 1
	}
	return p, nil
}

// RootReconcileConfig returns a validated reconcile config from persisted policy.
func (s *Store) RootReconcileConfig(ctx context.Context, rootID string) (reconcile.Config, error) {
	p, err := s.GetRootPolicy(ctx, rootID)
	if err != nil {
		return reconcile.Config{}, err
	}
	c := p.ReconcileConfig()
	if err := c.Validate(); err != nil {
		return reconcile.Config{}, err
	}
	return c, nil
}

// AdapterConfig is the provider-neutral root adapter binding.
type AdapterConfig struct {
	CollectorKind string
	Config        []byte // jsonb
}

// UpsertAdapterConfig persists the root's collector adapter binding.
func (s *Store) UpsertAdapterConfig(ctx context.Context, rootID string, a AdapterConfig) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO index_root_adapter_config(root_id, collector_kind, config, updated_at)
		 VALUES ($1::uuid, $2, $3, now())
		 ON CONFLICT (root_id) DO UPDATE SET collector_kind = EXCLUDED.collector_kind,
		     config = EXCLUDED.config, updated_at = now()`,
		rootID, a.CollectorKind, jsonOrEmpty(a.Config))
	return err
}

// GetAdapterConfig loads the root's collector adapter binding.
func (s *Store) GetAdapterConfig(ctx context.Context, rootID string) (AdapterConfig, error) {
	var (
		a   AdapterConfig
		cfg []byte
	)
	err := s.pool.QueryRow(ctx,
		`SELECT collector_kind, config FROM index_root_adapter_config WHERE root_id = $1::uuid`,
		rootID).Scan(&a.CollectorKind, &cfg)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdapterConfig{}, ErrNotFound
	}
	if err != nil {
		return AdapterConfig{}, err
	}
	a.Config = cfg
	return a, nil
}
