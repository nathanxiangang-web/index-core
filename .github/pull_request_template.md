## Summary

<!-- What changed and why? Keep this PR to one bounded concern. -->

## Scope and responsibility

- Issue / task:
- Responsibility: Collector / Kernel / Store / Query / Transport / Operations / Docs
- Exact base SHA:
- Exact head SHA:

## Architecture and safety

- [ ] Existing identity, completeness, absence/deletion, generation, FIFO, and Journal invariants are preserved.
- [ ] No public Q1–Q9 contract change unless explicitly authorized.
- [ ] No second writer or bypass of the PostgreSQL advisory lock.
- [ ] No product/business concern was moved into the Kernel.
- [ ] Provider traffic, database mutation, and external side effects are identified below.

## Verification

- [ ] gofmt
- [ ] go vet ./...
- [ ] go build ./...
- [ ] Relevant PostgreSQL 18 tests
- [ ] Relevant race / crash / concurrency tests
- [ ] Exact-head GitHub CI completed successfully

Commands and results:

## Data, provider, and production effects

- Provider requests / status codes:
- Database or migration effects:
- Production actions:
- Secrets and sensitive data review:

## Rollout and rollback

- Deployment required: yes / no
- Migration required: yes / no
- Rollback artifact and state compatibility:
- Monitoring / acceptance window:

## Evidence and limitations

- CI run URL and exact SHA:
- Evidence files / checksums:
- Tests skipped or not run:
- UNKNOWN / NOT_TRACKED:
- Known risks:

## Architect review

- [ ] Independent review requested.
- [ ] Executor did not self-approve or self-merge.
- [ ] Project state / next actions updated if accepted truth changed.