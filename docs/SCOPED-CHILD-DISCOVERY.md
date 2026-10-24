# Scoped child-directory discovery

## Responsibility and dependencies

The collector acquires one directory's direct children. `scan.Service` admits the PARTIAL observation through the existing kernel and returns observed directory scopes in root-relative scope-key coordinates (not provider paths). The incremental executor decides whether those scopes belong to an explicitly configured discovery subtree. The store atomically persists child work and verifies the parent claim, using Root -> Watch -> Work locking and the existing claimed-signal CAS. Canonical inventory and Journal remain kernel-owned.

`INDEXCORE_INCREMENTAL_DISCOVERY_SCOPE` is empty by default. To expand a download subtree, configure `/cloud-downloads` (or the actual root-relative directory). Runtime scheduling must also be enabled. This option applies in every active root using that scope coordinate; do not enable a global `/` prefix casually. No public endpoint, schema migration, new queue or recurring watch is added.

## Bounds and replay

Each ExecuteOne still makes exactly one forced directory-refresh call. At most the existing 1000-entry scope limit is observed/queued per completion. Descendant expansion stops eight directory levels below the configured prefix. These are per-scope/depth bounds, not a global total-work cap or a universal 30-second SLA. Large directory trees may require more cycles. Failed/oversized observations retain their existing failure/retry behavior. Paths outside the prefix, siblings sharing only a textual prefix, malformed scopes and non-direct children cannot be expanded.

Children and parent completion commit in one transaction. A completion failure cannot leave a VERIFIED parent without durable follow-up work. Repeated parent observations may merge another signal into existing child work; this preserves pending signals/backoff and supports repairing previously missed folders. This is bounded scope discovery, not a provider-native delta feed. Once all work is VERIFIED it goes idle; changes after verification still require a hint, configured watch or explicit operator refresh.

## Safety and extension rules

Directory results do not imply descendant files have been indexed. Consumers must keep file and directory display semantics distinct. Discovery does not alter acquisition result_copy_id, existing READY tasks, object identity, removal grace or PARTIAL missing-is-not-deleted rules. Do not introduce a consumer-side provider traversal to mask missing canonical coverage. Collector support must keep reporting validated root-relative direct-child scopes and reuse the same durable completion boundary.

## Tests

`TestDiscoveryNestedFoldersExistingReplayAndNamespace` covers nested file discovery with a non-root provider prefix, one refresh per invocation, replay of existing folders, omission safety, and subtree exclusion. `TestDiscoveryCompletionAtomicRollbackAndStaleClaim` injects a PostgreSQL trigger failure after child insertion and checks total rollback, stale claims, wrong-parent rejection and durable success. `TestDiscoveryScopeBoundaries` covers opt-out, alias-prefix attacks, duplicates, depth and count limits.

## Issue134 F1 accepted: bounded first-scan service order

The existing explicit non-root discovery subtree keeps its HIGH priority. A
separate, default-off option `INDEXCORE_INCREMENTAL_FIRST_DISCOVERY_ENABLED=true`
(or `--incremental-first-discovery`) provides finite first-discovery and aged
background slots within the existing serial runtime cycle. It does not change
child publication, deletion rules, the normal source refresh or one-shot CLI
selection outside a cycle. Disabling the option restores legacy priority/FIFO
without resetting any durable state.

See [ADR007](decisions/ADR-007-BOUNDED-FIRST-DISCOVERY.md) for proof,
quotas, modular extension constraints and rollback; see the [D0 observation](p0/P0-134-D0-FIRST-DISCOVERY.md)
and [isolated F1 evidence](p0/P0-134-F1-ISOLATED-RESULTS.md) for measured results
and limitations. This is not production acceptance or duplicate-requeue
suppression. F2/F3 require separate review under Issue134.

## Issue134 F2 accepted default-OFF integration: coalesced parent intents

`INDEXCORE_INCREMENTAL_MIRROR_REVISIT=2m` (or
`--incremental-mirror-revisit=2m`) opts into the integrated F2 policy. Default0s
preserves legacy parent-child merges. Valid positive intervals are whole seconds
30s..1h. Only positively known ordinary mirror children are eligible; the explicit
HIGH subtree remains unchanged. Canonical directory mtime never proves contents
unchanged. A due revisit or formal Hint still verifies the child through the
normal scoped scan/Kernel boundary.

F2 atomically enrolls existing WARM/BACKSTOP_ENROLL watches and only coalesces
duplicate parent intent while their bounded schedule is intact. Repeated parent
observations cannot postpone next_due or rewrite a verified/pending child epoch.
The F2 orchestrator reserves one due-watch slot for existing policy and one for
managed rolling checks, retaining the existing per-cycle limits and CAS. No
second queue or writer is added. Disabling F2 does not erase enrolled watches;
retire exact owned watch policies through the accepted CAS API when required.

See [ADR008](decisions/ADR-008-MIRROR-ROLLING-REVISIT.md) and
[isolated F2 evidence](p0/P0-134-F2-ISOLATED-RESULTS.md). Production
enablement and large20k/50k F3 capacity acceptance remain separate gates.

## Issue134 F3 design and capacity investigation

[ADR009 proposal](decisions/ADR-009-CAPACITY-BUDGET-INVESTIGATION.md) and
[bounded capacity evidence](p0/P0-134-F3-CAPACITY-INVESTIGATION.md) distinguish
preloaded2k/20k/50k directory populations from actual31-scan samples. Due Watch
age/debt remains high despite small PENDING counts. No runtime budget controller,
automatic Watch retirement, prefetch or production rollout is implemented here.
