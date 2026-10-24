# Issue138 validation prerequisite: retry-maintenance parent shutdown

Status: narrow implementation candidate, independent review required.
Baseline main562a852 / merge CI40081586002 SUCCESS, no production changes.

## Evidence and causal boundary

The same-root candidate's broad Race run failed an inherited test:
TestIssue130RealScanWorkerHandoff/synchronous_scan_wins returned
`incrementalruntime: fatal phase=retry_maintenance error_class=store` AFTER stop().
This is not evidence of a new source-read lineage or canonical-write failure.

Unchanged exact-main owned-PG18 Race count40 reproduced1 failed iteration.
A separate exported copy added only parent-context and errors.Is assertions to
the failure message. Its count40 reproduced3 failures; all3 showed
parent_canceled=true AND cause_canceled=true. Original logs/exports are retained.
The instrumented copy is diagnostic only, not part of this runtime diff.

runOnce already distinguishes shutdown in the cycle path, but retry maintenance
wrapped every Store error as fatal before that check. A cancel interrupting its
DB request was therefore misclassified. This proves this local shutdown defect,
NOT the cause of every historical production fatal or Issue129 completion.

## Module contract and narrow change

Only retry-maintenance error classification changes: a nonnil parent ctx.Err()
AND errors.Is(Store error, that EXACT parent error) authorize clean shutdown.
An unrelated Store error during shutdown, a cancellation with a live parent,
and a different child timeout remain fatal with their original error chain.
No retry, queue reset, Canonical/Journal write, source call or new dependency.
No change to startup recovery or other fatal phases is implied by this patch.

## Validation

Go1.27.1 / owned PostgreSQL18 / CPU1 / memory1GiB / pids128 /512MiB tmpfs,
loopback-only, restart=no, GOMAXPROCS2/GOMEMLIMIT512MiB soft target.
Purpose-marked indexcore_issue130_test is separate from ordinary test DB.

- Channel-controlled four-case classification test, Race count100: PASS.
  No sleep chooses the interleaving; actors are joined on assertion failure.
- Unmodified real PG handoff test, Race count60: PASS,53.441s,
  all180 worker-before/after/synchronous subcases passed.
- Full suite, broad Race and exact-head GitHub CI are tracked in the PR receipt;
  the focused passes above are not a claim that those later checks have passed.

Reproducibility: ssh dsh, /home/nathan/index138-same-root-20261024/logs/:
baseline-handoff40.log, baseline-handoff-cause40.log,
fix-classification100.log, fix-real-handoff60.log. Failed evidence is retained.
An unrelated same-root performance pilot was aborted during the64-read case to
isolate this fault; it is not counted as a completed performance matrix.

## Integration and rollback

This PR is a small independent prerequisite, not the same-root PR-A, runtime
Worker Pool/PR-B, production enablement or Issue134/138 closure. Review and merge
separately. No migration; reverting the one classification branch restores the
previous behavior. No production binary/configuration is replaced here.
