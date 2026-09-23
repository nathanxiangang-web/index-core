# Gate 1C — Collector Adapter Contract

> Implementation-facing contract between a concrete Collector adapter and the
> frozen IndexCore Snapshot / Evidence contracts, plus the Architect-facing
> contract-fit evidence for the initial adapter selection.
> Produced by the Codex Executor (Worker) for ChatGPT Architect review.
> Status: **PARTIAL_FOR_ARCH_REVIEW** (deliverable E; PR #43 D/E round-1
> rework applied: E1 identity gate, E2 digest canonicalization, E3
> skipped_scopes UNKNOWN, E4 Kernel-owned shrink corroboration; round-2 rework
> applied: final IO3 identity finalized after Kernel evaluation, rclone Gate-2
> traversal/evidence mode + PoC role split).
> Baseline: remote `main` = `6a131f17657807d9aee2921be1f286ceaff784e4`.
> Depends on `GATE1A-COLLECTOR-CONTRACT-SKELETON.md`,
> `GATE1B-SNAPSHOT-COMPLETENESS.md`, `GATE1B-DOMAIN-MODEL.md` and the FROZEN
> `GATE1C-POSTGRESQL-STORE.md` (`snapshot_identity` contract).
> Evidence inputs: D02 (`docs/research/d02/*`, `ALIST-OPENLIST-COLLECTOR-DISCOVERY-REPORT.md`)
> and D03 (`docs/research/d03/*`, `D03-COLLECTOR-GAP-COMPARISON.md`), both accepted.

---

## 0. Scope and tag system

This document defines what **any** Collector adapter MUST produce so the Kernel
can reconcile safely, and maps the accepted D02/D03 candidates against those
requirements. The final selection is an ADR recommendation (`ADR-001`); this
document provides the contract and the evidence.

Tags: `DERIVED` / `PROPOSED` / `CANDIDATE` / `DEFERRED` / `REJECTED`, with
evidence `FACT` / `INFERENCE` / `UNKNOWN`. `FACT` here means directly cited from
an accepted contract or a D02/D03 source with a file/section reference.

---

## 1. Purpose and non-goals

