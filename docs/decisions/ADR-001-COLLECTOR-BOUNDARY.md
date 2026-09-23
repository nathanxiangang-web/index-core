# ADR-001 — Collector Boundary and Initial Adapter Selection

> Status: **ACCEPTED** — Architect-approved in PR #43 (ARCHITECT FINAL ACCEPTANCE
> — Gate 1C CLOSED, final verification head `7a3b32f`).
> PR #43 D/E round-2: the Gate-2 rclone traversal/evidence mode was reviewed and
> **cannot establish confirmed-no-skips**; rclone is therefore kept as the
> leading candidate for the additive-only PoC role and is **NOT** accepted for
> the COMPLETE/removal-semantics PoC role (E Sec 7.1/7.2).
> Date: 2026-09-24. Gate: 1C (deliverable E).
> Related: `docs/architecture/GATE1C-COLLECTOR-ADAPTER-CONTRACT.md`,
> `GATE1B-SNAPSHOT-COMPLETENESS.md`, `GATE1B-DOMAIN-MODEL.md`.
> Evidence: accepted D02 (`docs/research/d02/*`,
> `ALIST-OPENLIST-COLLECTOR-DISCOVERY-REPORT.md`) and D03
> (`docs/research/d03/*`, `D03-COLLECTOR-GAP-COMPARISON.md`).

---

## Context

The project needs a Collector layer that produces a normalized Snapshot + Evidence
for the Kernel, behind the frozen boundary:

```text
Collector Adapter -> normalized Snapshot + Evidence -> IndexCore Kernel -> Store Interface -> PostgreSQL
```

Changing the Collector must not require rewriting Kernel semantics (Issue #40
strong current direction). Accepted D02/D03 research evaluated AList, OpenList,
rclone and fsspec; the contract-fit analysis (deliverable E) shows that **no
candidate can prove a provider-complete traversal**, and that the decisive
differences are:

