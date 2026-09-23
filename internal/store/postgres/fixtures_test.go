package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/completeness"
	io3 "github.com/nathanxiangang-web/index-core/internal/kernel/identity"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// loadPriorRich loads canonical resources enriched with their latest
// IdentityEvidence observation, so provider_object_id-based identity matching
// (R1) can be exercised end to end.
func loadPriorRich(t *testing.T, st *postgres.Store, ctx context.Context, rootID string) []reconcile.PriorResource {
	t.Helper()
	rows, err := st.Pool().Query(ctx,
		`SELECT c.resource_id::text, c.root_id::text, c.introduced_at_generation, c.last_confirmed_generation,
		        c.resource_presence, c.removal_evidence_state, c.missing_since, c.consecutive_complete_missing,
		        c.canonical_path, c.parent_resource_id::text, c.name, c.is_dir, c.size, c.mtime,
		        c.content_hash, c.hash_algorithm, c.content_type, c.current_attributes, c.created_at, c.updated_at,
		        o.provider_object_id, o.provider_object_id_scope, o.provider_identity_assurance
		   FROM index_canonical_resource c
		   LEFT JOIN LATERAL (
		       SELECT provider_object_id, provider_object_id_scope, provider_identity_assurance
		         FROM index_identity_evidence_observation
		        WHERE resource_id = c.resource_id
		        ORDER BY observation_id DESC LIMIT 1
		   ) o ON true
		  WHERE c.root_id = $1::uuid
		  ORDER BY c.resource_id`, rootID)
	if err != nil {
		t.Fatalf("load prior rich: %v", err)
	}
	defer rows.Close()
	var out []reconcile.PriorResource
	for rows.Next() {
		var (
			p             reconcile.PriorResource
			presence, rem string
			assurance     *string
		)
		if err := rows.Scan(&p.ResourceID, &p.RootID, &p.IntroducedAtGeneration, &p.LastConfirmedGeneration,
			&presence, &rem, &p.MissingSince, &p.ConsecutiveCompleteMissing,
			&p.CanonicalPath, &p.ParentResourceID, &p.Name, &p.IsDir, &p.Size, &p.Mtime,
			&p.ContentHash, &p.HashAlgorithm, &p.ContentType, &p.CurrentAttributes, &p.CreatedAt, &p.UpdatedAt,
			&p.ProviderObjectID, &p.ProviderObjectIDScope, &assurance); err != nil {
			t.Fatalf("scan prior: %v", err)
		}
		p.ResourcePresence = domain.ResourcePresence(presence)
		p.RemovalEvidenceState = domain.RemovalEvidenceState(rem)
		if assurance != nil {
			p.ProviderIDAssurance = domain.ProviderIdentityAssurance(*assurance)
		} else {
			p.ProviderIDAssurance = domain.IdentityUnavailable
		}
		out = append(out, p)
	}
	return out
}

// runSnapshotRich processes one snapshot using the rich prior loader.
func runSnapshotRich(t *testing.T, st *postgres.Store, ctx context.Context, snapshotID string,
	acceptance domain.AcceptanceState, ident domain.SnapshotIdentity,
	entries []domain.SnapshotEntry, cfg reconcile.Config) postgres.ReconcileOutcome {
	t.Helper()
	snap := domain.Snapshot{
		SnapshotID: snapshotID, RootID: pipeRoot, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, CompletenessFlag: domain.CompletenessFlagComplete,
		LifecycleState: domain.SnapshotDraft,
	}
	if err := st.InsertSnapshotStub(ctx, st.Pool(), snap); err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snapshotID); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := st.SetSnapshotEvaluated(ctx, st.Pool(), snapshotID, acceptance, nil); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	for _, e := range entries {
		e.SnapshotID = snapshotID
		if err := st.InsertSnapshotEntry(ctx, st.Pool(), e); err != nil {
			t.Fatalf("insert entry: %v", err)
		}
	}
	seq, err := st.AllocateAdmission(ctx, st.Pool(), pipeRoot, snapshotID)
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	out, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
		RootID: pipeRoot, AdmissionSeq: seq, SnapshotID: snapshotID, Identity: ident,
	}, func(_ []domain.CanonicalResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
		prior := loadPriorRich(t, st, ctx, pipeRoot)
		res := reconcile.Reconcile(prior, entries, acceptance, cfg, time.Now().UTC())
		return st.PlanFromResult(pipeRoot, res), nil
	})
	if err != nil {
		t.Fatalf("reconcile %s: %v", snapshotID, err)
	}
	return out
}

