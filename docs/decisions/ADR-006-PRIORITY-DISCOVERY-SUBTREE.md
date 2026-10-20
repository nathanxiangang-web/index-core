# ADR-006: explicit discovery subtree fast lane

Status: user-authorized latency repair, 2026-10-20. Isolated PG18 focused tests
(10 methods/subcases) and full vet/build/test/targeted race pass (633 passing
test events,152.459s). Exact-head CI and live deployment/acceptance pending.

## Evidence

A completed acquisition anchored a directory after 45 seconds, but its files
were only indexed after 8m19s. The directory's child work waited 462.899s before
its first scan, behind about 400 ordinary mirror discovery scopes. The scan
itself took about one second. Another directory had a 501.041s directory-to-file
Journal gap. Browser GET refresh cannot expose files absent from Canonical.

## Module and interface decision

Keep the executor, one serial writer, bounded discovery, work selector and
transactional Store. Extend the atomic discovery completion seam with the
existing configured DiscoveryScope. Direct children equal to or below that
explicit non-root subtree get HIGH operational priority; all other mirror
children stay NORMAL. Empty and root-wide configuration do not promote the
entire library. Exact slash boundaries prevent sibling-prefix leakage.

This applies to rediscovery too: a file may arrive after its containing directory
was first verified empty. Child signals remain RECOVERY/POSSIBLE_CHANGE, not
fabricated external hints. Existing merge rules retain higher priority, retry
eligibility, blocked/suspended state and in-flight pending epochs. Parent
completion, child insert/priority merge and CAS fencing share one transaction.

Only the runtime's validated explicit configuration selects the fast lane; no
provider name, CloudSite path, user quota or downloader state enters IndexCore.
Collectors still perform one direct-child refresh per claim; the Kernel alone
decides Canonical and atomic Q8. No schema, new queue/worker, source recursion,
deletion rule, trust-plane or application observation bypass is introduced.

## Bounds, extension and rollback

Existing scope-entry, depth, cycle and burst limits remain. This is latency
isolation, not an unconditional 120-second completion SLA: provider download,
throttling, failures and a large opted-in subtree can still delay completion.
Finite foreground work drains before the retained ordinary backlog resumes.
Do not opt the whole library into the foreground lane to mask overload.

Regression coverage requires a 400-item older backlog, subtree/sibling/default
boundaries, repeated existing child merges, stale claims, transaction rollback,
and a real PostgreSQL + mock OpenList executor-to-Canonical test. Run full CI.
Deploy only a checked GitHub binary with old binary/config/database backup and
single-writer stop/start. Rolling back the binary restores old scheduling; never
rewind Journal or clear durable work. Real download acceptance must separately
time provider completion, Canonical file creation and website visibility.