- **license boundary** (Issue #40: no license-incompatible donor code), and
- **failure-visibility assurance** (Gate 1B C-9a: a `WEAK`/`UNKNOWN` success is
  degraded to `SUSPICIOUS`, blocking destructive reconcile).

Advanced capabilities (identity, hash, delta) are DRIVER_DEPENDENT for every
candidate and therefore cannot be the selection basis. Issue #40 explicitly
forbids selecting by feature count.

---

## Decision

1. **rclone is the leading candidate for the additive-only PoC role.** It is
   wrapped behind the adapter contract (process / RC boundary; no rclone source
   linked into the Kernel). Its `STRONG_FAILURE_VISIBILITY` is **conditional on
   the traversal mode actually used**, and its `snapshot_identity` defaults to
   the deterministic digest (per-object `IDer` ids are NOT revision tokens) —
   see Rationale and E Sec 2.4.
2. **rclone is NOT accepted as the initial adapter for the COMPLETE/removal
   semantics PoC role.** On the accepted D02/D03 evidence, the Gate-2 rclone mode
   (RC `operations/list`, recurse) cannot establish confirmed no skips on a
   successful scan: there is no structured skip set, and no examined tool can
   detect silent backend truncation ("error ⇒ incomplete" holds; "no error ⇒
   complete" does not). `skipped_scopes` therefore stays UNKNOWN (absent/`NULL`),
   which does **not** satisfy the frozen completeness gate C-9 (E Sec 7.1).
3. **PoC role split.** The additive-only role uses rclone; the COMPLETE/removal
   role requires an adapter/traversal mode that emits `skipped_scopes = []` from a
   **positive** end-of-enumeration signal — a per-provider property. The concrete
   candidate path is a **direct-provider adapter** with an explicit
   exhaust/truncation flag, validated per provider before `[]` may be emitted;
   until then that role is BLOCKED/DEFERRED and MUST NOT be faked by treating
   UNKNOWN as empty (E Sec 7.2).
4. **AList/OpenList: supported boundary adapter** for deployments that already
   run AList/OpenList, used through an independent-process / public-API boundary
   with a separate license review (AGPL-3.0); they do not close the completeness
   gap either.
5. **direct-provider: deferred, per-provider**, used only where neither rclone
   nor AList can cover a required provider (and the candidate path for the
   positive-completeness evidence above).
6. The adapter normalization contract is frozen in
   `GATE1C-COLLECTOR-ADAPTER-CONTRACT.md` (Sec 2, Sec 6): optional
   `provider_object_id`/`hash`, normalized freshness/failure-visibility/identity
   enums, `snapshot_identity` tuple mapping, and no Kernel write path.

---

## Rationale (contract-fit, not feature count)

| Criterion | rclone | AList/OpenList | Effect |
|-----------|--------|----------------|--------|
| License | **MIT** | **AGPL-3.0** | rclone usable directly as a boundary; AList needs strict isolation + legal review. |
| Failure visibility (gate) | can declare **STRONG in the traversal mode actually used** (conditional; typed + propagate) | **WEAK** (silent swallow) | Where the mode propagates errors, rclone yields a trustworthy success signal; AList successes degrade to `SUSPICIOUS`. `STRONG` is NOT claimed for every backend/mode (E3). |
| Provider identity | DRIVER_DEPENDENT (`IDer`, 38 backends) — a **per-object** id, NOT a revision token | DRIVER_DEPENDENT (AList) / **UNAVAILABLE** (OpenList) | rclone no worse; OpenList strictly weaker. For all candidates the identity defaults to the deterministic digest (E1). |
| Optional hash | DRIVER_DEPENDENT (68 backends) | DRIVER_DEPENDENT | tie; `hash` stays optional either way. |
| Completeness | PARTIAL (contract-level + error propagation) | PARTIAL (no proof) | tie at the contract ceiling; Kernel decides. |
| Skip evidence (C-9) | **cannot establish confirmed-no-skips** (no structured skip set; silent truncation undetectable) → `skipped_scopes` UNKNOWN | **NOT AVAILABLE** | Gates the **destructive** role: UNKNOWN does not satisfy C-9, so rclone is limited to the additive role until a positive-completeness path exists (E Sec 7.1). |
| Operations | single binary / RC daemon | long-running service + admin token | rclone simpler to isolate. |

`INFERENCE` Since completeness is a tie at "cannot prove", the decisive
contract-fit gates are license and failure visibility — both favor rclone for
the **additive** role. The **destructive** role is gated separately by positive
skip evidence, which none of these candidates provides (E Sec 7.1).

---

## Alternatives considered

| Alternative | Disposition | Reason |
|-------------|-------------|--------|
| Select by feature count | **REJECTED** | Forbidden by Issue #40; advanced capabilities are DRIVER_DEPENDENT and do not change the gates. |
| AList/OpenList as initial adapter | **DEFERRED** | AGPL isolation burden + `WEAK` failure visibility; retained as a boundary adapter. |
| fsspec as initial adapter | **REJECTED** | Weaker on failure visibility/identity/structured skips; cloud backends external and unverifiable from the core repo. |
| direct-provider first | **DEFERRED** | Largest operational surface; justified only where necessary. |
| Make `hash`/`provider_object_id` mandatory | **REJECTED** | Excludes local/WebDAV/S3; violates INV-011 (already rejected in Gate 1A). |
| Global collection cursor | **REJECTED** | Conflicts with the frozen no-cross-root-order rule and per-root identity/scope. |

---

## Consequences

**Positive**

- Kernel semantics stay Collector-agnostic; swapping adapters does not reopen
  Gate 1B/1C.
- A trustworthy `STRONG` failure-visibility signal is available **where the
  chosen traversal mode propagates errors**, so an "error ⇒ incomplete" signal is
  evidence-based.
- No AGPL contamination risk in the initial (additive) path.

**Negative / risks**

- rclone still cannot prove provider-complete traversal; the Kernel must rely on
  evidence + C-9/C-9a.
- `STRONG_FAILURE_VISIBILITY` is **conditional on the traversal mode**; the
  adapter MUST declare the visibility class per mode rather than as a blanket
  property (E3).
- rclone identity/hash are DRIVER_DEPENDENT; per-object `IDer` ids are NOT
  revision tokens, so `snapshot_identity` uses the versioned deterministic digest
  by default (E1). A native revision token is admitted only where a provider
  documents a whole-scope revision.
- rclone returns no structured skipped set and cannot detect silent backend
  truncation, so `skipped_scopes` is UNKNOWN (absent/`NULL`) rather than
  confirmed-empty and cannot satisfy C-9. **Consequence:** rclone cannot serve
  the COMPLETE/removal-semantics PoC role; that role is deferred to a
  positive-completeness evidence path (per-provider, E Sec 7.2).
- RC requires an `rclone rcd` daemon or CLI invocation; the adapter owns that
  operational choice.

**Follow-ups**

- Per-adapter `snapshot_identity` mapping (digest by default; native token only
  under the E1 whole-scope-revision gate); the IO3 identity is finalized by the
  Kernel after evaluation and includes Kernel-owned decision evidence (E
  Sec 2.4.3).
- Per-provider `provider_identity_assurance` evidence.
- Per-mode `STRONG` failure-visibility declaration (not a blanket claim).
- Positive-completeness evidence path for the COMPLETE/removal role
  (per-provider exhaust/truncation flag); `skipped_scopes=[]` only from that
  signal, never from log parsing; UNKNOWN stays absent/`NULL` until then.

---

## Compliance with frozen contracts

| Frozen rule | How ADR-001 complies |
|-------------|----------------------|
| Collector reports evidence; Kernel decides (Gate 1A B4) | Adapter never sets `complete=true`; Kernel owns acceptance. |
| Optional `provider_object_id`/`hash` (INV-011) | Preserved; no provider excluded. |
| `snapshot_identity` = (`kind`,`namespace`,`version`,`value`), no wall-clock | Adapter maps a documented whole-scope revision token, else a versioned deterministic digest over entry set + decision-relevant evidence; per-object ids are not tokens (E1/E2); the Kernel finalizes the IO3 identity after evaluation and owns its canonicalization namespace/version (E Sec 2.4.3). |
| `scope_shrink_corroboration` is not self-declared | Kernel/evaluation-owned; the adapter supplies only raw normalized evidence (E4); the finalized IO3 identity includes it (E Sec 2.4.3). |
| C-9 skip evidence (`skipped_scopes` empty) | rclone RC cannot establish it; `skipped_scopes` stays UNKNOWN and rclone is limited to the additive-only role (E Sec 7.1/7.2). |
| Root visibility / scope disjointness (Gate 1B) | Adapter owns `root_id -> scope`; `scope_descriptor` opaque to Kernel. |
| No Collector field leaks into Kernel Domain | Normalized enums only (Sec 2.3/AR3–AR9 of E). |
| No new gate; no silent Gate 1B change | This ADR adds no Kernel semantics. |

---

## Evidence

- `d02/W-B-ALIST-OPENLIST-PROVIDER-API.md` — AList `id` per-driver; OpenList removed `id`; silent partial failure; cache TTL; refresh needs write permission.
- `d02/W-D-PROVIDER-SNAPSHOT-MATRIX.md` — AList/OpenList hard gaps; AGPL-3.0 license text.
- `d03/W-A-RCLONE-IDENTITY-METADATA.md` — optional `IDer`; local/S3/WebDAV absence.
- `d03/W-B-RCLONE-COMPLETENESS-RC.md` — RC `operations/list` returns only a `list` array (no completeness marker/total/cursor, Q7); `listRwalk` swallows per-directory errors and returns only the last one (Q4/Q5); no `failedDirs`/`skippedDirs` stat.
- `d03/W-C-FSSPEC-GAP-CHECK.md` — fsspec completeness NO; BSD-3-Clause; metadata hashes.
- `d03/W-D-COLLECTOR-GAP-MATRIX.md` — cross-candidate matrix; MIT license for rclone; F3 "PARTIAL — not proof against silent backend truncation"; none of rclone/fsspec/AList can detect silent truncation, so "error ⇒ incomplete" holds but "no error ⇒ complete" does not.
- `D03-COLLECTOR-GAP-COMPARISON.md` — rclone PARTIAL with explicit failure propagation; fsspec NO.