# data-platform

A personal data-engineering learning project: a Go + dbt data platform, phase 1 on one Mac with
DuckDB, that grows into an EKS deployment in phase 2. It ingests the [beads](#phase-1-source-facts)
issue tracker (a Dolt server) into Parquet, builds dbt marts over that Parquet, runs one Go
transform between two dbt passes, and orchestrates all of it on Temporal.

The architecture and its decision log live in
[`docs/data-platform-spec.md`](docs/data-platform-spec.md) — read that first. This file is the
runbook: how to install the toolchain, configure it, and run the whole pipeline end to end.

## Layout

```
cmd/          Go module: the three binaries below.
internal/     Go module: the packages the binaries are built from.
dbt/          dbt-duckdb project: sources.yml over bronze paths, staging views, marts as Parquet.
bronze/       raw and derived Parquet, written by Go, read by dbt and by Go transforms (gitignored).
warehouse/    dbt's DuckDB file (views only) and the mart Parquet dbt writes (gitignored).
local/        spool files, the dev Temporal DB, and other disposable local state (gitignored).
scripts/      live-materialise.sh (§ Runbook) and marts-views.sql (§ Visualisation).
docs/         spec, decision log, reviews, diagrams; docs/fable-streams/ holds conductor streams.
```

Every Go package under `cmd/` and `internal/` has a `doc.go` whose package comment names the
data-engineering concept it embodies. This table is pulled from those files (`grep '^// Data-engineering concept: ' <pkg>/doc.go`):

| Package | Data-engineering concept |
|---|---|
| `cmd/ingest` | Backfill as a first-class command. |
| `cmd/transform` | Running one Go transform between the two dbt passes. |
| `cmd/worker` | The long-lived worker process. |
| `internal/bronze` | The idempotent, replayable raw layer (overwrite-on-rerun files). |
| `internal/dbt` | An orchestration boundary around a SQL transformation tool, with run artefacts as data. |
| `internal/orchestrator` | Durable, replayable orchestration (activities, workflows, schedules, claim-check). |
| `internal/record` | The schema-carrying batch that forms the extract/load contract. |
| `internal/runner` | Replayable backfill and lookback re-extraction. |
| `internal/source` | Declared extraction contracts (grain, cadence, lookback). |
| `internal/source/beads` | Extraction from an event log plus time-travel snapshots, without transformation. |
| `internal/telemetry` | Pipeline observability (traces and metrics for data runs). |
| `internal/transform` | The transform contract (inputs, one output, window, tests) shared by dbt and Go. |
| `internal/transform/catalog` | An explicit transform registry for orchestration. |
| `internal/transform/mentions` | Free-text parsing where SQL is the wrong tool. |
| `internal/window` | Event-time windowing and replayable, window-derived partitions. |

## Prerequisites

- **Go 1.25.x** (`go.mod` pins `go 1.25.4`; this repo has been run under Go 1.25.5). No install
  command needed if you already have it — verify with `go version`.
- **dbt-core + dbt-duckdb**, **DuckDB CLI** and **Temporal CLI**, all free and requiring no
  account (spec principle 5). Exact pinned versions are in the spec's
  [decision log](docs/data-platform-spec.md#decision-log) (F9). Install with:

  ```
  brew install duckdb temporal
  uv tool install --python 3.12 dbt-core --with dbt-duckdb
  ```

  (`uv` itself: `brew install uv`, or see https://docs.astral.sh/uv/.)
- A **beads Dolt server** reachable at `127.0.0.1:49209` (database `trk`, MySQL wire protocol),
  already running and populated — this repo only reads it (spec §1, [F1–F3](#phase-1-source-facts)
  below). Not installed by this repo.
- Optionally, an OTLP collector at `127.0.0.1:4317` (the workspace's Grafana Alloy pipeline, F8) —
  only needed if you want spans and metrics to leave the machine; `PLATFORM_OTEL_EXPORTER=stdout`
  or `=none` work without one.

## Configuration

All configuration is environment variables; nothing is read from a config file, and no credential
is committed.

| Variable | Default | Read by |
|---|---|---|
| `BEADS_HOST` | `127.0.0.1` | the beads Fetcher |
| `BEADS_PORT` | `49209` | the beads Fetcher |
| `BEADS_DATABASE` | `trk` | the beads Fetcher |
| `BEADS_USER` | `root` | the beads Fetcher |
| `BEADS_PASSWORD` | (empty) | the beads Fetcher |
| `BEADS_EVENTS_TZ` | `Australia/Sydney` | the beads Fetcher (F1: `events.created_at` is naive Sydney wall time) |
| `TEMPORAL_ADDRESS` | `127.0.0.1:7233` | `cmd/worker`, the Temporal CLI — use the IP, not `localhost` (spec F10) |
| `TEMPORAL_NAMESPACE` | `default` | `cmd/worker` |
| `PLATFORM_OTEL_EXPORTER` | `otlp` | `internal/telemetry` — `otlp`, `stdout` or `none` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset (falls back to `127.0.0.1:4317` insecure) | `internal/telemetry`, standard OTel var |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | unset | `internal/telemetry`, standard OTel var |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | unset | `internal/telemetry`, standard OTel var |
| `PLATFORM_DBT_BIN` | `dbt` (on `PATH`) | `cmd/worker`'s `DbtBuild` activity |

`BEADS_LIVE=1` (read by `go test`, not by the binaries) turns on the live Fetcher tests, which
need the Dolt server; `make test-live` sets it.

## Runbook

Every command below is a `make` target (`make help` lists them all); each one was executed to
produce the evidence in this stream's `10-integration` task report. `Makefile` exports
`TEMPORAL_ADDRESS=127.0.0.1:7233` for every target for the same reason as the table above.

1. **Dev server** — `make temporal-dev` (foreground): `temporal server start-dev --db-filename
   local/temporal.db`.
2. **Worker** — `make worker` (foreground, in another terminal): `go run ./cmd/worker -root .`.
   Polls task queue `data-platform` and installs telemetry (`internal/telemetry`) before it
   connects.
3. **Schedule** — `make schedule`: `go run ./cmd/worker -apply-schedule` upserts the Temporal
   Schedule `materialise-beads` (interval = beads' declared 15 m cadence, overlap policy Skip).
4. **Trigger** — `make trigger`: `temporal schedule trigger --schedule-id materialise-beads`
   starts one `MaterialiseSource` execution, which ingests beads' lookback windows and then, once
   ingestion succeeds, starts a `BuildMarts` child (the two-pass dbt build below, plus the Go
   transform) for the day.

   **`make live` performs steps 1–4 end to end**, in one script
   (`scripts/live-materialise.sh`): it starts or reuses a dev server, builds and starts the worker
   with `PLATFORM_OTEL_EXPORTER=stdout`, applies the schedule, waits for the schedule to be quiet,
   triggers it, waits for the `MaterialiseSource` execution and its `BuildMarts` child to
   complete, checks that bronze and mart Parquet moved forward, stops the worker so its telemetry
   flushes, and checks the worker's stdout for the expected spans. It prints `LIVE-MATERIALISE:
   PASS` (or `FAIL <reason>`) as its last line and cleans up everything it started.
5. **Backfill** — `make backfill` (`FROM` defaults to `2026-08-21`, `TO` to today UTC; override
   with `make backfill FROM=... TO=...`): `go run ./cmd/ingest --source beads --from $FROM --to
   $TO` walks every window in the range and overwrites each bronze file — the full recorded
   history.
6. **Ingest once** — `make ingest-once`: `go run ./cmd/ingest --source beads --once` runs a
   lookback pass (each table's trailing windows from now), the same call the Schedule makes.
7. **Build marts** — `make build-marts`, or its three steps individually:
   - `make dbt-pre` — `mkdir -p warehouse/marts`, then `dbt build --project-dir dbt
     --profiles-dir dbt --select tag:pre_go` (staging views, the five `pre_go` marts).
   - `make transform` (`WINDOW` defaults to today UTC) — `go run ./cmd/transform issue_mentions
     --window $WINDOW` reads `warehouse/marts/dim_issues/` and writes
     `bronze/derived/issue_mentions/dt=$WINDOW/part-0.parquet`.
   - `make dbt-post` — `dbt build --project-dir dbt --profiles-dir dbt --select tag:post_go`
     (`mart_issue_mention_drift`, the mart the Go transform's output feeds).
8. **Freshness** — `make freshness`: `dbt source freshness --project-dir dbt --profiles-dir dbt`
   checks every raw and derived source's `_extracted_at` against its warn/error thresholds.
9. **UI** — `make ui`: `duckdb -ui -init scripts/marts-views.sql` opens the DuckDB UI (a browser
   tab) in its own, in-memory DuckDB session with one view per mart, each a `read_parquet(...)`
   over `warehouse/marts/<model>/`. Interactive only — not run in verification. The same view file
   also works non-interactively, e.g. `duckdb -c ".read scripts/marts-views.sql" -c "select
   count(*) from dim_issues"`.

## Idempotency and replay

The idempotency requirement (spec, "Requirement: idempotent re-runs") is demonstrated by two
drills task 04 ran against `cmd/ingest`, and either can be re-run against this repo at any time:

- **Idempotent re-run.** Record the bronze file list under `bronze/raw/beads/`, run the same
  backfill again, and diff the file lists — no additions, no removals. Then, per table, compare
  rows with DuckDB's `EXCEPT ALL` in both directions, excluding `_extracted_at` (the one column a
  re-run is allowed to change): both directions return 0 rows, so the data content, not just the
  file set, is unchanged.

  ```
  find bronze/raw/beads -name '*.parquet' | sort > /tmp/before.txt
  make backfill FROM=2026-09-14 TO=2026-09-15
  find bronze/raw/beads -name '*.parquet' | sort > /tmp/after.txt
  diff /tmp/before.txt /tmp/after.txt && echo SAME-FILE-SET
  ```
- **Replay after loss.** Delete one day's directory under a table's bronze path, back-fill just
  that day again, and show the restored content is byte-for-byte the same as before deletion
  (excluding `_extracted_at`) via a DuckDB hash comparison:

  ```
  rm -rf bronze/raw/beads/events/dt=2026-09-15
  make backfill FROM=2026-09-15 TO=2026-09-15
  ls bronze/raw/beads/events/dt=2026-09-15 | wc -l   # 24 files restored
  ```

Both rely on the same fact: a bronze path is a pure function of the window it covers (never of
wall-clock time), so a window is always safely re-fetchable and a lost file is only ever one
backfill away from being exactly what it was.

## Phase-1 source facts

Facts established live against the beads Dolt server (spec §2, Phase R) that shape the Fetcher and
the staging models:

- **F1 — mixed timestamp semantics.** `events.created_at` is naive **Australia/Sydney wall time**
  (the Dolt server's `CURRENT_TIMESTAMP`, `@@time_zone = SYSTEM = AEST`). Every other timestamp
  (`issues.*_at`, `comments.created_at`, `dependencies.created_at`, `dolt_log.date`,
  `dolt_history_issues.commit_date`) is naive **UTC**. Bronze keeps `events.created_at` exactly as
  the source served it (ingestion never transforms); the UTC conversion happens only in dbt
  staging (`stg_beads__events`), using `BEADS_EVENTS_TZ`.
- **F2 — Dolt time travel.** `SELECT ... FROM <table> AS OF TIMESTAMP('YYYY-MM-DD HH:MM:SS')`
  returns the table's state at that UTC instant; a future instant returns the latest state, and an
  instant before the table's first commit fails with `table not found`. The four snapshot tables
  (`issues`, `comments`, `dependencies`, `labels`) are extracted this way, once per day window.
  Every fetch runs inside `START TRANSACTION READ ONLY`, which Dolt enforces (a write inside it
  fails with error 1792), and is always rolled back — the Dolt server is read-only to this
  project.
- **F3 — payload size.** The largest observed hourly `dolt_history_issues` window is 4,745 rows,
  about 6 MB of text — above Temporal's default 2 MB payload limit. Row data therefore never
  travels through a workflow or activity payload: `FetchWindow` writes rows to a spool file under
  `local/spool/` and hands the workflow only a reference (path, row count, fetch instant);
  `WriteWindow` reads the spool and writes bronze.

## Troubleshooting

- **DuckDB single-writer rule.** A DuckDB database file (`warehouse/dev.duckdb`) allows **one
  read-write process, or any number of read-only processes, but never both at once**. `dbt build`
  needs the read-write slot; anything else that opens `dev.duckdb` while a build is running will
  either be blocked or block the build. This is why marts are materialised as Parquet
  (`external`), `dev.duckdb` holds only views, and every reader — the Go transform, the DuckDB UI,
  `scripts/marts-views.sql` — opens its own **in-memory** DuckDB session over the mart Parquet
  instead of `dev.duckdb`.
- **`warehouse/marts` must exist before a dbt build.** dbt-duckdb's `external` materialisation
  writes into `warehouse/marts/<model>/` but does not create the parent directory itself (spec
  F5). `make dbt-pre` / `make dbt-post` run `mkdir -p warehouse/marts` first; if you invoke `dbt
  build` directly (not through `make`), create that directory yourself first, or the build fails.
- **The Dolt server is read-only to this project.** Every beads query runs in a read-only
  transaction (F2); nothing in this repo writes to Dolt. If Dolt is unreachable, `make
  backfill` / `make ingest-once` / `make live` fail immediately (see `check_dolt` in
  `scripts/live-materialise.sh`) — check `BEADS_HOST` / `BEADS_PORT` and that the Dolt server
  process is actually running at `127.0.0.1:49209`.
- **`localhost` vs `127.0.0.1`.** In this workspace's shell, `localhost` can resolve to `::1`
  first; gRPC to a Temporal dev server listening on `127.0.0.1` then hangs until it times out
  (spec F10). Always address Temporal (and the OTLP collector) by `127.0.0.1`, never `localhost`.
  `Makefile` and `scripts/live-materialise.sh` both do this by default.
