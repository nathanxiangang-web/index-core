# ADR-004: distinct live paths can contain equal content

Status: accepted architecture clarification for Issue117; implementation requires
tests and exact-head CI before deployment. No schema or public API change.

## Cause and scope

R3 in the original Domain Model simultaneously described a live cross-path hash
match as CONFLICT and the no-MISSING copy case as NEW_RESOURCE. The implementation
chose CONFLICT. Thus real copies could never enter Canonical, and multiple hash
matches could even shadow unique same-path continuity. Four source-present paths
were affected during Issue115 recovery. This is a Kernel identity problem, not a
frontend category or cache problem.

## Decision and module boundaries

- Stable provider-ID precedence (R1/R10) is unchanged.
- Without that stronger match, duplicate canonical/observed paths remain conflicts.
- A unique same-path matching hash establishes continuity even if other paths
  contain equal content. A contradictory same-path hash remains an R8 new object,
  not a cross-path merge. Hashless same-path rows still require R4 evidence.
- At a new path, when all matching canonical candidates have no MISSING evidence,
  create a distinct resource. Never move or merge those candidates. This includes
  multiple existing live copies; content equality is not object identity.
- A single MISSING candidate can still move only inside the existing horizon,
  with no other same-hash candidate and exactly one observed destination. If its
  old path is also observed, or multiple destinations compete, do not guess.
- Mixed live/MISSING or multiple MISSING candidates remain conflicts; expired
  unique MISSING candidates remain unresolved. No new absence authority is added.
- REMOVED IDs remain tombstones. Re-observation creates a fresh ID, including when
  a live copy of that content exists elsewhere.

`reconcile` owns these decisions and shares the same observation index with
removal-context evaluation. Collector keeps hashes intact. Store retains existing
root lock, generation CAS and atomic Canonical/Journal transaction. Q5/Q6/Q8 and
consumers use normal Canonical records, never UI overlays or repair SQL.

IO3 advances to base `v3-live-copies` and coverage `v6-directory-coverage`, so an
earlier conflict-only observation cannot suppress re-evaluation after upgrade.
Coverage authorization/proof remains unchanged. No collector or UI dependency is
introduced into Kernel; future adapters must supply honest evidence, not invent
stable provider identities or strip hashes to evade these rules.

## Verification and operational constraints

Pure tests cover independent copies, same-path continuity, R8, provider IDs,
ambiguous moves, horizon, tombstones and removal-context agreement. The real
PostgreSQL + HTTP adapter fixture tests rollback, replay, Q5/Q6/Q8, independent
authorized deletion and fresh-ID reappearance.

Production deployment must preserve the current additive-only Root configuration
and absent topology credential. Re-observe the four known parent scopes via the
existing queue, without downloading media, modifying drive files, resurrecting
canonical rows or changing acquisition tasks. Binary rollback is retained; no
database history is erased. Automatic mixed-root deletion is still Issue115 and
is explicitly outside this fix. Production counts are evidence, not assumed here.
