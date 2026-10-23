# Issue134 D0: measured ordinary-directory latency

2026-10-23 UTC. Read-only operational evidence, not a deployment or a promise
that the new optimization works in production. Paths/user filenames anonymized.

## Verified baseline and source

- GitHub main84096becb373f46161b00d91170b503e611bfaa1.
- Native runtime reports cloud-priority / commit84096be, built2026-10-22.
  Actual mounted executable SHA256:
  cff186cc0e06e24ab30a9e33a9bce72b1e43e0b6d22dc9b791b3594ab7364ecb.
  Container image revision894962f is NOT that executable's source revision.
- Actual runtime DB was selected from the protected binding (not the container
  default). Root ACTIVE, explicit discovery subtree enabled, wake5s, scan60s,
  one concurrent root. Source mirror watch interval30s.
- Read-only OpenList SQLite metadata proves the live mount uses **115 Open**,
  cache_expiration30, not a local filesystem. Its legacy nested Alias is disabled.
  The accepted scoped Collector calls refresh=true. Thus upstream requests and
  topology authentication have real cost; do not claim zero native provider load.
  No provider HTTP request was made by this D0 pass.

## Target timeline and limitations

| Persisted fact | UTC |
| --- | --- |
| Directory observation observed_at | 14:02:47.807250 |
| Directory generation268 produced_at / canonical created_at | 14:02:47.879117 |
| Child Work created_at | 14:02:51.318306 |
| Prior live read: still NORMAL/PENDING, never verified | about14:08 |
| File observation observed_at | 14:11:39.659007 |
| File generation269 produced_at / canonical created_at | 14:11:39.734956 |

Directory-to-file generation timestamp gap: **531.855839s**. Snapshot started_at
and finished_at are NULL. produced_at is a transaction timestamp, not a measured
commit time; observed_at-to-produced_at is not a full DB execution profile.

The initial pending_first_seen_at/claimed epoch has since been replaced by
later rediscoveries. Its exact first claim time cannot be reconstructed from
the current Work row alone. A separately authorized single trusted Hint rescued
the target before this Issue134 task. Therefore this is NOT an unassisted
completion benchmark and NOT a production before/after F1 speed measurement.
The prior live read did establish multi-minute NORMAL queue wait, not UI cache.

## Fresh 30-minute operational sample (about14:35Z)

- 1448 scoped observations /30min, **48.267 scopes/minute**.
- Scoped scan duration log P50/P95/P99: **1012/1978/2873ms**. These include the
  scoped scanner's work; separate source fetch vs ingest vs selector/lock costs
  are NOT present in existing telemetry and are not fabricated here.
- 425 eligible NORMAL PENDING, one IN_FLIGHT, 57 VERIFIED;
  five INVALID_SCOPE BLOCKED, zero RETRY_WAIT/INTERNAL cohorts in this sample.
- PENDING backlog-age P50/P95/P99: **171596.559/213214.461/217921.796ms**.
  425 older than30s,383 older than60s,352 older than120s,zero older than600s.
  These are current pending ages, NOT completed-job wait percentiles.
- One HOT SOURCE_MIRROR watch /30s, not due at the sampled instant.
- Bounded30min log sample: zero incremental_runtime_fatal. This is not a new D2
  stability acceptance. The already observed container restart count was zero.
- Follow-up bounded2h read at15:10:50Z parsed9108 JSON log rows, oldest13:10:29Z:
  zero runtime fatal/start/stop events. Container still running, restart count0,
  same2026-10-22T09:45:52Z start. This completes the requested recent2h check,
  not a claim that the separate Issue132 stability gate is accepted.
- Target Work signal_seq5 / last_verified_signal_seq4, again NORMAL PENDING
  after its14:33:42 verification. SourceRECOVERY rediscovery is real. Existing
  telemetry has no complete counter distinguishing repeat merges from new dirs;
  the exact amplification ratio remains UNMEASURED.

The dominant demonstrated target delay is backlog admission, not a600s timer or
multi-minute individual scan. Repeat-child merges plausibly replenish this
backlog; F2 must measure and handle them separately without dropping revisits.

## Implementation and isolation boundary

F1 is a separate opt-in bounded scheduling PR; no requeue suppression, source
parallelism, new queue, migration or Kernel/Journal/deletion rule changes.
See proposed ADR007. Production was only read through bounded READ ONLY SQL,
metadata and existing logs; no restart, Hint, retry, provider call or SQL UPDATE
was performed for D0.

Development host is ssh dsh /192.168.178.50, Go1.27.1. At admission it had only
about4.5GB disk free (90% used). Use an owned, resource-limited disposable PG18
and bounded fixtures, not shared databases. F2/F3,2k/20k/50k capacity validation
and production fresh-target before/after are **NOT_RUN** at this F1 boundary.
