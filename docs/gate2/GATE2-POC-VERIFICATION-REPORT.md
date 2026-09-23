# Gate 2 — Index Core PoC Verification Report

> Execution: Issue #44. Stack (Architect-locked, PR #45): **Go 1.27.x + PostgreSQL 18
> + pgx/v5**. Branch: **`poc/gate2-indexcore`**. Status: **READY_FOR_ARCH_REVIEW**.
> Rule: PASS is claimed only where tests actually exercise the behavior.

## 1. Branch and commits

Branch `poc/gate2-indexcore` (from `main` @ `1a420df`):

| Commit | Stage |
|--------|-------|
| `1ba7694` | P0 scaffold: module, layered packages, SQL-first migrations, real-PG harness |
| `023e911` | P0 package skeletons (kernel/collector/query) |
| `a343b05` | P1 PostgreSQL Store repos |
| `85acfb6` | P3 Kernel evaluation + Kernel-owned IO3 digest |
| `3444825` | P2 per-root serialized reconcile transaction |
| `27fa377` | P4 Safe Reconcile core + Store Plan wiring |
| `495d70e` | P5 Canonical Change Journal + J6 repair |
| `5bb06f7` | P6 read-only Query Contract |
| `213e32b` | P7 V1/V2 fixture matrix |
| `34cc299` | P8 rclone adapter |

## 2. Migration / schema

- SQL-first migrations embedded in the binary: `internal/store/postgres/migrations/*.sql`.
- Runner: `postgres.Migrate(ctx, pool)` — applies pending files in lexical order,
  each in its own transaction, tracked in `schema_migrations`; idempotent and
  reproducible from an empty database.
- `0001_init.sql` implements FROZEN doc A tables T1..T11 plus the
  `index_identity_evidence_current` projection, and append-only triggers on
  `index_journal_event` and `index_identity_evidence_observation`.
- No fake `UNIQUE(root_id, canonical_path)` (C-C3 REJECTED); `C-C3a` overlap is real.

## 3. Exact test commands

```bash
# PostgreSQL 18 container (docker available):
make pg-up                       # postgres:18 on localhost:55432

# Real-PostgreSQL + unit tests:
make test                        # INDEXCORE_TEST_DATABASE_URL=... go test ./...

# or explicitly:
INDEXCORE_TEST_DATABASE_URL='postgres://indexcore:indexcore@localhost:55432/indexcore?sslmode=disable' go test ./...
go vet ./...
gofmt -l .
```

## 4. Test summary (including failure paths)

All tests run against **real PostgreSQL 18.6** except the pure Kernel/unit tests
(no in-memory fake substitutes PostgreSQL anywhere).

| Area | Tests |
|------|-------|
| Migration | reproducible from empty DB; idempotent; journal/evidence append-only triggers reject UPDATE/DELETE; no fake path uniqueness |
| Transaction (P2) | zero-mutation keeps generation + records application; mutation advances + ordered journal; non-head rejected; CAS conflict leaves no partial state; rollback leaves no partial state then FAILED in a later tx; same-generation NOOP; DELETED root rejected |
| Kernel eval (P3) | C-1..C-10/C-9a cascade; UNKNOWN skips never COMPLETE; corroboration NONE/CORROBORATED/CONTRADICTED; digest distinguishes corroboration/assurance/skip-evidence; order-independent; timing-free |
| Reconcile (P4) | ADD/UPDATE/RENAME/MOVE; move+update ordered pair; PARTIAL does not advance removal; first missing; confirmed removal; R8 imposter; ambiguous content-hash conflict |
| Journal (P5) | cursor vector; rebuild=0 vs checkpoint resume; non-zero floor without checkpoint rejected; apply order |
| J6 repair | corrective append on DELETED root without canonical/generation change; event_seq=next; intra=MAX+1; external reconcile still rejected |
| Query (P6) | visibility defaults; REMOVED hidden by default; resolve_path ambiguity; generation-bound pagination STALE_CURSOR; journal cursor |
| Fixtures (P7) | 15 scenario subtests (see Sec 6) |
| rclone (P8) | lsjson normalization; hashes with algorithm; provider id preserved as evidence only; UNKNOWN skip evidence cannot be COMPLETE; failure/parse errors |

## 5. Changed-file summary by module

