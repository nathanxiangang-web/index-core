# ADR-005: renewable topology authority and bounded root recovery

Status: accepted for the user-authorized deletion-sync repair, 2026-10-19.
Implementation/deployment acceptance still requires tests and live evidence.

## Incident and boundaries

A physically deleted file remained PRESENT under an obsolete virtual Alias.
The mirror root had no topology credential and therefore correctly remained
additive-only. Its last successful root observation was 2026-10-18T13:29Z;
an INTERNAL/RETRY_WAIT root item was not eligible for generic P10 promotion,
despite continuing 30-second poll signals. The original trigger of that INTERNAL
error is not established from retained logs and must not be invented.

No UI overlay, canonical repair SQL, source-file mutation, acquisition submission,
new Job Engine, migration or weakening of the virtual-overlay guard is allowed.

## Decisions

1. Collector may explicitly receive a separate topology username/password via
   environment references, in addition to the existing topology token. Resolve a
   fresh short-lived login once per observation; use its token only for the
   existing bounded GET storage-topology checks before/after the source refresh.
   Do not reuse source credentials implicitly. No token/password enters logs,
   adapter config, Snapshot or Journal. Missing/failed credentials yield no
   absence authority, not an empty-success deletion. The same coverage v2 fences
   and root-lock/generation/adapter transaction remain authoritative.
2. Existing ListDueRetryWork may additionally select INTERNAL root work only for
   an ACTIVE, explicitly opted-in source mirror with a scheduled SOURCE_MIRROR
   OPERATOR_POLICY watch and a configured topology reference. Eligibility must
   already be due and consecutive failures must be 1 or 2. Use existing RetryReady
   CAS; never clear attempts, signals or failure history. A third consecutive
   failure stays paused for operator investigation. Other INTERNAL work, auth,
   invalid scope/config, disabled watches and ordinary P10 roots remain excluded.
   These are metadata scan retries, never Provider StartDownload retries.
3. Deployment may retire ONLY the owned legacy Alias after verifying its actual
   source is empty and there are zero Copy/Manifest/Binding dependencies. Preserve
   its exact configuration and a private OpenList DB backup; disable, do not
   delete. No other mount/account/session or physical directory is changed.
   If that audit fails, do not bypass topology protection. Once no active nested
   virtual mount exists, normal verified source coverage can converge deletions.

Collector owns topology authentication/observations; Kernel/Store owns coverage,
Canonical and atomic Journal; existing runtime owns bounded retries. CloudSite
continues consuming Q5/Q8 and the authenticated resource API, not OpenList scans.
Future providers must supply independently justified topology/completeness
contracts. New virtual mounts still disable absence authority automatically.

## Verification / rollback

Test renewable-session isolation, denied/partial credentials, PostgreSQL deletion
and sibling preservation, original virtual-only protections, due-time/CAS retry,
failure cap and generic allowlist. Run exact-head CI before binary replacement.
Back up DB/config/entrypoint/binary, snapshot unaffected identities, pin the
candidate hash and keep an owned-container rollback. Verify actual Q5/Q8, public
inventory removal, fresh successful poll cycles, positive sibling continuity and
no provider starts. Rollback must disable mirror authority before re-enabling the
legacy Alias; never rewind canonical history by restoring the database dump.
