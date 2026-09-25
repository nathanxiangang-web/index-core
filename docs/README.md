# IndexCore Documentation

This directory contains two kinds of documentation:

1. **Current user/operator documentation** — start here if you want to run or integrate IndexCore.
2. **Architecture/history evidence** — Gate 1–4 contracts, ADRs, verification reports, and research material.

## Start here

| Goal | Document |
| --- | --- |
| Understand the project quickly | [README](../README.md) |
| Understand the current architecture / interface planes | [ARCHITECTURE.md](ARCHITECTURE.md) |
| Run IndexCore locally | [QUICKSTART.md](QUICKSTART.md) |
| Learn the CLI | [CLI.md](CLI.md) |
| Call the read-only HTTP API | [HTTP-API.md](HTTP-API.md) |
| Configure rclone / AList / OpenList | [COLLECTORS.md](COLLECTORS.md) |
| Integrate a new Web/application | [INTEGRATION.md](INTEGRATION.md) |
| Operate / deploy / recover | [OPERATIONS.md](OPERATIONS.md) |
| Review incremental architecture/history | [architecture/POST-MVP-INCREMENTAL-BLUEPRINT.md](architecture/POST-MVP-INCREMENTAL-BLUEPRINT.md) |
| Review current P12 deployment-soak plan | [architecture/INCREMENTAL-P12-DEPLOYMENT-SOAK.md](architecture/INCREMENTAL-P12-DEPLOYMENT-SOAK.md) |
| Read accepted incremental capability research | [research/INCREMENTAL-CHANGE-DISCOVERY-REPORT.md](research/INCREMENTAL-CHANGE-DISCOVERY-REPORT.md) |

A complete **test/reference consumer** is available in the separate repository:

`nathanxiangang-web/indexcore-reference-web`

It is a validation Web, not a production frontend. It was used for Gate 4 end-to-end validation and consumes IndexCore only through server-side HTTP `/v1`.

## Project status

IndexCore core development is complete for the accepted Alpha scope.

```text
Gate 1  Architecture / semantics              CLOSED
Gate 2  PostgreSQL + Kernel PoC               CLOSED
Gate 3  Standalone runtime + scale            CLOSED
Gate 4  Independent consumer validation       CLOSED

Current mode: Stable Alpha Foundation / Incremental Hardening
IndexCore extension: D0 + P0–P11 ARCHITECT_ACCEPTED
Active validation phase: P12 Deployment Soak with Reference Test Web
Incremental runtime: accepted opt-in same-process/same-writer runtime, default disabled
Reference test consumer: nathanxiangang-web/indexcore-reference-web
Next product blueprint phase: Gate 5 (NOT AUTHORIZED)
```

The accepted baseline includes PostgreSQL 18, safe reconcile, canonical inventory,
change journal, rclone and AList/OpenList collectors, a standalone runtime,
read-only HTTP Query API, trusted loopback Hint ingestion, the accepted opt-in
hybrid incremental runtime, P11 readiness/recovery/CI hardening, and a verified
external consumer.

## Architecture contracts

The frozen architecture contracts live under [architecture/](architecture/).

Important entry points:

- `GATE1B-DOMAIN-MODEL.md`
- `GATE1B-SNAPSHOT-COMPLETENESS.md`
- `GATE1B-SAFE-RECONCILE.md`
- `GATE1C-POSTGRESQL-STORE.md`
- `GATE1C-TRANSACTION-BOUNDARY.md`
- `GATE1C-QUERY-CONTRACT.md`
- `GATE1C-JOURNAL-PERSISTENCE.md`
- `GATE1C-COLLECTOR-ADAPTER-CONTRACT.md`

Architecture decisions live under [decisions/](decisions/).

## Verification / historical evidence

- [gate2/](gate2/) — PostgreSQL / Kernel PoC
- [gate3/](gate3/) — standalone runtime, packaging, real sources, scale
- [gate4/](gate4/) — independent Reference Consumer validation
- [research/](research/) — donor / external-system research

These files are valuable project evidence, but operators and integrators should normally start with the current docs listed above.
