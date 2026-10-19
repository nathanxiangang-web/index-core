package postgres_test

import (
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func TestMirrorInternalRootRetryIsOptInDueAndBounded(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	interval := int64(30)
	watch, err := st.CreateWatch(ctx, state.ScopeWatchState{RootID: p3RootActive, ScopeKey: "/", WatchState: state.WatchHot, CadenceClass: "SOURCE_MIRROR", EffectiveIntervalSeconds: &interval, NextDueAt: &now, Priority: state.PriorityHigh, SourceSet: []state.WatchSource{state.WatchSourceOperatorPolicy}})
	if err != nil {
		t.Fatal(err)
	}
	setConfig := func(raw string) {
		t.Helper()
		if err := st.UpsertAdapterConfig(ctx, p3RootActive, postgres.AdapterConfig{CollectorKind: "openlist", Config: []byte(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	setConfig(`{"path":"/","mirror_presence":true,"topology_username_env":"TOPO_USER","topology_password_env":"TOPO_PASS"}`)
	if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, "/", state.PriorityHigh, state.SourcePollSchedule, state.ReasonPossibleChange, now, nil)); err != nil {
		t.Fatal(err)
	}
	for failure := 1; failure <= 3; failure++ {
		claim, err := p3Claim(t, st, ctx, p3RootActive, "/", now)
		if err != nil {
			t.Fatal(err)
		}
		due := now.Add(time.Minute)
		failed, err := st.CompleteFailure(ctx, p3RootActive, "/", *claim.ClaimedSignalSeq, state.ErrorInternal, &due, now)
		if err != nil {
			t.Fatal(err)
		}
		if failed.ConsecutiveFailures != int64(failure) {
			t.Fatal("failure budget reset")
		}
		if rows, err := st.ListDueRetryWork(ctx, now, 10); err != nil || len(rows) != 0 {
			t.Fatal("retry before due", err)
		}
		eligible, err := st.ListDueRetryWork(ctx, due, 10)
		if err != nil {
			t.Fatal(err)
		}
		if failure == 3 {
			if len(eligible) != 0 {
				t.Fatal("persistent INTERNAL retried without bound")
			}
			break
		}
		if len(eligible) != 1 || eligible[0].ScopeKey != "/" {
			t.Fatalf("eligible=%+v", eligible)
		}
		if failure == 1 {
			for _, raw := range []string{`{"mirror_presence":false,"topology_token_env":"TOKEN"}`, `{"mirror_presence":true}`, `{"mirror_presence":true,"topology_username_env":"USER"}`} {
				setConfig(raw)
				if rows, e := st.ListDueRetryWork(ctx, due, 10); e != nil || len(rows) != 0 {
					t.Fatal("unsafe config enabled INTERNAL retry", e)
				}
			}
			setConfig(`{"mirror_presence":true,"topology_token_env":"TOKEN"}`)
			watch, err = st.GetWatch(ctx, p3RootActive, "/")
			if err != nil {
				t.Fatal(err)
			}
			watch.WatchState = state.WatchDisabled
			watch.EffectiveIntervalSeconds = nil
			watch.NextDueAt = nil
			watch, err = st.CASUpdateWatch(ctx, watch, watch.Version)
			if err != nil {
				t.Fatal(err)
			}
			if rows, e := st.ListDueRetryWork(ctx, due, 10); e != nil || len(rows) != 0 {
				t.Fatal("disabled watch retried", e)
			}
			watch.WatchState = state.WatchHot
			watch.EffectiveIntervalSeconds = &interval
			watch.NextDueAt = &due
			watch, err = st.CASUpdateWatch(ctx, watch, watch.Version)
			if err != nil {
				t.Fatal(err)
			}
		}
		resumed, err := st.RetryReady(ctx, p3RootActive, "/", failed.Version, due)
		if err != nil {
			t.Fatal(err)
		}
		if resumed.ConsecutiveFailures != failed.ConsecutiveFailures || resumed.AttemptCount != failed.AttemptCount || resumed.SignalSeq != failed.SignalSeq {
			t.Fatal("retry discarded history")
		}
		if _, err = st.RetryReady(ctx, p3RootActive, "/", failed.Version, due); err == nil {
			t.Fatal("stale retry CAS accepted")
		}
		now = due
	}
}