**Purpose.** Freeze the adapter boundary so that changing the Collector never
requires rewriting Kernel semantics (Issue #40 strong current direction):

```text
Collector Adapter  ->  normalized Snapshot + Evidence  ->  IndexCore Kernel  ->  Store Interface  ->  PostgreSQL
```

**Non-goals.** This document does NOT:

- choose by feature count (explicitly forbidden by Issue #40);
- add provider-specific concepts to the Kernel Domain;
- authorize CloudSite integration, UI, Scanner Resume, true incremental/native
  delta, or a provider-specific endpoint.

`DERIVED` The Collector reports **evidence**; the **Kernel decides** whether a
snapshot is acceptable for destructive reconcile (Gate 1A B4; Gate 1B
`GATE1B-SNAPSHOT-COMPLETENESS.md`). `FACT`.

---

## 2. Frozen contract the adapter MUST satisfy

### 2.1 `SnapshotEntry` fields and optionality (frozen)

| Field | Requirement | Why |
|-------|-------------|-----|
| `parent_ref` | REQUIRED | hierarchy |
| `path` | REQUIRED | observed path at scan time; **NOT** stable identity (INV-010) |
| `name` | REQUIRED | |
| `is_dir` | REQUIRED | |
| `size` | optional | |
| `mtime` | optional | may be absent/unreliable |
| `provider_object_id` | **optional (MUST remain optional)** | DRIVER_DEPENDENT; absent on local/WebDAV/S3 (D02 Sec 3; D03 Q1). Forcing it is **REJECTED** (would exclude providers, INV-011) |
| `hash` | **optional (MUST remain optional)** | "first stage must never require all providers to have hash" (blueprint Sec 6) |
| `metadata` | optional | |

`DERIVED` / `FACT` (`GATE1A-COLLECTOR-CONTRACT-SKELETON.md` SnapshotEntry;
`REJECTED`: making `hash` or `provider_object_id` mandatory).

### 2.2 Snapshot-level evidence (frozen)

- REQUIRED: `source_ref`, `root_ref`, `started_at`, `finished_at`,
  `traversal_status`, `error_summary`.
- CANDIDATE: `skipped_scopes`, `freshness_evidence`, `entry_count`, `byte_count`.
- DEFERRED: `adapter_generation` (post-MVP incremental).

`traversal_status` is a **contract-level adapter signal**, NOT proof of a
provider-complete snapshot. The Collector MUST NOT set `complete=true`; it
reports status + evidence and the Kernel decides (`GATE1B-SNAPSHOT-COMPLETENESS.md`).

### 2.3 Normalized enums the adapter MUST map onto (frozen)

- **Freshness:** `FRESH_DIRECT` / `FRESH_REFRESHED` / `CACHED_FRESH` / `STALE` /
  `UNKNOWN`. The Kernel does NOT understand provider switches (no AList
  `cache_bypassed`, no rclone internal flags); the adapter normalizes.
- **Failure visibility class:** `STRONG_FAILURE_VISIBILITY` /
  `WEAK_FAILURE_VISIBILITY` / `UNKNOWN_FAILURE_VISIBILITY`. `WEAK`/`UNKNOWN`
  means a reported success cannot guarantee no underlying error; a success with
  `WEAK`/`UNKNOWN` visibility is degraded to `SUSPICIOUS` (C-9/C-9a).
- **Provider identity assurance:** `STABLE_WITHIN_SCOPE` / `UNVERIFIED` /
  `UNSTABLE` / `UNAVAILABLE`. Only `STABLE_WITHIN_SCOPE` upgrades
  `provider_object_id` to STRONG. `provider_object_id_scope` is required iff
  `provider_object_id` is present.

`DERIVED` / `FACT` (`GATE1B-SNAPSHOT-COMPLETENESS.md`;
`GATE1B-DOMAIN-MODEL.md`).

#### 2.3.1 `skipped_scopes`: UNKNOWN is not confirmed-empty (PR #43 D/E review round 1, E3)

Adapters such as AList/OpenList cannot expose a structured skip set, and rclone
returns skips only as logs. "Skip visibility is UNKNOWN" MUST NOT be serialized
as an empty list: that would fabricate positive evidence of "no skips".

Encoding (no new Kernel field; uses the existing `skipped_scopes` evidence, which
is nullable in A Sec 3.5):

| Adapter state | Encoding | Meaning |
|---------------|----------|---------|
| adapter can establish nothing was skipped | concrete empty value (`[]`) | **confirmed no skips** |
| adapter cannot observe skips at all | absent / `NULL` / declared UNKNOWN | **UNKNOWN skip visibility** |
| adapter observed skips | non-empty list of prefixes + reasons | skips present |

The frozen completeness gate C-9 requires `skipped_scopes` **empty** as a
positive condition; `null`/absent (UNKNOWN) MUST NOT satisfy it — only a
confirmed empty value does. Missing/unknown skip evidence therefore degrades
exactly like missing evidence, not like a clean scan. `DERIVED` (Gate 1B
C-5/C-9); `FACT`.

### 2.4 `snapshot_identity` the adapter MUST feed (frozen)

The adapter maps a concrete source to the frozen identity
(`kind`, `namespace`, `version`, `value`): a stable adapter/native **revision
token** whose stability the adapter declares, OR a **versioned deterministic
digest** over normalized immutable Snapshot content. It MUST NOT include
wall-clock, admission timing, or DB timing (`GATE1C-POSTGRESQL-STORE.md` `C-AS*`).

> `DERIVED` A path is a **matching key**, not stable identity; rename changes
> name and move changes parent. The adapter may not present a mutable path as a
> stable revision token. `FACT` (INV-010; D02).

#### 2.4.1 `REVISION_TOKEN` admission gate (PR #43 D/E review round 1, E1)

A `provider_object_id` (or any per-object/provider ID) is **identity evidence for
one resource**, NOT a revision token for the whole scanned scope. A stable
object/folder ID normally stays constant while that object's content changes, so
using it as `snapshot_identity` would let an actually-changed scope reuse the same
identity and silently produce a **false IO3 NO-OP** (`C-AS3`).

`REVISION_TOKEN` MUST therefore NOT be produced from an object/folder ID
(AList `id`, OpenList, rclone `IDer`, S3 `ETag`, a cloud object id, ...) unless
the provider **explicitly documents** that the token represents the **entire
scanned revision/snapshot** of the scope and changes whenever any relevant state
within that scope changes. Absent such documented proof, the adapter MUST emit
the `DETERMINISTIC_DIGEST` form. A per-object ID MAY still appear in
`provider_object_id` / IdentityEvidence; it just MUST NOT be the identity.

`FACT` (frozen `C-AS*` + IO3); `DERIVED` (the object-id vs revision-token
distinction, E1).

#### 2.4.2 `DETERMINISTIC_DIGEST` canonicalization (PR #43 D/E review round 1, E2)

The digest is versioned (`snapshot_identity_version`) and taken over the
**normalized, reconcile-decision-relevant, immutable** Snapshot evidence. It MUST
cover at least:

1. the **normalized `SnapshotEntry` set** (each entry's normalized immutable
   fields; the entry set itself, not mutable paths as identity), and
2. the **normalized traversal/completeness inputs that can change the Kernel's
   classification or reconcile decision** — at minimum `traversal_status`,
   normalized `error_summary`, `skipped_scopes` (including its UNKNOWN vs
   confirmed-empty distinction, Sec 2.3.1), `freshness_evidence`, and the
   `collector_completeness_assurance` class.

It MUST exclude wall-clock, admission timing, and DB timing (frozen). This rule
exists because the same entry set can be observed first as PARTIAL/SUSPICIOUS and
later as COMPLETE: if the digest covered entries only, the first (zero-mutation)
application would record identity@G and a later, stronger observation at the same
generation would be wrongly collapsed to an IO3 NO-OP, so the Kernel could never
re-evaluate removal eligibility.

> `PROPOSED` (E2) — for the digest form the canonicalization rule-set id is the
> `snapshot_identity_namespace` (A Sec 3.8). Changing this rule set is a new
> `snapshot_identity_version`, hence a distinct identity (`C-AS1`); old and new
> digests never collapse.
>
> `DERIVED` The native `REVISION_TOKEN` path MUST also preserve the E2 guarantee:
> two submissions whose reconcile evidence is materially different MUST NOT
> collapse into the same IO3 identity. If the native token alone cannot guarantee
> this, the adapter MUST use the deterministic digest. `FACT` (IO3 correctness,
> E2).

#### 2.4.3 Final IO3 identity is finalized **after** Kernel evaluation (PR #43 D/E review round 2)

The adapter feeds a normalized **raw identity input** (the Sec 2.4.2 digest over
the normalized entry set + traversal/completeness evidence, or a native revision
token admitted under the E1 gate). The identity that **T8 / IO3 actually compare**
is the **Kernel-finalized `DETERMINISTIC_DIGEST`**, computed and finalized
**after** Kernel evaluation over the normalized entry set **plus all
reconcile-decision-relevant evidence, including Kernel-derived decision
evidence** such as `scope_shrink_corroboration` (Sec 2.5; persisted in A
Sec 3.5).

- **Gate 2 default/final form.** The finalized `DETERMINISTIC_DIGEST` is the
  default and final IO3 identity for Gate 2; a native whole-scope token never
  replaces it.
- Consequence: `NONE` / `CORROBORATED` / `CONTRADICTED` produce **distinct** IO3
  identities wherever the distinction changes reconcile eligibility.
- A native whole-scope `REVISION_TOKEN`, where admitted under E1, may be retained
  as **provenance / input basis**, but MUST NOT bypass Kernel-derived decision
  evidence: it cannot by itself define the final IO3 identity when
  evaluation-derived evidence differs.
- Persistence timing: `scope_shrink_corroboration` is set **once** by the Kernel
  during `SUBMITTED -> EVALUATED` and is immutable thereafter (A Sec 3.5, narrow
  clarification); the finalized digest is therefore stable for a given evaluated
  Snapshot.

**Failure case this closes.** At the same generation `G`, S1 observes a
significant scope shrink whose corroboration is `NONE`; it reconciles additively
(zero canonical mutation), so identity `X` is recorded at `G`. A later,
independent S2 observes the **same** reduced entry set, but its evidence lets the
Kernel derive `CORROBORATED` at `G`. If the identity were the adapter's raw
observation, S2 would also map to `X` and IO3 would wrongly NO-OP it, so the
`CORROBORATED` observation could never authorize the removal reconcile. Because
the finalized digest includes the Kernel-owned `scope_shrink_corroboration`, S2's
identity differs from `X` and S2 is **not** collapsed to a NO-OP.

`DERIVED` The identity must be the identity of the **evaluated Snapshot
semantics**, not the adapter's raw observation. `FACT` (frozen `C-AS*` + IO3;
Gate 1B Sec 3.1.3).

### 2.5 `scope_shrink_corroboration` is Kernel/evaluation-owned (PR #43 D/E review round 1, E4)

`scope_shrink_corroboration` (Gate 1B Sec 3.1.3; persisted in A Sec 3.5 as
`NONE`/`CORROBORATED`/`CONTRADICTED`) requires a **later, independently admitted
observation** plus prior canonical context. A single Collector scan MUST NOT
self-declare it.

- The adapter provides only the **raw normalized scan evidence** (entry set +
  traversal/completeness evidence, Sec 2.4.2); it never sets
  `CORROBORATED`/`CONTRADICTED` and exposes no such field.
- The **Kernel/evaluation** derives the signal from independent admitted
  observations using the adapter-provided normalized evidence, and persists it on
  / equivalent to the Snapshot being evaluated (as frozen in A), matching Gate 1B
  Sec 3.1.3: same root/scope, fresh evidence, no errors/skips,
  `STRONG_FAILURE_VISIBILITY`.
- The significant-shrink threshold value is a Gate 1C/runtime config
  (`D-DEFER-7`), not a Collector concern.

`DERIVED` (Gate 1B Sec 3.1.3, C-7/C-9; A Sec 3.5). The adapter boundary stays
neutral: no provider-specific Kernel field is introduced. `FACT`.

---

## 3. Candidate set and evaluation method

Candidates (from accepted D02/D03): **AList**, **OpenList**, **rclone**,
**fsspec** (reference), **direct-provider** (per-provider SDK).

`PROPOSED` Evaluation method (per Issue #40 "do NOT select by feature count"):

1. Score each candidate **dimension-by-dimension against the frozen contract**
   (Sec 2), not by counting features.
2. Treat **license boundary** and **failure visibility** as gates, because they
   determine whether a candidate can satisfy the contract at all and whether the
   project may use it.
3. Prefer the candidate that satisfies the highest number of **contract-critical**
   dimensions (failure visibility, snapshot-identity mapping feasibility, license,
   scope mapping), and treat equal-risk dimensions as ties.

---

## 4. Contract-fit matrix (evidence-backed)

| # | Dimension | AList | OpenList | rclone | fsspec | direct-provider |
|---|-----------|-------|----------|--------|--------|-----------------|
| 1 | traversal / provider-complete | PARTIAL (no proof) | PARTIAL (weaker; no `has_more`) | PARTIAL (contract-level + error propagation) | NO | DRIVER_DEPENDENT |
| 2 | failure-visibility assurance | **WEAK** (silent swallow) | **WEAK** (same) | **can declare STRONG** (typed + propagate) | PARTIAL (callable, no typed) | DRIVER_DEPENDENT |
| 3 | freshness evidence | cache TTL ~30m; `refresh` needs write perm | same | VFS cache; `--refresh`/`no-cache` | DirCache TTL; manual `invalidate_cache` | can be `FRESH_DIRECT` |
| 4 | provider identity / scope | DRIVER_DEPENDENT (`id`) | **UNAVAILABLE** | DRIVER_DEPENDENT (`IDer`, 38 backends) | NO (`fsid` is fs-level) | DRIVER_DEPENDENT |
| 5 | optional hash | DRIVER_DEPENDENT (content hash when present) | DRIVER_DEPENDENT | DRIVER_DEPENDENT (68 backends; real content hash) | weak (metadata hash) | DRIVER_DEPENDENT |
| 6 | skipped-scope / error evidence | **NOT AVAILABLE** | **NOT AVAILABLE** | logged, **not structured** | best (`on_error=callable`) | DRIVER_DEPENDENT |
| 7 | ResourceRoot / scope mapping | mount path unique; `provider`=driver name | same, no `id` | remote path; entry embeds no root | `fsid` fs-level | provider+account+scope |
| 8 | snapshot_identity mapping | digest (object `id` is NOT a revision token) | digest (no `id` at all) | digest (object `IDer` is NOT a revision token) | digest over `name` + evidence | `REVISION_TOKEN` only where the provider documents a whole-scope revision |
| 9 | operational complexity | service + admin token | same | single binary / RC daemon | Python lib + external backend packages | per-provider integration |
| 10 | license boundary | **AGPL-3.0** | **AGPL-3.0** | **MIT** | BSD-3-Clause | provider-dependent |

Evidence citations for each cell are in Sec 5.

---

## 5. Per-dimension findings with evidence

### 5.1 Traversal / completeness

- **AList:** full traversal possible, but result is "everything the current token
  is allowed to see at this instant per storage cache state, not a
  guaranteed-complete provider view" (`d02/W-B-ALIST-OPENLIST-PROVIDER-API.md`).
- **OpenList:** same, with no `has_more`/`page`; `len(content)==total` does not
  prove completeness (`d02/W-B-...`).
- **rclone:** "PARTIAL — contract-level completeness with explicit failure
  propagation, not proof against silent backend truncation"
  (`D03-COLLECTOR-GAP-COMPARISON.md`; `d03/W-D-COLLECTOR-GAP-MATRIX.md`).
  `List` promises completeness for a **single directory**, worded "should"
  (`fs/types.go`).
- **fsspec:** "Provider-complete traversal cannot be proven from the fsspec
  abstract contract alone" (`d03/W-C-FSSPEC-GAP-CHECK.md`).
- **direct-provider:** DRIVER_DEPENDENT; pagination differs per provider
  (`d02/W-D-PROVIDER-SNAPSHOT-MATRIX.md`).

`FACT` All candidates leave the Kernel's `COMPLETE` acceptance decision
non-trivially evidence-dependent; none can set `complete=true` (Sec 2.2).

### 5.2 Failure visibility

- **AList/OpenList (WEAK):** `storage.List` errors are silently swallowed when
  virtual mount points exist; HTTP 200 with partial results
  (`d02/W-D-PROVIDER-SNAPSHOT-MATRIX.md`, `W-B-...`).
- **rclone:** explicit directory errors propagate to a non-nil error; typed
  errors distinguishable — the adapter can reliably set `partial`/`failed` on
  explicit error (`D03-COLLECTOR-GAP-COMPARISON.md`;
  `d03/W-D-COLLECTOR-GAP-MATRIX.md`). It still cannot detect silent backend
  truncation and returns no structured skipped-set.
- **fsspec:** `walk(on_error=callable)` / `cat(on_error="return")` give per-path
  visibility, but default `omit` silently skips and there is no typed hierarchy
  (`d03/W-C-FSSPEC-GAP-CHECK.md`).
- **direct-provider:** DRIVER_DEPENDENT.

`INFERENCE` Under Gate 1B C-9a, an adapter that can only claim `WEAK` visibility
degrades every success to `SUSPICIOUS`, blocking destructive reconcile. This
makes failure visibility a **gate**, not a scored nicety.

### 5.3 Freshness

- **AList/OpenList:** cache TTL ~30m; `refresh=true` bypasses cache but requires
  write permission; no staleness field; empty dirs not cached
  (`d02/W-...`).
- **rclone:** adapter knows VFS cache config; `--refresh`/`no-cache` options;
  stale within TTL with no flag (`GATE1A-COLLECTOR-CONTRACT-SKELETON.md`;
  `d03/W-D-...`).
- **fsspec:** `DirCache` TTL; only manual `invalidate_cache`
  (`d03/W-C-...`).
- **direct-provider:** can reach `FRESH_DIRECT` if no cache layer exists
  (`GATE1B-SNAPSHOT-COMPLETENESS.md`).

### 5.4 Provider identity / scope

- **AList:** `id` populated from `obj.GetID()`; non-empty only for id-based
  drivers; empty for local/WebDAV; commented out for S3
  (`d02/W-B-...`).
- **OpenList:** removed the `id` field entirely — **UNAVAILABLE** regardless of
  driver (`d02/W-B-...`).
- **rclone:** optional `IDer` interface (38 backends); local returns `""`;
  S3/WebDAV do not implement it (`d03/W-A-...`). DRIVER_DEPENDENT.
- **fsspec:** no per-object id; `fsid` is filesystem-level
  (`d03/W-C-...`).
- **direct-provider:** native ids on cloud providers; whether
  `STABLE_WITHIN_SCOPE` must be declared by the adapter
  (`GATE1B-DOMAIN-MODEL.md`).

### 5.5 Optional hash

- **AList/OpenList:** `hash_info` only when the driver populates it; content
  hash when present (`d02/W-B-...`, `W-D-...`).
- **rclone:** `Hashes()` on the core interface (68 backends); WebDAV
  `hash.None`; S3 ETag is not a content hash for multipart
  (`d03/W-D-...`).
- **fsspec:** default `checksum`/`ukey` are property-dict hashes, not content
  hashes (`d03/W-C-...`).

`FACT` `hash` MUST remain optional (Sec 2.1); no candidate can guarantee it.

### 5.6 Skipped-scope / error evidence

- **AList/OpenList:** NOT AVAILABLE from API; adapter must infer from missing
  subtrees (`GATE1A-COLLECTOR-CONTRACT-SKELETON.md`).
- **rclone:** skipped dirs are logged with path, not returned as a structured
  set; RC has no `failedDirs` (`d03/W-D-...`).
- **fsspec:** best of the four — per-path callbacks, but sync/async
  inconsistency (`d03/W-C-...`).

`DERIVED` For every candidate above, skip visibility is **UNKNOWN** in the
missing case (AList/OpenList: not observable; rclone: logged, not structured;
fsspec: only where callbacks are configured). Per Sec 2.3.1 this MUST be encoded
as absent/`NULL`, never as an empty `skipped_scopes`; it therefore does NOT
satisfy the C-9 "`skipped_scopes` empty" positive condition. `skipped_scopes` may
be empty or incomplete; the Kernel treats incompleteness as evidence, not as a
guarantee (`GATE1A-COLLECTOR-CONTRACT-SKELETON.md`;
`GATE1B-SNAPSHOT-COMPLETENESS.md`).

### 5.7 ResourceRoot / scope mapping

- `root_id` is Kernel-assigned and NOT a path; `scope_descriptor` is opaque to
  the Kernel and interpreted by Collector selection (`GATE1B-DOMAIN-MODEL.md`).
- AList/OpenList mount paths are unique by design, confirming disjoint scope
  boundaries are practical (`GATE1B-DOMAIN-MODEL.md`; D02 Q11).
- Neither rclone nor fsspec embeds root identity in each entry; the adapter must
  attach `root_ref` (it owns the `root_id -> remote scope` mapping).

### 5.8 `snapshot_identity` mapping feasibility

- AList/OpenList: only a path-based **matching key** is universally available;
  `id` is driver-dependent (AList) or unavailable (OpenList). Even when present,
  `id` is a **per-object** identifier, not a revision token for the scanned scope
  (Sec 2.4.1) — it MUST NOT be the identity.
- rclone: `IDer` gives a per-object `id` on 38 backends (empty for local; absent
  for S3/WebDAV). That `id` is **not** a whole-scope revision token; `ListR`
  exposes no token and `ChangeNotifier` no replay token (`d03/W-D-...`). Absent a
  documented scope-level revision, `value` MUST be the deterministic digest.
- fsspec: only `name`/path — digest form.
- direct-provider: a native revision token is usable **only** if the provider
  documents it as a whole-scope revision/snapshot token; S3 `versionID` is not
  exposed through `IDer` and is per-object, so it is not implicitly a token
  (`d03/W-D-...`).

`DERIVED`/`INFERENCE` The universal `value` for these adapters is a **versioned
deterministic digest** over the normalized entry set + decision-relevant evidence
(Sec 2.4.2). A native `REVISION_TOKEN` is an optimization admitted only under the
E1 gate; per-object IDs stay in `provider_object_id` / IdentityEvidence.

### 5.9 Operational complexity

- AList/OpenList: run the service + admin token; forcing refresh needs write
  permission (`d02/W-B-...`).
- rclone: single binary; RC (`operations/list`) needs an `rclone rcd` daemon, or
  use CLI `lsjson`.
- fsspec: Python lib plus separately-installed cloud backend packages, whose
  identity/completeness cannot be judged from the core repo
  (`d03/W-C-...`).
- direct-provider: per-provider SDK/auth/rate-limit integration; largest
  operations surface.

### 5.10 License boundary

- **AList: AGPL-3.0. OpenList: AGPL-3.0.** (`d02/W-D-PROVIDER-SNAPSHOT-MATRIX.md`).
  Project policy: do not copy or link their source into the Kernel; prefer an
  independent-process / public-API boundary; separate license review before
  actual adoption. This document is not a legal conclusion.
- **rclone: MIT** (`d03/W-D-COLLECTOR-GAP-MATRIX.md`).
- **fsspec: BSD-3-Clause** (`d03/W-C-FSSPEC-GAP-CHECK.md`).

`FACT` License boundary is a **contract gate** (Issue #40 hard constraint:
"do not copy license-incompatible donor code").

---

## 6. Adapter requirements (what every adapter MUST provide)

`PROPOSED` These are the normalized outputs the Kernel will consume; they are
derived from Sec 2 and do not add Kernel concepts.

| # | Adapter output | Requirement |
|---|----------------|-------------|
| AR1 | normalized `SnapshotEntry` stream | per Sec 2.1; optional fields stay optional |
| AR2 | `traversal_status` + `error_summary` | contract-level signal; never claims `complete=true` |
| AR3 | failure-visibility class | one of `STRONG`/`WEAK`/`UNKNOWN`; honest self-declaration |
| AR4 | freshness evidence | one of the normalized enum values; no provider switches leak |
| AR5 | provider identity assurance + scope | `STABLE_WITHIN_SCOPE`/`UNVERIFIED`/`UNSTABLE`/`UNAVAILABLE`; scope iff id present |
| AR6 | optional `hash` + `hash_algorithm` | only when available; never fabricated |
| AR7 | `skipped_scopes` (confirmed-empty vs UNKNOWN) | best effort; UNKNOWN encoded as absent/`NULL`, never as empty; incompleteness is evidence (Sec 2.3.1) |
| AR8 | `ResourceRoot` scope mapping | adapter owns `root_id -> remote scope`; `scope_descriptor` stays opaque |
| AR9 | `snapshot_identity` tuple | `kind`+`namespace`+`version`+`value` per Sec 2.4; no wall-clock |
| AR10 | no Kernel mutation | adapter only reports; Kernel decides |
| AR11 | no `scope_shrink_corroboration` self-declaration | Kernel/evaluation-owned; the adapter supplies only raw normalized evidence (Sec 2.5) |

`DERIVED` AR10 is forced by the frozen write prohibition (C Sec 6) and the
Collector/Kernel boundary (Gate 1A). `FACT`.

---

## 7. Initial adapter recommendation and PoC role split

`PROPOSED` (recommendation; the binding decision with alternatives is
`ADR-001-COLLECTOR-BOUNDARY.md`).

### 7.1 Gate-2 rclone traversal/evidence mode (PR #43 D/E round 2)

The concrete Gate-2 rclone mode considered is RC `operations/list` with
`recurse=true` (or the equivalent CLI `lsjson --recursive`). On the accepted
D02/D03 evidence this mode **cannot establish confirmed no skips on a successful
scan**:

- `operations/list` returns only a `list` array with **no completeness marker,
  total, or cursor**; pagination is internal and not exposed
  (`d03/W-B-RCLONE-COMPLETENESS-RC.md` Q7; `fs/operations/rc.go:27-80`).
- For backends **without** native `ListR` the traversal falls back to
  `listRwalk`, whose callback swallows per-directory list errors, logs them, and
  returns only the **last** one; for backends **with** native `ListR` the error
  semantics are backend-specific. Either way there is **no structured skipped-set**
  (`d03/W-B-...` Q4/Q5).
- Accepted D03 verdict: **no** examined tool can detect a backend that returns a
  partial list **without an error** (silent truncation); a snapshot builder may
  rely on "error returned ⇒ scan incomplete" but **cannot** rely on "no error ⇒
  scan complete" (`d03/W-D-COLLECTOR-GAP-MATRIX.md`, finding F3 and the
  silent-truncation finding).

`DERIVED` Consequently `skipped_scopes` for this mode MUST be encoded as
absent/`NULL` (UNKNOWN, Sec 2.3.1) — never `[]`. A successful rclone RC scan
therefore does **not** satisfy the frozen completeness gate C-9's
"`skipped_scopes` empty" positive condition, and cannot by itself authorize a
destructive/COMPLETE reconcile. The absence of a structured skip list is **not**
positive evidence of no skips. `FACT`.

### 7.2 PoC role split (not a blanket rclone selection)

Because rclone RC cannot establish confirmed-no-skips, rclone is **NOT accepted
as the initial adapter for the PoC role that must exercise COMPLETE/removal
semantics**. The roles are split:

1. **Additive-only PoC role — rclone is acceptable (leading candidate).** Where a
   reconcile is additive (additions only), an UNKNOWN skip set can only **delay**
   additions; it can never authorize removal. On the contract-fit gates that
   matter here — license (MIT) and failure visibility (`STRONG` **conditional on
   the mode actually used**, typed + propagated) — rclone leads: AList/OpenList
   are `WEAK` (silent swallow) and AGPL-3.0, and would require strict isolation +
   legal review.
2. **COMPLETE/removal-semantics PoC role — requires a positive completeness
   signal; NOT satisfied by rclone RC.** This role needs an adapter/traversal mode
   that can emit `skipped_scopes = []` from a **positive** end-of-enumeration
   signal (every directory/page enumerated with the provider explicitly reporting
   no truncation). That is a **per-provider** capability, not an rclone-wide one.
   The concrete candidate path is a **direct-provider adapter** whose provider
   listing API exposes an explicit exhaust/truncation flag, validated per provider
   before `[]` may be emitted (Sec 2.3.1). Until such a provider is validated
   this role is **BLOCKED/DEFERRED**; it MUST NOT be faked by treating UNKNOWN as
   empty.

**AList/OpenList** remain a supported boundary adapter path (independent process /
public API boundary) where the deployment already runs AList and accepts the
`WEAK` failure-visibility downgrade; they do not close the completeness gap
either.

`INFERENCE` Because no examined candidate proves provider-complete traversal, the
decisive contract-fit difference for the **additive** role remains license +
failure visibility (both favor rclone). The **destructive** role is gated by a
separate positive-completeness property that none of the examined candidates
currently provides, so it is a distinct PoC with a distinct evidence requirement.

---

## 8. Rejected alternatives and reasons

| Alternative | Status | Reason |
|-------------|--------|--------|
| Select by feature count | `REJECTED` | Explicitly forbidden by Issue #40; advanced capabilities are DRIVER_DEPENDENT and do not improve the gate dimensions. |
| Make `hash` / `provider_object_id` mandatory | `REJECTED` | Would exclude local/WebDAV/S3 providers (INV-011); already rejected in Gate 1A. |
| fsspec as initial adapter | `REJECTED` | Weaker than rclone on failure visibility, identity, and structured skips; cloud backends are external/unverifiable from the core repo. |
| AList/OpenList as initial adapter | `DEFERRED` (to a later/customer-driven need) | AGPL isolation burden + `WEAK` failure visibility; usable as a boundary adapter but not the contract-fit optimum. |
| direct-provider first | `DEFERRED` (per-provider, only where necessary) | Highest operational surface; justified only for a provider rclone/AList cannot cover. |
| rclone as the initial adapter for the COMPLETE/removal PoC | `REJECTED` | rclone RC cannot establish confirmed-no-skips (Sec 7.1); `skipped_scopes` must stay UNKNOWN and must not be encoded as confirmed-empty; rclone is retained for the additive-only role (Sec 7.2). |
| Exposing a global collection cursor | `REJECTED` | Conflicts with the frozen no-cross-root-order rule and per-root identity/scope. |

---

## 9. Consistency mapping (Issue #40)

| Issue #40 check | Where it is satisfied in E |
|-----------------|----------------------------|
| 10 — Collector-specific fields do not leak into Kernel Domain | Sec 1 non-goals, Sec 2.3 normalized enums, AR3–AR9 (adapter normalizes; Kernel sees neutral enums) |
| completeness cannot be asserted by Collector | Sec 2.2, AR2–AR3 |
| optional provider id/hash preserved | Sec 2.1, AR5–AR6 |
| selection not by feature count | Sec 3 evaluation method, Sec 8 rejected alternatives |
| Architect-facing ADR recommendation | Sec 7 + `ADR-001-COLLECTOR-BOUNDARY.md` |
| PR #43 D/E round-1 fixes (E1–E4) | Sec 2.4.1 (object id ≠ revision token), Sec 2.4.2 (digest canonicalization), Sec 2.3.1 (skipped_scopes UNKNOWN), Sec 2.5 (shrink corroboration Kernel-owned) |
| PR #43 D/E round-2 fixes | Sec 2.4.3 (final IO3 identity finalized after Kernel evaluation, includes Kernel-owned `scope_shrink_corroboration`; EC13), Sec 7.1/7.2 (rclone Gate-2 mode + PoC role split; EC14) |

---

## 10. Acceptance cases (evidence-level)

| # | Case | Expected |
|---|------|----------|
| EC1 | Adapter cannot prove completeness | Reports `traversal_status` + evidence; Kernel decides; never sets `complete=true`. |
| EC2 | Adapter has `WEAK` failure visibility | A reported success is treated as `SUSPICIOUS` (C-9a), not `COMPLETE`. |
| EC3 | Provider id absent | `provider_object_id` omitted; `provider_identity_assurance=UNAVAILABLE`; no failure. |
| EC4 | Provider id present | `provider_identity_assurance=STABLE_WITHIN_SCOPE` (or weaker); `provider_object_id_scope` present. |
| EC5 | Rename observed | Adapter does not present the new path as a stable identity; identity/`snapshot_identity` mapping unaffected by path. |
| EC6 | `snapshot_identity` derivation | `kind`/`namespace`/`version`/`value` produced without wall-clock/admission/DB timing; `REVISION_TOKEN` only under the E1 gate, else `DETERMINISTIC_DIGEST` over entry set + decision-relevant evidence (Sec 2.4). |
| EC7 | Root scope mapping | Each entry carries its `root_ref`; the adapter owns `root_id -> scope`; `scope_descriptor` opaque to Kernel. |
| EC8 | Cache returned data | Freshness normalized (`CACHED_FRESH`/`STALE`/`UNKNOWN`); no provider switch leaks. |
| EC9 | Provider object id present but no documented whole-scope revision token | `value` MUST NOT be the object id; use `DETERMINISTIC_DIGEST`; the object id stays in `provider_object_id`/IdentityEvidence. No false IO3 NO-OP when the scope changed (E1). |
| EC10 | Same generation, identical entries, first observation PARTIAL then a stronger COMPLETE observation | The two observations MUST NOT collapse to the same IO3 identity (the digest includes decision-relevant evidence); the later one is reconciled, not treated as a NO-OP (E2). |
| EC11 | Adapter cannot observe skips (AList/OpenList; rclone logs-only) | `skipped_scopes` encoded as absent/`NULL` (UNKNOWN), never empty; C-9's "empty" positive condition is NOT satisfied (E3). |
| EC12 | A scope shrink is observed, then a later independent admitted observation confirms it | The adapter sets no corroboration field; the Kernel/evaluation derives `CORROBORATED` from the independent observation and persists it on the evaluated Snapshot (E4, Gate 1B Sec 3.1.3). |
| EC13 | Same generation `G`: S1 observes a significant shrink with `scope_shrink_corroboration = NONE` and reconciles additively (zero canonical mutation, identity `X`); an independent S2 observes the same reduced entry set and the Kernel derives `CORROBORATED` at `G` | S2's finalized IO3 identity differs from `X` (the digest is finalized after evaluation and includes the Kernel-owned `scope_shrink_corroboration`); S2 is **not** an IO3 NO-OP and MAY authorize the removal reconcile (Sec 2.4.3). |
| EC14 | rclone RC (`operations/list`, recurse) completes successfully with no structured skip set | `skipped_scopes` is encoded absent/`NULL` (UNKNOWN), never `[]`; C-9's "empty" positive condition is NOT satisfied; rclone is not used for the COMPLETE/removal PoC role (Sec 7.1/7.2). |

---

## 11. Open items

| Item | Tag | Note |
|------|-----|------|
| Final initial-adapter selection + PoC role split | `PROPOSED` | `ADR-001`; rclone is the leading candidate for the **additive-only** PoC role, NOT accepted for the COMPLETE/removal role (Sec 7.1/7.2); binding only after Architect review. |
| Concrete `snapshot_identity` mapping per adapter | `PROPOSED` | default is the versioned deterministic digest (Sec 2.4.2); `REVISION_TOKEN` only where the provider documents a whole-scope revision (E1); the IO3 identity is finalized by the Kernel after evaluation (Sec 2.4.3). |
| `skipped_scopes` population strategy / positive completeness signal | `CANDIDATE` | UNKNOWN stays absent/`NULL` until confirmed (Sec 2.3.1); for the COMPLETE/removal role only a **positive** provider end-of-enumeration signal may yield `[]` (Sec 7.2) — rclone log parsing does NOT establish it. |
| `scope_shrink_corroboration` derivation + persistence | `DERIVED` (Kernel-owned) | Kernel/evaluation derives from independent admitted observations (Sec 2.5); the adapter never sets it; set once during `SUBMITTED -> EVALUATED` (A Sec 3.5) and included in the finalized IO3 digest (Sec 2.4.3). |
| `provider_identity_assurance` evidence per provider | `CANDIDATE` | required only where an id is claimed. |
| `adapter_generation` (incremental) | `DEFERRED` | post-MVP; not part of the PoC contract. |