```
internal/domain/            enums, entities, SnapshotIdentity
internal/kernel/completeness/  acceptance_state cascade + shrink corroboration
internal/kernel/identity/      Kernel-owned evaluated-Snapshot DETERMINISTIC_DIGEST
internal/kernel/reconcile/     identity matching subset + transitions (pure)
internal/kernel/journal/       cursor vector + rebuild/checkpoint floor rules
internal/store/postgres/       pgxpool store, repos, migrations, Stage-1/2 tx, Plan, query
internal/query/                read-only view types + STALE_CURSOR
internal/collector/rclone/     external-process adapter (lsjson normalize)
testutil/                      real-PostgreSQL test harness
Makefile, go.mod, .env.example, PROJECT-STATE.md, NEXT-ACTIONS.md
```

## 6. Fixture matrix -> frozen contract mapping

| # | Fixture | Frozen source | Where |
|---|---------|---------------|-------|
| 1 | initial complete Snapshot -> population | doc A G1; Safe Reconcile Sec 1.1 | `TestFixtureMatrix/1_initial_population` |
| 2 | V1 -> V2 add | doc A G1; Safe Reconcile ADD | `.../2_v1_v2_add` |
| 3 | rename | Safe Reconcile RENAME (J8) | `.../3_rename` |
| 4 | move | Safe Reconcile MOVE | `.../4_move` |
| 5 | move/rename + update ordered pair | doc A G8; doc D Sec 6 | `.../5_move_rename_plus_update` |
| 6 | incomplete Snapshot missing prior -> no removal | Gate 1B INV-022; doc A G3 | `.../6_partial_missing_no_removal` |
| 7 | confirmed removal tombstone + event | doc A G4 | `.../7_confirmed_removal` |
| 8 | significant shrink NONE -> SUSPICIOUS/non-destructive | Gate 1B C-7 | `.../8_shrink_none_is_suspicious_non_destructive` |
| 9 | later corroboration -> distinct identity + eligible | doc A G13; Gate 1B Sec 3.1.3; EC13 | `.../9_corroborated_shrink_...` |
| 10 | duplicate same-generation replay -> NOOP | doc A G5; C-AS3 | `.../10_duplicate_same_generation_is_noop` |
| 11 | same identity after generation advanced -> re-reconcile | doc A G14/G16; T12 | `.../11_same_identity_after_generation_reconciles` |
| 12 | rollback/fault injection preserves truth | doc B T-AT2..T-AT5 | `TestReconcileRollbackLeavesNoPartialStateThenFailed` |
| 13 | same-root absolute FIFO | doc B C-A5/RC1/RC4 | `.../13_same_root_absolute_fifo` |
| 14 | different-root concurrency independence | doc B T9 | `.../14_different_root_concurrency_independence` |
| 15 | path reuse / imposter | doc A G7/G12; R8 | `.../15_path_reuse_imposter` |
| 16 | root DELETED behavior | doc A G17; C-R4 | `TestReconcileDELETEDRootRejectsWithoutMutation` |
| 17 | J6 corrective repair incl. DELETED root | doc D Sec 8.2; JD16 | `TestRepairJournalOnDeletedRoot...` |

## 7. Unresolved implementation choices (CANDIDATE freedom)

**chosen for PoC  ≠  newly frozen architecture.** None of the following changes a
frozen contract; each is an implementation/CANDIDATE selection to be reviewed.

- **`content_hash`/`hash_algorithm` required in pairs (C-C4)** — tests supply
  `hash_algorithm` alongside every hash; no behavior change to the contract.
- **PoC identity-matching subset**: implemented R1 (stable provider id),
  R3 (content hash), R4 (path+size+mtime), R5/R8 (move vs imposter), R11
  (unresolved->CONFLICT); full R0–R11 (R6/R7/R9/R10 nuances) not exhaustively
  exercised. Ambiguity is never weakened.
- **PoC assumption**: an entry's `ParentRef` is treated as the parent's canonical
  path so `canonical_path = join(ParentRef, Name)`; the adapter normalizes refs.
- **Enum storage** = constrained `text` (doc A CANDIDATE).
- **T8 primary key** = the six C-AS1 columns (doc A leaves PK implicit).
- **`event_id`** = `bigint GENERATED ALWAYS AS IDENTITY`, opaque only.
- **Append-only enforcement** = BEFORE UPDATE/DELETE triggers (doc D secondary;
  role revocation is the production primary).
- **Migration runner** = embedded ordered SQL files + `schema_migrations`.
- **Concurrency** = per-root `SELECT ... FOR UPDATE` + conditional CAS
  (doc B recommended READ COMMITTED option).
- **Shrink threshold** = Gate 1C runtime config default 0.5 (D-DEFER-7).
- **rclone mode** = external CLI `lsjson --recursive`; live remote not exercised.

## 8. Architecture contradictions

**NONE.** No frozen Gate 1B/1C semantic was changed. No contract conflict was found.