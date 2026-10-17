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
