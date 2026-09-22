# Index Core — Next Actions

> Operational near-term queue only.

## Current task

**Post-D02 Decision Review**

Do not start D03 yet.

## Immediate objective

Decide whether additional Collector research is worth the cost.

Use only D02 evidence and one question:

> Would rclone/fsspec plausibly change the architecture boundary, or only provide another way to access the same provider facts?

## Decision criteria

A narrow D03 is justified only if at least one of these is plausibly true:

1. rclone can expose a more general stable provider identity than AList/OpenList
2. rclone can expose partial traversal failures/completeness semantics materially better
3. rclone process/API boundary could remove the need for custom provider/scanner work
4. fsspec provides a materially different capability not already represented by AList/OpenList/rclone

If none is likely to change the architecture boundary:

**skip D03 and enter Gate 1 Architecture.**

## Current evidence from D02

AList/OpenList already provide:

- recursive resource enumeration
- name/size/is_dir/mtime/ctime
- driver-dependent hash
- AList-only driver-dependent provider id
- multi-storage access via admin APIs
- refresh semantics

They do not provide:

- cross-driver stable identity
- self-certifying provider-complete Snapshot semantics
- public native delta/change feed
- canonical inventory
- safe reconcile

## Current hold

- #17 is decision-only
- #18-#21 remain closed
- no D03 branch
- no worker dispatch
- no product implementation

## Next state

Exactly one of:

### START_D03
Create a narrow comparison task.

### SKIP_D03
Enter Gate 1 Architecture and record why additional donor research is unlikely to change the boundary.
