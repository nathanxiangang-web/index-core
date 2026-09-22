# Index Core — Next Actions

> Operational near-term queue only.
> Do not use this file as historical archive.

## Current phase

Discovery 02 — AList / OpenList Collector capability investigation

## Immediate objective

Answer:

> Can AList/OpenList provide a sufficiently reliable external Collector boundary for Index Core, or is another provider abstraction investigation required?

## Execution order

### 1. Foreman baseline recovery

Foreman must:

1. sync `main`
2. read:
   - `PROJECT-CONTEXT.md`
   - `PROJECT-STATE.md`
   - `ARCHITECTURE-INVARIANTS.md`
   - this file
   - Issue #1
   - Issue #9
3. verify the current accepted main baseline
4. create only:
   `research/d02-alist-openlist-discovery`

### 2. Dispatch four narrow worker tasks

- #10 Worker A — indexing/search internals
- #11 Worker B — public FS API / resource metadata
- #12 Worker C — representative driver capability differences
- #13 Worker D — independent Snapshot matrix and counter-evidence

Workers do not operate Git/GitHub.

### 3. Required D02 outputs

```text
docs/research/d02/
  W-A-ALIST-OPENLIST-INDEXING.md
  W-B-ALIST-OPENLIST-PROVIDER-API.md
  W-C-ALIST-OPENLIST-DRIVER-CAPABILITIES.md
  W-D-ALIST-OPENLIST-SNAPSHOT-MATRIX.md

docs/research/
  ALIST-OPENLIST-COLLECTOR-DISCOVERY-REPORT.md
```

### 4. Foreman cross-check

Before PR:

- A internal fields vs B public API
- B API claims vs C real driver behavior
- D independently attacks optimistic assumptions
- path vs stable identity checked
- cache/full listing/native delta terminology checked
- unsupported/driver-dependent fields clearly represented

### 5. Submit one PR

Branch:

`research/d02-alist-openlist-discovery`

Base:

`main`

Only Foreman submits.

### 6. Architect gate

Architect reviews actual files/diff, not just Foreman summary.

Possible result:

- ACCEPT D02
- REWORK
- BLOCKED / evidence insufficient

## D02 exit questions

Before D02 can be accepted, the evidence must support answers to:

1. Can public API produce a complete root listing?
2. Can completeness be distinguished from failure/staleness?
3. Which SnapshotEntry fields are reliable?
4. Which are driver-dependent?
5. Is any stable provider object identity externally available?
6. Is rename/move identity observable?
7. Is native delta available?
8. Can multi-root identity be isolated?
9. What safety responsibilities remain for Index Core?
10. Is D03 needed?

## D03 trigger

Do **not** start rclone/fsspec research automatically.

D03 becomes eligible only if Architect concludes from D02 that AList/OpenList are insufficient and a broader provider abstraction comparison is necessary.

## Context maintenance at phase close

When Architect accepts D02:

Foreman prepares updates to:

- `PROJECT-STATE.md`
- `NEXT-ACTIONS.md`

Architect reviews those updates before merge.

Do not rewrite `PROJECT-CONTEXT.md` or `ARCHITECTURE-INVARIANTS.md` just because a phase completed.

Those change only when project meaning or accepted invariants actually change.

## Current prohibited actions

Until D02 is accepted:

- no product code
- no formal DB schema
- no CloudSite integration
- no rclone/fsspec investigation
- no UI
- no premature Provider adapter
- no PoC
- no final architecture decision
