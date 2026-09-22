# Index Core — Next Actions

> Operational near-term queue only.
> Do not use this file as historical archive.

## Current phase

Discovery 03 — Collector gap comparison

## Immediate objective

Answer only:

> Can rclone or fsspec materially improve the two unresolved D02 gaps — stable resource identity and snapshot completeness — or must IndexCore own those properties itself?

## Scope

In scope:

- rclone
- fsspec
- only the two hard gaps above
- representative backend evidence
- public/process boundary capabilities
- failure visibility

Out of scope:

- broad provider popularity comparisons
- performance bake-offs
- UI
- CloudSite integration
- database schema design
- implementation
- choosing final architecture before evidence review

## Execution order

### 1. Foreman baseline recovery

Foreman must:

1. sync `main`
2. read:
   - `PROJECT-CONTEXT.md`
   - `PROJECT-STATE.md`
   - `ARCHITECTURE-INVARIANTS.md`
   - this file
   - D02 consolidated report
   - Issue #1
   - Issue #17
3. verify current accepted main baseline
4. create only:
   `research/d03-collector-gap-comparison`

### 2. Dispatch four narrow worker tasks

- #18 Worker A — rclone stable identity / metadata
- #19 Worker B — rclone traversal completeness / RC / failure semantics
- #20 Worker C — fsspec identity / completeness
- #21 Worker D — independent gap matrix / counter-evidence

Workers do not operate Git/GitHub.

### 3. Required D03 outputs

```text
docs/research/d03/
  W-A-RCLONE-IDENTITY-METADATA.md
  W-B-RCLONE-COMPLETENESS-RC.md
  W-C-FSSPEC-GAP-CHECK.md
  W-D-COLLECTOR-GAP-MATRIX.md

docs/research/
  D03-COLLECTOR-GAP-COMPARISON.md
```

### 4. Foreman cross-check

Before PR:

- do not treat path as stable identity
- do not treat hash as object identity
- do not treat recursive success as completeness proof
- do not generalize one backend to all backends
- native delta vs polling/list diff must remain distinct
- every YES in capability matrix must have evidence

### 5. Submit one PR

Branch:

`research/d03-collector-gap-comparison`

Base:

`main`

Only Foreman submits.

### 6. Architect gate

Architect reviews actual files and evidence.

Possible outcome:

- ACCEPT D03 and close Gate 0 Discovery
- REWORK
- authorize one additional narrow discovery only if a material unknown remains

## D03 exit questions

1. Does rclone provide a general stable object identity?
2. Does fsspec provide a general stable object identity?
3. Can either prove a provider-complete traversal?
4. Can either reliably surface partial traversal failure?
5. Can either expose useful provider-specific identity metadata without embedding donor code?
6. Which properties still must be owned by IndexCore?
7. Is there any remaining reason to continue Discovery?

## Current prohibited actions

Until D03 is accepted:

- no product code
- no PostgreSQL schema/migrations
- no PoC
- no CloudSite integration
- no final provider adapter
- no final architecture
- no broad new donor search
