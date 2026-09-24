# P0 Scoped Refresh — Live Test Runbook

> Status: **EXECUTED — see `P0-SCOPED-REFRESH-LIVE-RESULT.md` (T0–T6 observed; decisive stale-cache gate passed)**
>
> Execution issue: #62
>
> Parent architecture: #57
>
> Production implementation: **NOT AUTHORIZED**

This runbook is the required non-production live validation for the P0 Targeted
Scoped Refresh prototype. **Until it is executed against a disposable/non-production
115/OpenList setup, P0 MUST NOT be reported as PASS.**

## 1. Why this is required

The prototype's deterministic and real-PostgreSQL tests prove:

- one forced `refresh=true` observation per scope;
- fail-closed behavior for overflow / total mismatch / permission / provider error;
- additive-safe PARTIAL reconcile (add/update only, no removal).

They do **not** prove that a real 115 change becomes visible earlier than the
stale OpenList cache. Only a live run can prove that.

## 2. Preconditions

- a **disposable / non-production** 115 account (never a production account);
- an OpenList instance configured with the **community-maintained 115 Open driver**
  (official 115 Open API path only — the legacy/private `115 Cloud` driver is **not**
  an acceptable acceptance path);
- a **dedicated service identity** whose credential is write-capable enough for
  `refresh=true`, and is **never** exposed to a browser or public API;
- the Worker may call only `/api/fs/list` (list/refresh). It must not call upload,
  delete, rename, move, copy, or any other provider mutation endpoint. An
  authentication `POST /api/auth/login` may precede the single canonical
  `refresh=true` observation when username/password is used; that is not a
  canonical observation and it never paginates (Round 3 wording fix).

Record up front:

- OpenList version / commit;
- 115 Open driver `page_size` and `limit_rate` (state explicitly whether they were changed);
- the target directory and its approximate direct-child count.

## 3. Procedure

```text
1. baseline: the target directory is ingested into IndexCore (its current children are canonical).
2. make the OpenList cache intentionally stale (do not force-refresh yet).
3. create/upload exactly one new file directly in real 115.
4. confirm a normal refresh=false list does NOT yet expose the new file.
5. run P0 scoped refresh on that parent directory:
      scan.Service.ScanScope(ctx, rootID, "<parent>", maxEntries)
   (via a gated integration test / probe helper — not a production CLI).
6. confirm the new file appears in the scoped observation.
7. confirm IndexCore reconcile applied it (Q3/Q4/Q6 can see the new resource).
8. record end-to-end latency from the P0 trigger to IndexCore visibility.
9. confirm no removal evidence was produced and unrelated resources are unchanged.
```

## 4. Evidence to record

- target directory direct-child count;
- OpenList refresh request latency;
- total response size (bytes) and HTTP status;
- whether `page_size` / `limit_rate` were changed from defaults;
- any 401 / 403 / 429 / throttle / provider error, and whether visibility required
  retries;
- OpenList version / commit;
- end-to-end trigger-to-visibility time;
- the chosen `max_entries` (note: it bounds only what IndexCore accepts and writes —
  **not** the real provider read cost, because OpenList loads the whole directory
  with `fs.List` before applying HTTP pagination).

## 5. Explicit non-goals

Do not, during this live test:

- stress-test a production account;
- call any provider mutation endpoint;
- enable a scheduler, polling, retry/backoff, or dirty-scope queue;
- expose the service credential to a browser or public API;
- treat the private `115 Cloud` driver as acceptance evidence.

## 6. Current Worker status

The Worker environment has a real AList/OpenList container but **no disposable 115
account / no non-production 115 Open driver configuration**. Therefore:

- deterministic Collector tests: **DONE**;
- real-PostgreSQL reconcile-safety tests: **DONE**;
- full regression: **DONE**;
- **live 115/OpenList validation: NOT EXECUTED → P0 NOT PASS.**

P0 acceptance remains gated on this runbook being executed and the evidence
recorded above.
## 7. Environment probe — 2026-09-24 (Worker)

The Architect supplied a candidate OpenList endpoint for live validation. The
Worker performed a **read-only** probe (login + `fs/list` only — no mutation):

- endpoint `https://pan.netioi.com` is an AList/OpenList instance;
- login succeeded with a **restricted, non-admin** service identity;
- the identity's visible root is `/` (base path restricted to the 115 mount);
- the root listing reports **`provider: "115 Cloud"`** — i.e. the legacy/private
  driver, **not** the official `115 Open` driver;
- root directory has 5 direct children; this identity has `write=true`.

**Result: this endpoint does NOT satisfy the P0 acceptance precondition.**
Issue #62 and section 2 above require the **official `115 Open` driver**; using
the legacy/private `115 Cloud` driver as the acceptance path is explicitly
forbidden.

Therefore live validation is **still NOT EXECUTED**, and **P0 remains NOT PASS**.
A live environment whose OpenList storage uses the community `115 Open` driver
(official 115 Open API) is required.

The probe performed no create / modify / delete; it only logged in and listed a
directory.