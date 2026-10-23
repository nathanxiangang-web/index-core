package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func firstDiscoveryProof(t *testing.T, st *postgres.Store, ctx context.Context, root, scope string, now time.Time) {
	t.Helper()
	if err := st.UpsertAdapterConfig(ctx, root, postgres.AdapterConfig{CollectorKind: "alist", Config: []byte(`{"base_url":"http://fixture.invalid","path":"/source"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeSignal(ctx, p3Signal(root, "/", state.SourceManualOperator, state.ReasonManualVerify, now)); err != nil {
		t.Fatal(err)
	}
	claim, err := p3Claim(t, st, ctx, root, "/", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CompleteSuccessWithChildren(ctx, root, "/", *claim.ClaimedSignalSeq, []string{scope}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Pool().Exec(ctx, `INSERT INTO index_canonical_resource(resource_id,root_id,introduced_at_generation,last_confirmed_generation,resource_presence,removal_evidence_state,canonical_path,is_dir,name) VALUES($1,$2,0,0,'PRESENT','NONE',$3,true,'fresh')`, fmt.Sprintf("13400000-0000-4000-8000-%012d", 1), root, "/source"+scope); err != nil {
		t.Fatal(err)
	}
}
func TestIssue134FirstDiscoveryProofGuardsAndCAS(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	firstDiscoveryProof(t, st, ctx, p3RootActive, "/fresh", now)
	w, found, err := st.NextFirstDiscoveryWork(ctx, now, nil)
	if err != nil || !found || w.ScopeKey != "/fresh" || w.AttemptCount != 0 {
		t.Fatal(w, found, err)
	}
	if _, found, err = st.NextFirstDiscoveryWork(ctx, now, []string{p3RootActive}); err != nil || found {
		t.Fatal("per-root exclusion", found, err)
	}
	// Readers must not claim or rewrite priority/version.
	again, _ := st.GetWork(ctx, p3RootActive, "/fresh")
	if again.Version != w.Version || again.WorkState != state.WorkPending || *again.PendingPriority != state.PriorityNormal {
		t.Fatal("selection mutated work")
	}
	for _, sql := range []string{
		`UPDATE index_canonical_resource SET resource_presence='REMOVED',removal_evidence_state='REMOVAL_CANDIDATE'`,
		`UPDATE index_canonical_resource SET is_dir=false`,
		`UPDATE index_canonical_resource SET removal_evidence_state='REMOVAL_CANDIDATE'`,
		`UPDATE index_canonical_resource SET canonical_path='/another/fresh'`,
		`UPDATE index_dirty_scope_work SET last_verified_at=NULL WHERE scope_key='/'`,
		`UPDATE index_root SET lifecycle_state='DEPRECATED' WHERE root_id=$1`,
	} {
		if _, err = st.Pool().Exec(ctx, sql, p3Args(sql, p3RootActive)...); err != nil {
			t.Fatal(err)
		}
		if _, found, err = st.NextFirstDiscoveryWork(ctx, now, nil); err != nil || found {
			t.Fatal("invalid proof admitted", sql, found, err)
		}
		if _, err = st.Pool().Exec(ctx, `UPDATE index_canonical_resource SET resource_presence='PRESENT',removal_evidence_state='NONE',is_dir=true,canonical_path='/source/fresh'; UPDATE index_dirty_scope_work SET last_verified_at='2026-09-24T12:00:00Z' WHERE scope_key='/'; UPDATE index_root SET lifecycle_state='ACTIVE' WHERE lifecycle_state='DEPRECATED' AND root_id='00000000-0000-0000-0000-00000000a001'`); err != nil {
			t.Fatal(err)
		}
	}
	// A concurrent signal fences the chosen version. No scan is authorized.
	if _, err = st.MergeSignal(ctx, p3Signal(p3RootActive, "/fresh", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimWork(ctx, w.RootID, w.ScopeKey, w.Version, now); err == nil {
		t.Fatal("stale CAS accepted")
	}
	current, _ := st.GetWork(ctx, w.RootID, w.ScopeKey)
	claim, err := st.ClaimWork(ctx, w.RootID, w.ScopeKey, current.Version, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CompleteSuccess(ctx, claim.RootID, claim.ScopeKey, *claim.ClaimedSignalSeq, now); err != nil {
		t.Fatal(err)
	}
	if _, err = st.MergeSignal(ctx, p3Signal(p3RootActive, "/fresh", state.SourceRecovery, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	if _, found, err = st.NextFirstDiscoveryWork(ctx, now, nil); err != nil || found {
		t.Fatal("repeat verified child received first-scan credit", found, err)
	}
}

func p3Args(sql, root string) []any {
	if sql == `UPDATE index_root SET lifecycle_state='DEPRECATED' WHERE root_id=$1` {
		return []any{root}
	}
	return nil
}

func TestIssue134NestedAmbiguityEligibilityAndConcurrentClaim(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	firstDiscoveryProof(t, st, ctx, p3RootActive, "/fresh", now)
	parent, err := p3Claim(t, st, ctx, p3RootActive, "/fresh", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteSuccessWithChildren(ctx, p3RootActive, "/fresh", *parent.ClaimedSignalSeq, []string{"/fresh/nested"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO index_canonical_resource(resource_id,root_id,introduced_at_generation,last_confirmed_generation,resource_presence,removal_evidence_state,canonical_path,is_dir,name) VALUES('13400000-0000-4000-8000-000000000002',$1,0,0,'PRESENT','NONE','/source/fresh/nested',true,'nested')`, p3RootActive); err != nil {
		t.Fatal(err)
	}
	assertSelected := func(want bool) state.DirtyScopeWork {
		t.Helper()
		w, found, err := st.NextFirstDiscoveryWork(ctx, now, nil)
		if err != nil || found != want || (found && w.ScopeKey != "/fresh/nested") {
			t.Fatal("nested proof", w.ScopeKey, found, err)
		}
		return w
	}
	assertSelected(true)
	// A PRESENT file at the same parent/child path is ambiguity, not proof.
	for _, path := range []string{"/source/fresh", "/source/fresh/nested"} {
		if _, err := st.Pool().Exec(ctx, `INSERT INTO index_canonical_resource(resource_id,root_id,introduced_at_generation,last_confirmed_generation,resource_presence,removal_evidence_state,canonical_path,is_dir,name) VALUES('13400000-0000-4000-8000-000000000003',$1,0,0,'PRESENT','NONE',$2,false,'ambiguous')`, p3RootActive, path); err != nil {
			t.Fatal(err)
		}
		assertSelected(false)
		if _, err := st.Pool().Exec(ctx, `DELETE FROM index_canonical_resource WHERE resource_id='13400000-0000-4000-8000-000000000003'`); err != nil {
			t.Fatal(err)
		}
	}
	for _, change := range []string{
		`pending_not_before='2026-09-25T12:00:00Z'`,
		`attempt_count=1`,
		`pending_priority='HIGH'`,
	} {
		if _, err := st.Pool().Exec(ctx, `UPDATE index_dirty_scope_work SET `+change+` WHERE scope_key='/fresh/nested'`); err != nil {
			t.Fatal(err)
		}
		assertSelected(false)
		if _, err := st.Pool().Exec(ctx, `UPDATE index_dirty_scope_work SET pending_not_before=NULL,attempt_count=0,pending_priority='NORMAL' WHERE scope_key='/fresh/nested'`); err != nil {
			t.Fatal(err)
		}
	}
	w := assertSelected(true)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := st.ClaimWork(ctx, w.RootID, w.ScopeKey, w.Version, now)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatal("concurrent planning authorized multiple claims", winners)
	}
}
