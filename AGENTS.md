# Index Core — Agent Governance

This repository is durable project memory. Chat history is only cache.

## Recovery order

Before substantial work, read:

1. PROJECT-CONTEXT.md
2. PROJECT-STATE.md
3. ARCHITECTURE-INVARIANTS.md
4. NEXT-ACTIONS.md
5. the current control Issue
6. the current Foreman / Worker task

Do not infer the active phase from old chat history.

## Authority hierarchy

Architect / ChatGPT
→ Windows Foreman
→ Worker A/B/C/D
→ temporary subagents

Only the Architect freezes architecture and accepts gates.

The Foreman owns dispatch, cross-worker QA, integration and Git/GitHub operations.

Workers own their assigned analysis/design task and must independently synthesize their final result.

Subagents are temporary specialists. They do not own architecture, task scope, project state or final conclusions.

## Subagent policy

A Worker SHOULD delegate when a task contains at least two independently verifiable questions, or when independent counter-evidence materially improves quality.

Recommended uses:

- evidence gathering
- repository/document lookup
- counterexample search
- state/scenario enumeration
- contract consistency checking
- test/failure-case design

Do NOT delegate:

- final architecture decision
- gate acceptance
- Git/GitHub operations
- branch/PR management
- cross-worker consolidation
- scope changes
- project-state transitions

### Subagent hard rules

1. No Git or GitHub operations.
2. No file modification.
3. No child-subagent recursion.
4. No final architecture verdict.
5. Answer only the assigned narrow question.
6. Separate FACT / INFERENCE / UNKNOWN.
7. Every important FACT must identify evidence (repo path, symbol, issue/report, or source URL as applicable).
8. Surface counterexamples and uncertainty instead of forcing a conclusion.
9. Do not expand scope because an adjacent topic looks interesting.
10. A majority of agents agreeing is not proof.

## Worker adoption rule

A Worker MUST NOT paste subagent output directly as its final answer.

For every material subagent finding the Worker adopts, the Worker must:

- verify the key evidence itself;
- resolve conflicts between subagents;
- state unresolved uncertainty;
- preserve accepted project invariants.

Each Worker final report must include a concise Subagent Ledger:

| Subagent | Narrow question | Key result | Worker verification | Decision |
|---|---|---|---|---|
| ... | ... | ... | VERIFIED / PARTIAL / FAILED | ADOPT / REJECT / HOLD |

## Quality principle

Workers and subagents are not trusted by role.

Evidence, invariants, reproducible checks and adversarial review are trusted.

Never use "multiple agents agreed" as the basis for correctness.
