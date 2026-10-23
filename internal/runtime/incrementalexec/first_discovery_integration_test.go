package incrementalexec_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

type firstScanRecorder struct {
	inner          incrementalexec.ScopedScanner
	now            *time.Time
	scopes         []string
	wall           []time.Duration
	started        time.Time
	firstClaimWall time.Duration
	targetStep     int
	targetSnapshot string
}

func (s *firstScanRecorder) ScanScope(ctx context.Context, root, scope string, cap int) (scan.Result, error) {
	s.scopes = append(s.scopes, scope)
	if scope == "/fresh" {
		s.firstClaimWall = time.Since(s.started)
	}
	*s.now = s.now.Add(time.Second)
	start := time.Now()
	result, err := s.inner.ScanScope(ctx, root, scope, cap)
	if scope == "/fresh/nested" && err == nil {
		s.targetStep = len(s.scopes)
		s.targetSnapshot = result.SnapshotID
	}
	s.wall = append(s.wall, time.Since(start))
	return result, err
}

// Real PG + HTTP + Kernel/Q8. The virtual clock models a stated 1s scoped
// fetch, not measured production time. Actual fixture wall times are separate.
func TestIssue134FirstDiscoveryBacklogRegression(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint("enabled=", enabled), func(t *testing.T) {
			st, mock, root := p4SetupRootPath(t, "/library")
			ctx := context.Background()
			now := p4Now
			dirs := []map[string]any{p4Dir("fresh")}
			for i := 0; i < 401; i++ {
				dirs = append(dirs, p4Dir(fmt.Sprintf("old-%03d", i)))
			}
			mock.set("/library", dirs...)
			scanner := p4RealScanner(st)
			if _, err := scanner.ScanScope(ctx, root, "/", 1000); err != nil {
				t.Fatal(err)
			}
			// Real existing directories which have been verified, then requeued.
			for i := 0; i < 401; i++ {
				scope := fmt.Sprintf("/old-%03d", i)
				mock.set("/library" + scope)
				p4SeedPending(t, st, ctx, root, scope, now.Add(-2*time.Hour), nil)
				w, _ := st.GetWork(ctx, root, scope)
				claim, err := st.ClaimWork(ctx, root, scope, w.Version, now.Add(-2*time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = st.CompleteSuccess(ctx, root, scope, *claim.ClaimedSignalSeq, now.Add(-2*time.Hour)); err != nil {
					t.Fatal(err)
				}
				if _, err = st.MergeSignal(ctx, state.DirtySignal{RootID: root, ScopeKey: scope, Source: state.SourceRecovery, Reason: state.ReasonPossibleChange, Priority: state.PriorityNormal, SeenAt: now.Add(-time.Hour)}); err != nil {
					t.Fatal(err)
				}
			}
			mock.set("/library/fresh", p4Dir("nested"))
			mock.set("/library/fresh/nested", p4File("target.txt", 1, "first-discovery-hash"))
			p4SeedPending(t, st, ctx, root, "/", now, nil)
			parent, _ := st.GetWork(ctx, root, "/")
			claim, err := st.ClaimWork(ctx, root, "/", parent.Version, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.CompleteSuccessWithChildren(ctx, root, "/", *claim.ClaimedSignalSeq, []string{"/fresh"}, now); err != nil {
				t.Fatal(err)
			}
			cfg := p4Config()
			cfg.DiscoveryScope = "/"
			cfg.FirstDiscoveryEnabled = enabled
			recorder := &firstScanRecorder{inner: scanner, now: &now}
			ex, err := incrementalexec.New(st, recorder, cfg, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			runner, _ := incrementalexec.NewCycleRunner(ex)
			start := time.Now()
			recorder.started = start
			refreshBefore := mock.refreshCount()
			first := 0
			cycles := 0
			found := 0
			for cycles < 90 && found == 0 {
				result, err := runner.Run(ctx, incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: 60 * time.Second})
				if err != nil {
					t.Fatal(result, err)
				}
				cycles++
				for i, scope := range recorder.scopes {
					if scope == "/fresh" && first == 0 {
						first = i + 1
					}
				}
				if err = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE canonical_path='/library/fresh/nested/target.txt' AND resource_presence='PRESENT'`).Scan(&found); err != nil {
					t.Fatal(err)
				}
			}
			if found != 1 || first == 0 {
				t.Fatal("target never reached canonical", cycles, first)
			}
			if enabled && (first > 2 || recorder.targetStep > 7) {
				t.Fatal("fresh directory waited behind old backlog", first, len(recorder.scopes))
			}
			if !enabled && first != 402 {
				t.Fatal("baseline no longer reproduces backlog", first)
			}
			if mock.refreshCount()-refreshBefore != len(recorder.scopes) {
				t.Fatal("multiple source refreshes per claim")
			}
			p4AssertNoRemovalEvidence(t, st, "/library/old-000")
			var removed, added int
			if err = st.Pool().QueryRow(ctx, `SELECT count(*) FILTER(WHERE event_type='resource-removed'),count(*) FILTER(WHERE event_type='resource-added' AND resource_id=(SELECT resource_id FROM index_canonical_resource WHERE canonical_path='/library/fresh/nested/target.txt')) FROM index_journal_event`).Scan(&removed, &added); err != nil {
				t.Fatal(err)
			}
			if removed != 0 || added != 1 {
				t.Fatal("Journal/identity safety", removed, added)
			}
			var snapshot string
			if err = st.Pool().QueryRow(ctx, `SELECT g.produced_by_snapshot_id FROM index_generation g JOIN index_canonical_resource c ON c.root_id=g.root_id AND c.introduced_at_generation=g.generation_number WHERE c.canonical_path='/library/fresh/nested/target.txt'`).Scan(&snapshot); err != nil || snapshot != recorder.targetSnapshot {
				t.Fatal("target publication not anchored to measured scan", snapshot, err)
			}
			t.Logf("MEASURE enabled=%t backlog=401 first_claim_scan_index=%d target_publish_scan_index=%d modeled_queue_wait_seconds=%d modeled_source_to_target_seconds=%d actual_target_claim_wall_ms=%d actual_fixture_wall_ms=%d", enabled, first, recorder.targetStep, first-1, recorder.targetStep, recorder.firstClaimWall.Milliseconds(), time.Since(start).Milliseconds())
		})
	}
}

type firstNoopScanner struct {
	scopes []string
	roots  []string
}

func (s *firstNoopScanner) ScanScope(_ context.Context, root, scope string, _ int) (scan.Result, error) {
	s.scopes = append(s.scopes, scope)
	s.roots = append(s.roots, root)
	return scan.Result{SnapshotID: "fixture-noop", Outcome: postgres.ReconcileOutcome{Status: domain.AdmissionNoop}}, nil
}

func TestIssue134FirstDiscoveryHundredFoldersAndHighFlood(t *testing.T) {
	st, mock, root := p4SetupRootPath(t, "/library")
	ctx := context.Background()
	now := p4Now
	dirs := []map[string]any{}
	children := []string{}
	for i := 0; i < 110; i++ {
		name := fmt.Sprintf("fresh-%03d", i)
		dirs = append(dirs, p4Dir(name))
		children = append(children, "/"+name)
	}
	mock.set("/library", dirs...)
	if _, err := p4RealScanner(st).ScanScope(ctx, root, "/", 1000); err != nil {
		t.Fatal(err)
	}
	p4SeedPending(t, st, ctx, root, "/", now, nil)
	p, _ := st.GetWork(ctx, root, "/")
	claim, err := st.ClaimWork(ctx, root, "/", p.Version, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CompleteSuccessWithChildren(ctx, root, "/", *claim.ClaimedSignalSeq, children, now); err != nil {
		t.Fatal(err)
	}
	const sibling = "a1000000-0000-0000-0000-0000000000b1"
	if err = st.CreateRoot(ctx, st.Pool(), sibling, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		p4SeedPending(t, st, ctx, sibling, fmt.Sprintf("/old-%03d", i), now.Add(-time.Hour), nil)
	}
	scanner := &firstNoopScanner{}
	cfg := p4Config()
	cfg.FirstDiscoveryEnabled = true
	ex, err := incrementalexec.New(st, scanner, cfg, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	runner, _ := incrementalexec.NewCycleRunner(ex)
	for cycle := 0; cycle < 20; cycle++ {
		for i := 0; i < 6; i++ {
			_, err = st.MergeSignal(ctx, state.DirtySignal{RootID: root, ScopeKey: fmt.Sprintf("/hot-%03d-%d", cycle, i), Source: state.SourceMutationHint, Reason: state.ReasonPossibleChange, Priority: state.PriorityHigh, SeenAt: now})
			if err != nil {
				t.Fatal(err)
			}
		}
		before := len(scanner.scopes)
		result, err := runner.Run(ctx, incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: time.Second})
		if err != nil || result.Succeeded != 5 {
			t.Fatal(result, err)
		}
		if scanner.roots[before] != sibling {
			t.Fatal("sibling NORMAL starvation under HIGH/fresh flood")
		}
		if scanner.scopes[before+1] != fmt.Sprintf("/fresh-%03d", cycle) {
			t.Fatal("first discovery not deterministic", scanner.scopes[before:])
		}
		now = now.Add(time.Second)
	}
	var done int
	if err = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_dirty_scope_work WHERE root_id=$1 AND work_state='VERIFIED'`, sibling).Scan(&done); err != nil || done != 20 {
		t.Fatal("bounded NORMAL progress", done, err)
	}
	// Discovery only changes operational scheduling, not priority labels.
	if err = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_dirty_scope_work WHERE scope_key LIKE '/fresh-%' AND pending_priority='HIGH'`).Scan(&done); err != nil || done != 0 {
		t.Fatal("whole root promoted", done, err)
	}
}
