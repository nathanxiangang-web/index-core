# Gate 1B — Final Adversarial Cases

> Normative final adversarial review for Gate 1B.
> Earlier pre-rework warnings remain available in legacy PR #35 history; this
> document contains only the final semantics after Architect closeout.

## 1. Review target

Contracts reviewed:

- `GATE1B-DOMAIN-MODEL.md`
- `GATE1B-SNAPSHOT-COMPLETENESS.md`
- `GATE1B-SAFE-RECONCILE.md`
- Gate 1A frozen responsibility boundaries
- `ARCHITECTURE-INVARIANTS.md`

PASS means the final contract has a conservative, deterministic answer and does
not violate an accepted invariant.

## 2. Identity attacks

| Scenario | Expected result | Safety property | Verdict |
|---|---|---|---|
| Identical-content copy at a new path while original is PRESENT | NEW_RESOURCE or CONFLICT/UNRESOLVED; never automatic rename MATCH | hash is fingerprint, not identity | PASS |
| Same-path same-size replacement, no stable id/hash | UNRESOLVED unless the full controlled heuristic has sufficient continuity evidence | path+size alone cannot inherit resource_id | PASS |
| Same path + size + matching present mtime, no stronger signal | controlled MEDIUM continuity heuristic may MATCH a single candidate; collisions remain CONFLICT/UNRESOLVED | v1 remains usable on providers without ids/hashes while refusing weaker cases | PASS |
| Qualified provider id match | MATCHED only when assurance = STABLE_WITHIN_SCOPE and exactly one candidate exists | raw provider id is not automatically authoritative | PASS |
| Unqualified/unstable provider id | weak evidence only | driver capability is not universal | PASS |
| Cross-path hash match to PRESENT original | CONFLICT/NEW_RESOURCE, not MATCHED | identical live copies stay distinct | PASS |
| Cross-path hash match to a MISSING candidate inside recognition horizon | MATCHED only with continuity context and no conflict | rename/move continuity is explicit | PASS |
| Directory move | directory may preserve its own identity; descendants are matched independently; no batch propagation in v1 | no unsafe descendant force-match | PASS |

## 3. Completeness and deletion attacks

| Scenario | Expected result | Verdict |
|---|---|---|
| PARTIAL scan omits prior canonical resource | prior canonical state unchanged; observation log may say UNOBSERVED/UNKNOWN_COVERAGE | PASS |
| STALE/SUSPICIOUS scan omits prior resource | no missing timer/counter/removal evidence mutation | PASS |
| Known weak-error-visibility Collector returns clean success | cannot reach destructive-safe COMPLETE on that single scan | PASS |
| Unknown failure visibility | defaults non-destructive | PASS |
| Small/non-significant legitimate count decline with all other strong evidence | may still reach COMPLETE | PASS |
| Significant unexpected count decline, first observation | SUSPICIOUS; destructive action blocked | PASS |
| Significant decline independently corroborated by a later strong/fresh/error-free observation | may reach COMPLETE if all other dimensions are positive | PASS |
| Permission-denied/skipped subtree | PARTIAL; destructive action blocked | PASS |
| Suspicious empty result for previously non-empty root | SUSPICIOUS until independently explained/corroborated | PASS |

## 4. Removal attacks

| Scenario | Expected result | Verdict |
|---|---|---|
| First COMPLETE snapshot misses resource | creates internal MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT evidence only; ResourcePresence remains PRESENT | PASS |
| PARTIAL snapshot arrives while resource has removal evidence | does not advance missing_since/counter | PASS |
| Removal validation tries to re-list provider from Kernel | forbidden; Provider I/O belongs to Collector | PASS |
| Same missing observation reused as its own validation | forbidden; confirmation requires independent later accepted evidence | PASS |
| Resource still inside move-recognition horizon | cannot be CONFIRMED_REMOVED | PASS |
| Confirmed removal | logical ResourcePresence -> REMOVED tombstone + resource-removed event atomically | PASS |
| Removed object later reappears | fresh ADD semantics; previous removal history is not rewritten | PASS |

## 5. Journal attacks

| Scenario | Expected result | Verdict |
|---|---|---|
| Provider native delta says changed but reconcile does not commit change | no canonical Journal event | PASS |
| Snapshot diff reports missing but removal not confirmed | no resource-removed event | PASS |
| MOVE/RENAME + attribute UPDATE in same reconcile | ordered path-change event then resource-updated event in same generation/transaction | PASS |
| Journal disagrees with Canonical Inventory | Canonical Inventory wins; append corrective event and/or rebuild derived projection; canonical Journal history is never rewritten | PASS |
| Root ACTIVE -> DEPRECATED | root-deprecated canonical event | PASS |
| Root ACTIVE/DEPRECATED -> DELETED | root-deleted canonical event | PASS |

## 6. Ordering and failure attacks

| Scenario | Expected result | Verdict |
|---|---|---|
| Same Snapshot replay | NO-OP; no journal event; no new canonical generation | PASS |
| Two same-root inputs processed by different worker timing | serialized per-root ingress fixes admission order before work; worker/commit timing cannot reorder canonical application | PASS |
| Older admitted input tries to commit after newer one | STALE_INPUT / rejected; cannot overwrite newer canonical truth | PASS |
| Store CAS failure | losing attempt commits nothing and must recompute/abort against current generation | PASS |
| Internal reconcile error | rollback; previous canonical truth unchanged | PASS |
| Different roots reconcile concurrently | independent per-root ordering domains | PASS |

## 7. Root lifecycle attacks

| Scenario | Expected result | Verdict |
|---|---|---|
| Accidental root overlap | rejected by disjoint root ownership rule | PASS |
| Root retirement | DEPRECATED/DELETED is explicit lifecycle, not inferred from missing files | PASS |
| Deleted root receives new snapshot | rejected | PASS |
| Same physical source is re-added | new root_id; old root_id never reused | PASS |
| Deleted root history | retained as logical tombstone/audit partition; physical retention is Gate 1C | PASS |

## 8. Governance and scope check

- Formal route: Gate 1A -> Gate 1B -> Gate 1C -> Gate 2 PoC.
- No product code is authorized by Gate 1B.
- No PostgreSQL schema, migration or exact Query API is defined here.
- No final Collector is selected here.
- Scanner checkpoint/resume remains post-MVP.
- Native delta / true incremental remains post-MVP.
- Active project execution governance is **ChatGPT Architect -> Codex Executor -> PR -> ChatGPT Architect Review**. Historical GLM worker roles are not part of the active architecture process.

## 9. Final verdict

**PASS — Gate 1B semantics are internally consistent after Architect closeout.**

The contracts now explicitly prevent the previously identified failure modes:
false identity from hash/path alone, incomplete-input absence mutation,
weak-Collector destructive promotion, Kernel provider re-traversal, Journal
history rewrite, ambiguous composite changes, and commit-timing-dependent
same-root ordering.
