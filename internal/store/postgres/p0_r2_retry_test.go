package postgres_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func TestP0R2RetryPolicyMatrixPreservesHistory(t *testing.T) {
	for _, kind := range []string{"eligible-hot", "eligible-warm", "environment-reference-only", "missing-watch", "disabled-watch", "cold-watch", "missing-operator-policy", "wrong-cadence", "child-scope", "mirror-off", "missing-topology", "partial-topology", "wrong-collector", "future-retry", "inactive-root", "failure-two", "failure-three"} {
		t.Run(kind, func(t *testing.T) {
			st, ctx := p3Store(t)
			now := p3Time()
			scope := "/"
			if kind == "child-scope" {
				scope = "/child"
			}
			interval := int64(30)
			watch := state.ScopeWatchState{RootID: p3RootActive, ScopeKey: scope, WatchState: state.WatchHot, CadenceClass: "SOURCE_MIRROR", EffectiveIntervalSeconds: &interval, NextDueAt: &now, Priority: state.PriorityHigh, SourceSet: []state.WatchSource{state.WatchSourceOperatorPolicy}}
			switch kind {
			case "eligible-warm":
				watch.WatchState = state.WatchWarm
			case "disabled-watch":
				watch.WatchState = state.WatchDisabled
				watch.EffectiveIntervalSeconds = nil
				watch.NextDueAt = nil
			case "cold-watch":
				watch.WatchState = state.WatchCold
				watch.EffectiveIntervalSeconds = nil
				watch.NextDueAt = nil
			case "missing-operator-policy":
				watch.SourceSet = []state.WatchSource{state.WatchSourceAdaptivePolicy}
			case "wrong-cadence":
				watch.CadenceClass = "ORDINARY"
			}
			if kind != "missing-watch" {
				if _, err := st.CreateWatch(ctx, watch); err != nil {
					t.Fatal(err)
				}
			}
			config := `{"path":"/","mirror_presence":true,"topology_token_env":"R2_NOT_POPULATED"}`
			collector := "openlist"
			switch kind {
			case "environment-reference-only":
				config = `{"path":"/","mirror_presence":true,"topology_username_env":"R2_USER_NOT_POPULATED","topology_password_env":"R2_PASS_NOT_POPULATED"}`
			case "mirror-off":
				config = `{"path":"/","mirror_presence":false,"topology_token_env":"TOKEN"}`
			case "missing-topology":
				config = `{"path":"/","mirror_presence":true}`
			case "partial-topology":
				config = `{"path":"/","mirror_presence":true,"topology_username_env":"USER"}`
			case "wrong-collector":
				collector = "rclone"
			}
			if err := st.UpsertAdapterConfig(ctx, p3RootActive, postgres.AdapterConfig{CollectorKind: collector, Config: []byte(config)}); err != nil {
				t.Fatal(err)
			}
			source := state.SourcePollSchedule
			if kind == "missing-watch" {
				// A schedule claim requires a watch. Seed ordinary manual work,
				// then prove missing watch never opts it into mirror recovery.
				source = state.SourceManualOperator
			}
			if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, scope, state.PriorityHigh, source, state.ReasonPossibleChange, now, nil)); err != nil {
				t.Fatal(err)
			}
			failures := 1
			if kind == "failure-two" {
				failures = 2
			}
			if kind == "failure-three" {
				failures = 3
			}
			var failed state.DirtyScopeWork
			for i := 0; i < failures; i++ {
				claim, err := p3Claim(t, st, ctx, p3RootActive, scope, now)
				if err != nil {
					t.Fatal(err)
				}
				due := now.Add(time.Minute)
				failed, err = st.CompleteFailure(ctx, p3RootActive, scope, *claim.ClaimedSignalSeq, state.ErrorInternal, &due, now)
				if err != nil {
					t.Fatal(err)
				}
				if i+1 < failures {
					if _, err := st.RetryReady(ctx, p3RootActive, scope, failed.Version, due); err != nil {
						t.Fatal(err)
					}
				}
				now = due
			}
			if kind == "inactive-root" {
				if _, err := st.TransitionRootLifecycle(ctx, p3RootActive, "DEPRECATED"); err != nil {
					t.Fatal(err)
				}
			}
			at := now
			if kind == "future-retry" {
				at = now.Add(-time.Second)
			}
			eligible := kind == "eligible-hot" || kind == "eligible-warm" || kind == "environment-reference-only" || kind == "failure-two"
			for cycle := 0; cycle < 4; cycle++ {
				rows, err := st.ListDueRetryWork(ctx, at, 10)
				if err != nil {
					t.Fatal(err)
				}
				if (len(rows) == 1) != eligible || len(rows) > 1 {
					t.Fatalf("eligibility %s=%v", kind, rows)
				}
				after, err := st.GetWork(ctx, p3RootActive, scope)
				if err != nil || !reflect.DeepEqual(after, failed) {
					t.Fatalf("read-only selection changed work/history: %v", err)
				}
				t.Logf("R2 retry=%s cycle=%d eligible=%t state=%s attempts=%d failures=%d signal=%d version=%d", kind, cycle, eligible, after.WorkState, after.AttemptCount, after.ConsecutiveFailures, after.SignalSeq, after.Version)
			}
			if eligible {
				ready, err := st.RetryReady(ctx, p3RootActive, scope, failed.Version, at)
				if err != nil {
					t.Fatal(err)
				}
				if ready.WorkState != state.WorkPending || ready.AttemptCount != failed.AttemptCount || ready.ConsecutiveFailures != failed.ConsecutiveFailures || ready.SignalSeq != failed.SignalSeq {
					t.Fatal("recovery reset history")
				}
				if _, err := st.RetryReady(ctx, p3RootActive, scope, failed.Version, at); err == nil {
					t.Fatal("stale CAS allowed second recovery")
				}
			}
			var count int
			if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource`).Scan(&count); err != nil || count != 0 {
				t.Fatal("retry selector created canonical state")
			}
			if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_journal_event WHERE event_type LIKE 'resource-%'`).Scan(&count); err != nil || count != 0 {
				t.Fatal("retry selector invented resource events")
			}
		})
	}
}
