package postgres

import (
	"context"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

// RecoverStaleInflight re-coalesces persisted IN_FLIGHT rows for a root back into
// a runnable state. It is NOT wired into daemon startup in P3.
//
// It is deterministic and idempotent: after the first run, a second run is a
// no-op for that root (no IN_FLIGHT rows remain). A crash is never counted as a
// provider failure.
func (s *Store) RecoverStaleInflight(ctx context.Context, rootID string, now time.Time) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// Lock order Root -> Work: the root lock must precede any work-row lock.
	lifecycle, err := lockRootForUpdate(ctx, tx, rootID)
	if err != nil {
		return 0, err
	}
	active := activeFromLifecycle(lifecycle)

	rows, err := tx.Query(ctx, `
		SELECT scope_key FROM index_dirty_scope_work
		WHERE root_id=$1 AND work_state='IN_FLIGHT'
		ORDER BY scope_key ASC
		FOR UPDATE`, rootID)
	if err != nil {
		return 0, err
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return 0, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}

	recovered := 0
	for _, key := range keys {
		wk, found, err := loadWorkForUpdate(ctx, tx, rootID, key)
		if err != nil {
			return recovered, err
		}
		if !found || wk.WorkState != state.WorkInFlight {
			continue
		}
		srcs, err := state.UnionTriggerSources(wk.ClaimedSourceSet, wk.PendingSourceSet)
		if err != nil {
			return recovered, err
		}
		reasons, err := state.UnionTriggerReasons(wk.ClaimedReasonSet, wk.PendingReasonSet)
		if err != nil {
			return recovered, err
		}
		wk.PendingSourceSet = srcs
		wk.PendingReasonSet = reasons
		wk.PendingPriority = mergedPriority(wk.PendingPriority, derefPriority(wk.ClaimedPriority))
		wk.PendingFirstSeenAt = state.MinTimePtr(wk.PendingFirstSeenAt, wk.ClaimedFirstSeenAt, wk.LastAttemptStartedAt)
		// Eligibility: a claimed signal with no barrier (claimed_not_before NULL)
		// was immediately runnable, so the merged item must NOT be delayed by a
		// post-claim future not_before. Otherwise keep the earliest barrier.
		if wk.ClaimedNotBefore == nil {
			wk.PendingNotBefore = nil
		} else {
			wk.PendingNotBefore = state.MinTimePtr(wk.ClaimedNotBefore, wk.PendingNotBefore)
		}
		releaseClaim(&wk)
		if active {
			wk.WorkState = state.WorkPending
		} else {
			wk.WorkState = state.WorkSuspended
		}
		if _, err := writeWork(ctx, tx, wk); err != nil {
			return recovered, err
		}
		recovered++
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return recovered, nil
}
