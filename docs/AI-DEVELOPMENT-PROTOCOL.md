# AI-Assisted Development Protocol

This protocol defines how AI sessions plan, execute, verify, and hand off IndexCore work. It is intentionally designed for a single Executor and a separate architecture/review role.

## 1. Authority hierarchy

Use this order when information conflicts:

1. Explicit current Owner authorization for the task.
2. Current main, accepted Issues/PRs, exact-head CI, and merged code.
3. AGENTS.md and accepted architecture invariants / ADRs.
4. PROJECT-CONTEXT.md, PROJECT-STATE.md, and NEXT-ACTIONS.md.
5. Module documentation and tests.
6. Historical reports and chat history.

A proposal, draft PR, test-only prototype, or open Issue is not an accepted architecture decision. Mark proposals clearly.

## 2. Roles

### Owner

Defines the desired outcome, priority, and any high-risk authorization.

### Architect

- Recovers the project state and exact baseline.
- Classifies the requirement and protects module boundaries.
- Writes the bounded task and acceptance criteria.
- Reviews the implementation and evidence independently.
- Returns exactly one disposition: PASS, FIX, or ARCHITECTURAL_BLOCKER.

The Architect does not claim that an operation was executed unless an Executor/operator receipt proves it.

### Executor

One Codex context or one explicitly assigned developer:

- reads the task packet and relevant context;
- implements only the authorized scope;
- adds tests and documentation;
- runs checks and records evidence;
- opens a PR and hands it back for review.

Do not assume a second worker, create unrequested subagents, or parallelize code writes. Parallel execution is allowed only when the Owner explicitly authorizes it and file ownership, dependencies, shared state, and integration gates are defined.

### Automated QA

CI, test harnesses, and browser/provider fixtures provide reproducible evidence. A model's assertion that code “looks correct” is not test evidence.

## 3. Context loading: minimum sufficient set

Every new session should load:

1. AGENTS.md;
2. PROJECT-CONTEXT.md;
3. PROJECT-STATE.md;
4. NEXT-ACTIONS.md;
5. ARCHITECTURE-INVARIANTS.md;
6. the current Issue/PR;
7. only the relevant module docs, ADRs, source files, and tests.

Do not repeatedly paste the entire repository into the prompt. Prefer a compact module context containing responsibility, public contracts, dependencies, persistence, failure modes, tests, and known limits.

The repository is the durable memory. Update it when a decision or verified state changes.

## 4. Bounded task packet

Before implementation, the Architect must provide:

~~~markdown
## Task
Issue / title:

## Baseline
Repository:
Base branch:
Exact base SHA:
Relevant accepted PRs / ADRs:

## Goal
One measurable outcome:

## Responsibility
Collector / Kernel / Store / Query / Transport / Operations / Out of Scope:

## Allowed scope
Files, packages, APIs, and behavior that may change:

## Forbidden scope
Invariants, contracts, schemas, flags, providers, and runtime behavior that must not change:

## Dependencies
Required prior decisions, branches, fixtures, or approvals:

## Acceptance criteria
Observable predicates and exact tests:

## Risk and rollout
Provider traffic, data mutation, migration, deployment, rollback:

## Handoff
Required evidence and report format:
~~~

If a required field is unknown, record it as UNKNOWN and resolve it before touching the affected boundary. Do not silently invent a baseline, test result, worker count, provider quota, or production state.

## 5. Task lifecycle

Use the states in AGENTS.md:

~~~text
TODO -> READY -> IN_PROGRESS -> REVIEW -> DONE
                    |             |
                    v             v
                 BLOCKED       FIX_REQUIRED
                                  |
                                  v
                                REVIEW
~~~

- READY requires scope, dependencies, and acceptance criteria.
- IN_PROGRESS has one named Executor.
- REVIEW means implementation and evidence are submitted; it does not mean accepted.
- FIX_REQUIRED must identify a concrete failing predicate or review finding.
- BLOCKED records the missing evidence, dependency, or authorization.
- DONE requires independent acceptance, merge, and required post-merge verification.

## 6. Execution rules

1. Re-check remote main immediately before branching.
2. Read the full relevant Issue and linked accepted decisions, not only its title.
3. Inspect current source and tests before proposing edits.
4. Write a short plan listing files, invariants, tests, and risks.
5. Stop and request Architect review before changing schema, Q1–Q9, identity/removal semantics, single-writer ownership, provider trust, or default runtime policy.
6. Implement the smallest coherent patch.
7. Do not change unrelated files merely to improve style.
8. Never delete or rewrite failing evidence.
9. Run the exact required test matrix and preserve output/exit codes.
10. Open one PR; do not self-approve, self-merge, or claim acceptance.

## 7. Model selection and tool use

Model choice is an execution detail, not an architecture boundary. Use a model/tool that can reliably inspect the repository, edit files, run shell/tests, and return inspectable evidence.

- A stronger reasoning model may plan or review complex changes.
- A coding agent may implement a bounded task.
- A separate model may perform an independent review.
- The number of models does not replace tests, source inspection, or human/Architect acceptance.

Do not hard-code vendor-specific model names into repository policy. Record model/tool identity only when it materially affects reproducibility or a test result.

## 8. Independent verification

The reviewer checks:

- exact base and head SHA;
- diff scope and changed files;
- responsibility and dependency direction;
- preserved identity, completeness, deletion, FIFO, generation, and Journal semantics;
- test completeness and exact-head CI;
- security, provider traffic, data migration, and rollback implications;
- claims against the actual evidence.

Review disposition:

- PASS — exact head meets the task and architecture acceptance criteria.
- FIX — bounded corrections are required.
- ARCHITECTURAL_BLOCKER — the task crosses a frozen boundary or lacks authorization/design.

CI green is necessary but does not equal Architect acceptance or production acceptance.

## 9. Required Executor handoff

~~~markdown
## Handoff
Task / Issue:
State: REVIEW
Base SHA:
Head SHA:
PR:

### Changes
- Files:
- Behavior changed:
- Explicitly unchanged:

### Verification
- Command:
- Environment:
- Exit status:
- CI run and exact head:
- Race / PostgreSQL / scale evidence:

### Runtime and safety
- Provider requests:
- Database / schema effects:
- Production actions:
- Backup / rollback:

### Limitations
- Not run:
- Skipped:
- UNKNOWN / NOT_TRACKED:
- Remaining risks:

### Suggested next action
One bounded next step:
~~~

Never convert an absent metric into zero. Never call an isolated fake-provider test a real-provider acceptance test. Never call a shortened soak the full acceptance soak.

## 10. Project memory maintenance

Maintain one authoritative location per information type:

| Information | Authoritative location |
| --- | --- |
| Durable agent rules | AGENTS.md |
| Product purpose and architecture boundary | PROJECT-CONTEXT.md |
| Latest verified repository/operational state | PROJECT-STATE.md |
| Next authorized work | NEXT-ACTIONS.md |
| Frozen architecture constraints | ARCHITECTURE-INVARIANTS.md and accepted ADRs |
| Module behavior and APIs | module docs and contract docs |
| Task execution and review | GitHub Issue / PR |
| Verified execution handoff | Issue/PR receipt and linked evidence |

After an accepted change, update only the state and next-action documents whose truth changed. Keep historical reports immutable; append a dated correction or new receipt rather than rewriting old evidence.