# Gate 1C — Collector Adapter Contract

> Implementation-facing contract between a concrete Collector adapter and the
> frozen IndexCore Snapshot / Evidence contracts, plus the Architect-facing
> contract-fit evidence for the initial adapter selection.
> Produced by the Codex Executor (Worker) for ChatGPT Architect review.
> Status: **PARTIAL_FOR_ARCH_REVIEW** (deliverable E).
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

### 2.4 `snapshot_identity` the adapter MUST feed (frozen)

The adapter maps a concrete source to the frozen identity
(`kind`, `namespace`, `version`, `value`): a stable adapter/native **revision
token** whose stability the adapter declares, OR a **versioned deterministic
digest** over normalized immutable Snapshot content. It MUST NOT include
wall-clock, admission timing, or DB timing (`GATE1C-POSTGRESQL-STORE.md` `C-AS*`).

> `DERIVED` A path is a **matching key**, not stable identity; rename changes
> name and move changes parent. The adapter may not present a mutable path as a
> stable revision token. `FACT` (INV-010; D02).

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
| 8 | snapshot_identity mapping | value mostly path (id driver-dependent) | only path | value = id (partial) / digest | only name(path) | possible native revision token |
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

`DERIVED` `skipped_scopes` may be empty or incomplete; the Kernel treats
incompleteness as evidence, not as a guarantee
(`GATE1A-COLLECTOR-CONTRACT-SKELETON.md`; `GATE1B-SNAPSHOT-COMPLETENESS.md`).

### 5.7 ResourceRoot / scope mapping

- `root_id` is Kernel-assigned and NOT a path; `scope_descriptor` is opaque to
  the Kernel and interpreted by Collector selection (`GATE1B-DOMAIN-MODEL.md`).
- AList/OpenList mount paths are unique by design, confirming disjoint scope
  boundaries are practical (`GATE1B-DOMAIN-MODEL.md`; D02 Q11).
- Neither rclone nor fsspec embeds root identity in each entry; the adapter must
  attach `root_ref` (it owns the `root_id -> remote scope` mapping).

### 5.8 `snapshot_identity` mapping feasibility

- AList/OpenList: only a **path-based matching key** is universally available;
  `id` is driver-dependent (AList) or unavailable (OpenList) — cannot serve as a
  universal `value`.
- rclone: `value` can be a native id where `IDer` exists, else a deterministic
  digest; `ListR` exposes no token and `ChangeNotifier` no replay token
  (`d03/W-D-...`).
- fsspec: only `name`/path.
- direct-provider: possibly a native revision token, but S3 `versionID` is not
  exposed through `IDer` (`d03/W-D-...`).

`INFERENCE` A universal `value` for path-based providers is a **versioned
deterministic digest** over normalized content; native tokens are an
optimization where a provider guarantees stability.

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
| AR7 | `skipped_scopes` (may be empty) | best effort; incompleteness is evidence |
| AR8 | `ResourceRoot` scope mapping | adapter owns `root_id -> remote scope`; `scope_descriptor` stays opaque |
| AR9 | `snapshot_identity` tuple | `kind`+`namespace`+`version`+`value` per Sec 2.4; no wall-clock |
| AR10 | no Kernel mutation | adapter only reports; Kernel decides |

`DERIVED` AR10 is forced by the frozen write prohibition (C Sec 6) and the
Collector/Kernel boundary (Gate 1A). `FACT`.

---

## 7. Initial adapter recommendation

`PROPOSED` (recommendation; the binding decision with alternatives is
`ADR-001-COLLECTOR-BOUNDARY.md`):

**Recommend rclone as the initial Collector adapter**, on contract-fit grounds:

1. **License (gate):** MIT — no AGPL isolation burden, directly usable as a
   process/RC boundary. AList/OpenList are AGPL-3.0 and would require strict
   isolation + separate legal review.
2. **Failure visibility (gate):** can declare `STRONG` (typed errors propagate),
   so successes are not auto-degraded to `SUSPICIOUS` (C-9a). AList/OpenList are
   `WEAK` (silent swallow), which structurally blocks treating a reported success
   as trustworthy.
3. **Identity/hash:** DRIVER_DEPENDENT, exactly like AList — so rclone is **not**
   worse on the dimensions that matter for optionality, while better on the
   gates.
4. **Operations:** single binary / RC daemon; no mandatory long-running
   third-party service.

**AList/OpenList** remain a supported adapter path (independent process / public
API boundary), suitable where the deployment already runs AList and accepts the
`WEAK` failure-visibility downgrade.

`INFERENCE` Because no candidate proves provider-complete traversal, the
decisive contract-fit difference is the combination of **license** and
**failure visibility**, both of which favor rclone.

---

## 8. Rejected alternatives and reasons

| Alternative | Status | Reason |
|-------------|--------|--------|
| Select by feature count | `REJECTED` | Explicitly forbidden by Issue #40; advanced capabilities are DRIVER_DEPENDENT and do not improve the gate dimensions. |
| Make `hash` / `provider_object_id` mandatory | `REJECTED` | Would exclude local/WebDAV/S3 providers (INV-011); already rejected in Gate 1A. |
| fsspec as initial adapter | `REJECTED` | Weaker than rclone on failure visibility, identity, and structured skips; cloud backends are external/unverifiable from the core repo. |
| AList/OpenList as initial adapter | `DEFERRED` (to a later/customer-driven need) | AGPL isolation burden + `WEAK` failure visibility; usable as a boundary adapter but not the contract-fit optimum. |
| direct-provider first | `DEFERRED` (per-provider, only where necessary) | Highest operational surface; justified only for a provider rclone/AList cannot cover. |
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

---

## 10. Acceptance cases (evidence-level)

| # | Case | Expected |
|---|------|----------|
| EC1 | Adapter cannot prove completeness | Reports `traversal_status` + evidence; Kernel decides; never sets `complete=true`. |
| EC2 | Adapter has `WEAK` failure visibility | A reported success is treated as `SUSPICIOUS` (C-9a), not `COMPLETE`. |
| EC3 | Provider id absent | `provider_object_id` omitted; `provider_identity_assurance=UNAVAILABLE`; no failure. |
| EC4 | Provider id present | `provider_identity_assurance=STABLE_WITHIN_SCOPE` (or weaker); `provider_object_id_scope` present. |
| EC5 | Rename observed | Adapter does not present the new path as a stable identity; identity/`snapshot_identity` mapping unaffected by path. |
| EC6 | `snapshot_identity` derivation | `kind`/`namespace`/`version`/`value` produced without wall-clock/admission/DB timing. |
| EC7 | Root scope mapping | Each entry carries its `root_ref`; the adapter owns `root_id -> scope`; `scope_descriptor` opaque to Kernel. |
| EC8 | Cache returned data | Freshness normalized (`CACHED_FRESH`/`STALE`/`UNKNOWN`); no provider switch leaks. |

---

## 11. Open items

| Item | Tag | Note |
|------|-----|------|
| Final initial-adapter selection | `PROPOSED` | `ADR-001` recommendation; binding only after Architect review. |
| Concrete `snapshot_identity` mapping per adapter | `PROPOSED` | digests vs native tokens; per-provider stability declaration needed. |
| `skipped_scopes` population strategy when only logs exist | `CANDIDATE` | rclone: parse logs or track skips in the adapter. |
| `provider_identity_assurance` evidence per provider | `CANDIDATE` | required only where an id is claimed. |
| `adapter_generation` (incremental) | `DEFERRED` | post-MVP; not part of the PoC contract. |