func tagProvider(t *testing.T, st *postgres.Store, ctx context.Context, resourceID, snapshotID, provID string) {
	t.Helper()
	scope := "root"
	assurance := domain.IdentityStableWithinScope
	if err := st.InsertIdentityEvidence(ctx, st.Pool(), domain.IdentityEvidenceObservation{
		ResourceID: resourceID, SnapshotID: snapshotID, ObservedAt: time.Now().UTC(),
		ProviderObjectID: &provID, ProviderObjectIDScope: &scope, ProviderIdentityAssurance: assurance,
		IsDir: false,
	}); err != nil {
		t.Fatalf("tag provider: %v", err)
	}
}

func newFixtureRoot(t *testing.T, st *postgres.Store, ctx context.Context) {
	t.Helper()
	if err := st.CreateRoot(ctx, st.Pool(), pipeRoot, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
}

func ident(v string) domain.SnapshotIdentity {
	return domain.SnapshotIdentity{
		Kind: domain.IdentityDeterministicDigest, Namespace: "kernel.index-core/io3", Version: "v1", Value: v,
	}
}

func TestFixtureMatrix(t *testing.T) {
	mt := time.Now().UTC()
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1}

	t.Run("1_initial_population", func(t *testing.T) {
		st, ctx := newStore(t)
		newFixtureRoot(t, st, ctx)
		out := runSnapshotRich(t, st, ctx, "a1000000-0000-0000-0000-000000000001",
			domain.AcceptanceComplete, ident("fx1"), []domain.SnapshotEntry{
				qEntry("a.txt", "ha", 1, mt), qEntry("b.txt", "hb", 2, mt),
			}, cfg)
		if out.AppliedGeneration != 1 {
			t.Fatalf("initial population must reach generation 1, got %+v", out)
		}
		requirePresence(t, st, ctx, "/a.txt")
		requirePresence(t, st, ctx, "/b.txt")
	})

	t.Run("2_v1_v2_add", func(t *testing.T) {
		st, ctx := newStore(t)
		newFixtureRoot(t, st, ctx)
		runSnapshotRich(t, st, ctx, "a2000000-0000-0000-0000-000000000001",
			domain.AcceptanceComplete, ident("fx2a"), []domain.SnapshotEntry{qEntry("a.txt", "ha", 1, mt)}, cfg)
		out := runSnapshotRich(t, st, ctx, "a2000000-0000-0000-0000-000000000002",
			domain.AcceptanceComplete, ident("fx2b"),
			[]domain.SnapshotEntry{qEntry("a.txt", "ha", 1, mt), qEntry("b.txt", "hb", 2, mt)}, cfg)
		if out.AppliedGeneration != 2 {
			t.Fatalf("V2 add must advance to generation 2, got %+v", out)
		}
		requirePresence(t, st, ctx, "/b.txt")
	})

	t.Run("3_rename", func(t *testing.T) {
		st, ctx := newStore(t)
		newFixtureRoot(t, st, ctx)
		runSnapshotRich(t, st, ctx, "a3000000-0000-0000-0000-000000000001",
			domain.AcceptanceComplete, ident("fx3a"), []domain.SnapshotEntry{qEntry("old.txt", "hx", 1, mt)}, cfg)
		runSnapshotRich(t, st, ctx, "a3000000-0000-0000-0000-000000000002",
			domain.AcceptanceComplete, ident("fx3b"), []domain.SnapshotEntry{qEntry("new.txt", "hx", 1, mt)}, cfg)
		if rows, _ := st.PresentResourcesAtPath(ctx, st.Pool(), pipeRoot, "/old.txt"); len(rows) != 0 {
			t.Fatalf("old path must be gone after rename, got %d", len(rows))
		}
		requirePresence(t, st, ctx, "/new.txt")
	})

	t.Run("4_move", func(t *testing.T) {
		st, ctx := newStore(t)
		newFixtureRoot(t, st, ctx)
		runSnapshotRich(t, st, ctx, "a4000000-0000-0000-0000-000000000001",
			domain.AcceptanceComplete, ident("fx4a"), []domain.SnapshotEntry{
				{EntryLocalID: "m", Name: "m.txt", ParentRef: "/dir", Size: i64p(1), Mtime: &mt, ContentHash: sp("hm"), HashAlgorithm: algp()},
			}, cfg)
		runSnapshotRich(t, st, ctx, "a4000000-0000-0000-0000-000000000002",
			domain.AcceptanceComplete, ident("fx4b"), []domain.SnapshotEntry{
				{EntryLocalID: "m", Name: "m.txt", ParentRef: "/other", Size: i64p(1), Mtime: &mt, ContentHash: sp("hm"), HashAlgorithm: algp()},
			}, cfg)
		requirePresence(t, st, ctx, "/other/m.txt")
		if rows, _ := st.PresentResourcesAtPath(ctx, st.Pool(), pipeRoot, "/dir/m.txt"); len(rows) != 0 {
			t.Fatalf("old parent path must be gone after move, got %d", len(rows))
		}
	})

	t.Run("5_move_rename_plus_update", func(t *testing.T) {
		st, ctx := newStore(t)
		newFixtureRoot(t, st, ctx)
		runSnapshotRich(t, st, ctx, "a5000000-0000-0000-0000-000000000001",
			domain.AcceptanceComplete, ident("fx5a"), []domain.SnapshotEntry{
				{EntryLocalID: "f", Name: "f.txt", ParentRef: "/dir", Size: i64p(1), Mtime: &mt, ContentHash: sp("h1"), HashAlgorithm: algp()},
			}, cfg)
		// Establish identity continuity for the in-place content change.
		r := requirePresence(t, st, ctx, "/dir/f.txt")
		tagProvider(t, st, ctx, r.ResourceID, "a5000000-0000-0000-0000-000000000001", "PF")

		newMt := mt.Add(time.Hour)
		out := runSnapshotRich(t, st, ctx, "a5000000-0000-0000-0000-000000000002",
			domain.AcceptanceComplete, ident("fx5b"), []domain.SnapshotEntry{
				{EntryLocalID: "f", Name: "f.txt", ParentRef: "/other", Size: i64p(9), Mtime: &newMt, ContentHash: sp("h2"), HashAlgorithm: algp(),
					ProviderObjectID: sp("PF"), ProviderObjectIDScope: sp("root")},
			}, cfg)
		requirePresence(t, st, ctx, "/other/f.txt")
		events, _ := st.ReadJournal(ctx, st.Pool(), pipeRoot, 1, 10)
		if len(events) < 2 || events[0].EventType != domain.EventResourceMoved || events[1].EventType != domain.EventResourceUpdated {
			t.Fatalf("move+update must emit ordered [resource-moved, resource-updated], got %+v", events)
		}
		if events[0].IntraGenerationSeq != 1 || events[1].IntraGenerationSeq != 2 {
			t.Fatalf("ordered pair intra seq must be 1 then 2, got %d,%d", events[0].IntraGenerationSeq, events[1].IntraGenerationSeq)
		}
		_ = out
	})

	t.Run("6_partial_missing_no_removal", func(t *testing.T) {
		st, ctx := newStore(t)
		newFixtureRoot(t, st, ctx)
		runSnapshotRich(t, st, ctx, "a6000000-0000-0000-0000-000000000001",
			domain.AcceptanceComplete, ident("fx6a"), []domain.SnapshotEntry{qEntry("a.txt", "ha", 1, mt)}, cfg)
		out := runSnapshotRich(t, st, ctx, "a6000000-0000-0000-0000-000000000002",
			domain.AcceptancePartial, ident("fx6b"), nil, cfg)
		if out.Mutated {
			t.Fatalf("PARTIAL must not advance removal evidence, got %+v", out)
		}
		requirePresence(t, st, ctx, "/a.txt")
	})

	t.Run("7_confirmed_removal", func(t *testing.T) {
		st, ctx := newStore(t)
		newFixtureRoot(t, st, ctx)
		runSnapshotRich(t, st, ctx, "a7000000-0000-0000-0000-000000000001",
			domain.AcceptanceComplete, ident("fx7a"), []domain.SnapshotEntry{qEntry("a.txt", "ha", 1, mt)}, cfg)
		runSnapshotRich(t, st, ctx, "a7000000-0000-0000-0000-000000000002",
			domain.AcceptanceComplete, ident("fx7b"), nil, cfg)
		if rows, _ := st.PresentResourcesAtPath(ctx, st.Pool(), pipeRoot, "/a.txt"); len(rows) != 0 {
			t.Fatalf("confirmed removal must exclude a.txt from PRESENT reads, got %d", len(rows))
		}
	})

	t.Run("8_shrink_none_is_suspicious_non_destructive", func(t *testing.T) {
		state := completeness.Evaluate(completeness.Evidence{
			TraversalStatus: domain.TraversalSuccess, SkippedKnownEmpty: true,
			Freshness: domain.FreshDirect, Assurance: domain.StrongFailureVisibility,
			PriorPresent: 100, EntryCount: 10,
		}, completeness.Config{})
		if state != domain.AcceptanceSuspicious {
			t.Fatalf("unconfirmed significant shrink must be SUSPICIOUS, got %s", state)
		}
		st, ctx := newStore(t)
		newFixtureRoot(t, st, ctx)
		out := runSnapshotRich(t, st, ctx, "a8000000-0000-0000-0000-000000000001",
			state, ident("fx8"), nil, cfg)
		if out.Mutated {
			t.Fatalf("SUSPICIOUS acceptance must not advance destructive removal, got %+v", out)
		}
	})

	t.Run("9_corroborated_shrink_distinct_identity_and_eligible", func(t *testing.T) {
		base := io3.Evidence{TraversalStatus: "SUCCESS", SkippedScopesCanonical: "CONFIRMED_EMPTY",
			Freshness: "FRESH_DIRECT", Assurance: "STRONG_FAILURE_VISIBILITY", ScopeShrinkCorroboration: "NONE"}
		corr := base
		corr.ScopeShrinkCorroboration = "CORROBORATED"
		n := io3.FinalDigest(nil, base)
		c := io3.FinalDigest(nil, corr)
		if n.Equal(c) {
			t.Fatal("NONE vs CORROBORATED must yield distinct evaluated IO3 identities")
		}
		state := completeness.Evaluate(completeness.Evidence{
			TraversalStatus: domain.TraversalSuccess, SkippedKnownEmpty: true,
			Freshness: domain.FreshDirect, Assurance: domain.StrongFailureVisibility,
			PriorPresent: 100, EntryCount: 10, Corroboration: &corr2,
		}, completeness.Config{})
		if state != domain.AcceptanceComplete {
			t.Fatalf("corroborated shrink with all-positive dimensions must be COMPLETE, got %s", state)
		}
	})

	t.Run("10_duplicate_same_generation_is_noop", func(t *testing.T) {
		st, ctx := newStore(t)
		newFixtureRoot(t, st, ctx)
		id := ident("fx10")
		runSnapshotRich(t, st, ctx, "aa000000-0000-0000-0000-000000000001",
			domain.AcceptanceComplete, id, []domain.SnapshotEntry{qEntry("a.txt", "ha", 1, mt)}, cfg)
		out := runSnapshotRich(t, st, ctx, "aa000000-0000-0000-0000-000000000002",
			domain.AcceptanceComplete, id, []domain.SnapshotEntry{qEntry("a.txt", "ha", 1, mt)}, cfg)
		if out.Status != domain.AdmissionNoop {
			t.Fatalf("same identity at same generation must be NOOP, got %s", out.Status)
		}
	})

	t.Run("11_same_identity_after_generation_reconciles", func(t *testing.T) {
		st, ctx := newStore(t)
		newFixtureRoot(t, st, ctx)
		idA := ident("fx11a")
		runSnapshotRich(t, st, ctx, "ab000000-0000-0000-0000-000000000001",
			domain.AcceptanceComplete, idA, []domain.SnapshotEntry{qEntry("a.txt", "ha", 1, mt)}, cfg)
		// Different identity first advances the generation to 2.
		runSnapshotRich(t, st, ctx, "ab000000-0000-0000-0000-000000000002",
			domain.AcceptanceComplete, ident("fx11b"), []domain.SnapshotEntry{qEntry("b.txt", "hb", 2, mt)}, cfg)
		// Re-apply idA at generation 2: identity matches but generation advanced -> not NOOP.
		out := runSnapshotRich(t, st, ctx, "ab000000-0000-0000-0000-000000000003",
			domain.AcceptanceComplete, idA, []domain.SnapshotEntry{qEntry("a.txt", "ha", 1, mt)}, cfg)
		if out.Status != domain.AdmissionApplied {
			t.Fatalf("same identity after generation advanced must re-reconcile (not NOOP), got %s", out.Status)
		}
		maxGen, _ := st.MaxAppliedGeneration(ctx, st.Pool(), pipeRoot, idA)
		if maxGen != out.AppliedGeneration {
			t.Fatalf("application history must record the post-application generation, got %d want %d", maxGen, out.AppliedGeneration)
		}
	})

	t.Run("13_same_root_absolute_fifo", func(t *testing.T) {
		st, ctx := newStore(t)
		newFixtureRoot(t, st, ctx)
		runSnapshotRich(t, st, ctx, "ad000000-0000-0000-0000-000000000001",
			domain.AcceptanceComplete, ident("fx13a"), []domain.SnapshotEntry{qEntry("a.txt", "ha", 1, mt)}, cfg)
		// Two pending admissions; the higher one must not be processed before the head.
		if _, err := st.AllocateAdmission(ctx, st.Pool(), pipeRoot, "ad000000-0000-0000-0000-000000000002"); err != nil {
			t.Fatal(err)
		}
		seq2, _ := st.AllocateAdmission(ctx, st.Pool(), pipeRoot, "ad000000-0000-0000-0000-000000000003")
		_, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
			RootID: pipeRoot, AdmissionSeq: seq2, SnapshotID: "ad000000-0000-0000-0000-000000000003", Identity: ident("fx13c"),
		}, func(_ []domain.CanonicalResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
			return &postgres.Plan{}, nil
		})
		if err != postgres.ErrNotHead {
			t.Fatalf("higher admission must not leapfrog the head, got %v", err)
		}
	})

	t.Run("14_different_root_concurrency_independence", func(t *testing.T) {
		st, ctx := newStore(t)
		roots := []string{"b1000000-0000-0000-0000-000000000001", "b1000000-0000-0000-0000-000000000002"}
		for _, r := range roots {
			if err := st.CreateRoot(ctx, st.Pool(), r, []byte(`{}`), domain.RootActive); err != nil {
				t.Fatalf("create root %s: %v", r, err)
			}
		}
		var wg sync.WaitGroup
		errs := make([]error, len(roots))
		for i, r := range roots {
			wg.Add(1)
			go func(i int, root string) {
				defer wg.Done()
				snap := "b2000000-0000-0000-0000-00000000000" + string(rune('1'+i))
				s := domain.Snapshot{SnapshotID: snap, RootID: root, Provenance: []byte(`{}`),
					ObservedAt: time.Now().UTC(), TraversalStatus: domain.TraversalSuccess,
					CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft}
				if err := st.InsertSnapshotStub(ctx, st.Pool(), s); err != nil {
					errs[i] = err
					return
				}
				if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snap); err != nil {
					errs[i] = err
					return
				}
				if err := st.SetSnapshotEvaluated(ctx, st.Pool(), snap, domain.AcceptanceComplete, nil); err != nil {
					errs[i] = err
					return
				}
				seq, err := st.AllocateAdmission(ctx, st.Pool(), root, snap)
				if err != nil {
					errs[i] = err
					return
				}
				_, err = st.ReconcileHead(ctx, postgres.ReconcileInput{
					RootID: root, AdmissionSeq: seq, SnapshotID: snap, Identity: ident("fx14-" + root),
				}, func(_ []domain.CanonicalResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
					return &postgres.Plan{MutatesCanonical: true, Events: []domain.JournalEvent{
						{EventType: domain.EventResourceAdded, ResourceID: sp("c0000000-0000-0000-0000-00000000000" + string(rune('1'+i))), Payload: []byte(`{}`)},
					}}, nil
				})
				errs[i] = err
			}(i, r)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("root %s concurrent reconcile failed: %v", roots[i], err)
			}
			g, _ := st.GetRoot(ctx, st.Pool(), roots[i])
			if g.CurrentGeneration != 1 {
				t.Fatalf("root %s must independently reach generation 1, got %d", roots[i], g.CurrentGeneration)
			}
		}
	})

	t.Run("15_path_reuse_imposter", func(t *testing.T) {
		st, ctx := newStore(t)
		newFixtureRoot(t, st, ctx)
		// A non-zero grace keeps the old MISSING resource PRESENT so the overlap is
		// observable (R8: old resource is not yet removed).
		cfg15 := reconcile.Config{MinConsecutiveCompleteMissing: 2, RemovalGracePeriod: time.Hour}
		runSnapshotRich(t, st, ctx, "b3000000-0000-0000-0000-000000000001",
			domain.AcceptanceComplete, ident("fx15a"), []domain.SnapshotEntry{qEntry("p.txt", "h1", 1, mt)}, cfg15)
		old := requirePresence(t, st, ctx, "/p.txt")
		// Same path, different content -> a NEW resource must not inherit old id.
		runSnapshotRich(t, st, ctx, "b3000000-0000-0000-0000-000000000002",
			domain.AcceptanceComplete, ident("fx15b"), []domain.SnapshotEntry{qEntry("p.txt", "h2", 7, mt)}, cfg15)
		res, _ := st.QueryResolvePath(ctx, pipeRoot, "/p.txt", false)
		if len(res.Matches) < 2 || !res.Ambiguous {
			t.Fatalf("path reuse must surface BOTH overlapping PRESENT rows with ambiguity, got %+v", res)
		}
		for _, m := range res.Matches {
			if m.ResourceID == old.ResourceID && m.ContentHash != nil && *m.ContentHash == "h2" {
				t.Fatal("imposter must not reuse the old resource_id")
			}
		}
	})
}

var corr2 = domain.ShrinkCorroborated

func i64p(v int64) *int64 { return &v }

func sp(s string) *string { return &s }

func algp() *string { s := "sha256"; return &s }
