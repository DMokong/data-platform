# Phase 1 build — acceptance spec

Stream `2026-09-18-phase-1-build` · epic `trk-bam` · branch `phase-1` · target repo `/Users/dustincheng/projects/data-platform`

Design of record: [`design.md`](design.md) → `docs/data-platform-spec.md` (decision log binding). This file adds
**numbered, independently testable acceptance criteria for phase 1 only**. Where it is more specific than the
architecture spec, the extra precision comes from the facts established at Phase R (§2) and never contradicts
the decision log.

## 1. Scope

**In scope.** Writer with deterministic window paths (TDD, byte-identical re-runs); beads Fetcher over Dolt
(MySQL wire, `127.0.0.1:49209`, db `trk`) for `events`, `dolt_history_issues`, `dolt_log` (hourly windows) and
`issues`, `comments`, `dependencies`, `labels` (daily snapshots); Runner with window / cadence / lookback and a
backfill command; dbt-duckdb project (external sources over bronze, staging views, marts as Parquet via
`external` materialisation, `unique` / `not_null` / `accepted_values` / `relationships` tests, source freshness
on `_extracted_at`); Temporal dev server with `MaterialiseWindow` and `BuildMarts` workflows (Fetcher and
Writer as activities, a Schedule instead of any ticker); first Go transform reading mart Parquet and writing
`bronze/derived/` inside a two-pass dbt build (`tag:pre_go`, `tag:post_go`); OTel emission from the
orchestrator; a `doc.go` per Go package naming the data-engineering concept it embodies.

**Out of scope.** S3, EKS, DuckLake, Redshift, Glue, any BI tool (DuckDB UI is only documented), the SNS/SQS
receiver, plant sensors, compaction, the legacy `claw-*` JSONL fetcher, CI.

**Hard rules.** Go never embeds DuckDB and never writes to the warehouse (Parquet only, `CGO_ENABLED=0`).
Ingestion never transforms; provenance columns are allowed. Open-source tooling only, no accounts. The Dolt
server is read-only to this project. Nothing under `bronze/`, `warehouse/`, `local/`, `.beads/`, and no
`*.duckdb` file, is ever committed.

## 2. Facts established at Phase R (2026-09-18, probed live)

- **F1 — mixed timestamp semantics in the source.** `events.created_at` is naive **Australia/Sydney wall time**
  (server `CURRENT_TIMESTAMP`, `@@time_zone = SYSTEM = AEST`): all 116 `created` events sit exactly +10.0 h
  from their issue's `issues.created_at`. `issues.*_at`, `comments.created_at`, `dependencies.created_at`,
  `dolt_log.date` and `dolt_history_issues.commit_date` are naive **UTC**.
- **F2 — Dolt time travel.** `SELECT … FROM <table> AS OF TIMESTAMP('YYYY-MM-DD HH:MM:SS')` returns table state
  at that UTC instant (a bare string literal is rejected). A future instant returns the latest state; an instant
  before the table's first commit fails with `table not found`. Dolt enforces `START TRANSACTION READ ONLY`
  (a write inside it fails with error 1792) and `AS OF` works inside it.
- **F3 — payload size.** The largest hourly `dolt_history_issues` window is 4,745 rows / ~6 MB of text
  (2026-09-05 03:00 UTC), above Temporal's default 2 MB payload limit. Row data cannot travel through
  workflow/activity payloads.
- **F4 — DuckDB 1.5.5 partitions.** `dt=` directories are parsed as hive partitions; the `hh=HH.parquet`
  *filename* is not a partition column. Window columns carry the hour.
- **F5 — dbt-duckdb 1.11.0.** `external` materialisation with `options: {partition_by: dt, overwrite: true}`
  writes hive-partitioned Parquet and removes stale partitions; the parent directory must already exist;
  afterwards the DuckDB file holds only views. Source freshness works over `external_location` sources.
- **F6 — parquet-go v0.32.0** (pure Go) writes timestamps with `IsAdjustedToUTC` false or true.
- **F7 — value domains.** status ∈ {open, in_progress, blocked, deferred, closed}; issue_type ∈ {bug, feature,
  task, epic, chore, decision, event}; observed event_type ⊂ {created, updated, status_changed, commented,
  closed, reopened, dependency_added, dependency_removed, label_added, label_removed, compacted, claimed}.
  No orphaned events, comments, labels or dependencies today.
- **F8 — telemetry sink.** Grafana Alloy listens on `127.0.0.1:4317` / `4318` (the workspace's existing
  OTLP pipeline to Grafana Cloud). Nothing in this stream can read Grafana Cloud back.
- **F10 — use `127.0.0.1`, not `localhost`.** In the agent shell, `localhost` resolves to `::1` first, and gRPC
  to a local service then hangs until timeout. `temporal operator cluster health --address localhost:<port>`
  timed out, while `--address 127.0.0.1:<port>` returned `SERVING`. Alloy listens on `127.0.0.1:4317` only. The
  dev server's `default` namespace has 24 h retention, and visibility list/count queries on `WorkflowType` /
  `StartTime` / `ExecutionStatus` work.
- **F11 — Go module set pinned to the installed Go 1.25.5.** `go.temporal.io/sdk` v1.49.0 declares `go 1.26.0`,
  so with `GOTOOLCHAIN=auto` a `go get` would download go1.26 and rewrite `go.mod`. The conductor resolved the
  whole planned set under `GOTOOLCHAIN=local`:
  - parquet-go v0.32.0;
  - go-sql-driver/mysql v1.10.1;
  - Temporal SDK **v1.48.0** (declares go 1.25.4);
  - `go.temporal.io/sdk/contrib/opentelemetry` v0.8.1 (requires SDK ≥ v1.46.0);
  - `go.opentelemetry.io/otel` family **v1.46.0**.

  `CGO_ENABLED=0 go build` succeeded, and the resulting directive is `go 1.25.4`.
- **F12 — snapshot history.** `issues`, `comments`, `dependencies` and `labels` all resolve `AS OF TIMESTAMP('2026-08-22
  00:00:00')` (12 / 0 / 0 / 4 rows), so a backfill from 2026-08-21 is valid for every table.
- **F9 — toolchain installed** (conductor, 2026-09-18): Go 1.25.5; DuckDB CLI 1.5.5 and Temporal CLI 1.9.1
  (server 1.32.0, UI 2.54.1) via Homebrew; dbt-core 1.12.5 + dbt-duckdb 1.11.0 (Python duckdb 1.5.5) via
  `uv tool install --python 3.12 dbt-core --with dbt-duckdb`.

## 3. Definitions

- **Window** — half-open `[Start, End)` in UTC, aligned to its **grain**: `Hour` (Start at minute 0) or `Day`
  (Start at 00:00 UTC). `End = Start + grain`.
- **Lookback `n` at instant `T`** — the `n` consecutive windows of the grain whose last one contains `T`,
  oldest first. The window containing `T` is still open; later runs overwrite it.
- **Backfill `--from A --to B`** — every window of the grain whose Start lies in `[A 00:00Z, (B+1d) 00:00Z)`;
  both dates inclusive.
- **Bronze path** — relative to the bronze root `bronze/`: hourly `<zone>/<dataset>/dt=YYYY-MM-DD/hh=HH.parquet`,
  daily `<zone>/<dataset>/dt=YYYY-MM-DD/part-0.parquet` (the spec's day-file name, shared with future
  compaction), `dt` / `hh` taken from the window Start in UTC. Zones: `raw` (datasets `beads/<table>`) and
  `derived` (dataset `<transform>`).
- **Provenance columns** — `_extracted_at` (instant the extraction read the source), `_window_start`,
  `_window_end`, `_source_file` (the file's own bronze-relative path).

## 4. Acceptance criteria

Every AC names its check. "Live" checks need the Dolt server; live Go tests run only when `BEADS_LIVE=1`.

### A. Foundations

**AC-01 — Window arithmetic.** `internal/window` provides `Grain` (`Hour`, `Day`), `Window{Start, End, Grain}`,
`Containing(t, g)`, `Lookback(now, g, n)` and `Range(from, to, g)` with the §3 semantics. `Lookback` errors for
`n < 1`, `Range` errors for `from > to`, and an input in a non-UTC location yields the same window as its UTC
instant. Check: `go test ./internal/window/...`, including: `Lookback(2026-09-15T00:30Z, Hour, 24)` runs
2026-09-14T01:00Z → 2026-09-15T00:00Z; `Lookback(same, Day, 2)` = [2026-09-14, 2026-09-15];
`Range(2026-09-14, 2026-09-15, Hour)` has 48 windows and `Day` has 2.

**AC-02 — Record contract.** `internal/record` defines `Kind` (`String`, `Int64`, `Float64`, `Bool`,
`Timestamp` — naive wall-clock), `Column{Name, Kind}`, `Batch{Columns, Rows}` with values `nil | string | int64
| float64 | bool | time.Time`, and `Batch.Validate()`, which rejects ragged rows, duplicate column names and
values whose Go type does not match the column kind. Check: `go test ./internal/record/...`.

### B. Writer (bronze)

**AC-03 — Deterministic path.** `bronze.Path(zone, dataset, w)` is a pure function implementing §3; it errors on
a misaligned window, an unknown zone, or a dataset segment outside `[a-z0-9_]+`, so no path escapes the bronze
root. Check: table-driven tests covering `hh=00`, `hh=23`, the day boundary and every error case.

**AC-04 — Byte-identical re-runs.** Two writes with identical batch, window and `extractedAt` produce files with
equal SHA-256. The same test shows that a different `extractedAt` changes the hash, so the equality is not
vacuous. Check: `go test ./internal/bronze/...`.

**AC-05 — Overwrite, never append, atomically.** Writing a window twice leaves exactly one file, holding only
the second batch's rows. Writes go to a temp file in the target directory that is then renamed into place. No
temp file remains after success, after an injected encode failure (through a test seam), or after a context
cancelled before the rename; on failure the previous file is left intact. Check: `go test ./internal/bronze/...`.

**AC-06 — Provenance columns.** Every row carries `_extracted_at`, `_window_start`, `_window_end` (UTC-adjusted
timestamps, µs) and `_source_file` (equal to the file's own path from AC-03), appended after the batch columns.
A batch already containing any of these names is rejected. Check: Go read-back test plus
`duckdb -c "select distinct _source_file, _window_start, _window_end from read_parquet('<file>')"` on a written file.

**AC-07 — Types round-trip untransformed.** `String`, `Int64`, `Float64`, `Bool` map to the matching Parquet
types, and `Timestamp` maps to µs with `IsAdjustedToUTC = false`. NULLs stay NULL; a zero-row batch writes a
readable file carrying the full schema. Check: Go read-back test, plus DuckDB `DESCRIBE` of a written file showing
`TIMESTAMP` for a `Timestamp` column and `TIMESTAMP WITH TIME ZONE` for `_extracted_at`.

**AC-08 — Writer suite sensitivity (test-suite AC).** The Writer tests fail against each deliberately wrong
writer: a path derived from wall-clock time; an append or keep-old-rows write; nondeterministic output; missing
or wrong provenance values. Check: the stream's `test-adversary` run on the Writer suite leaves no unadjudicated
residue.

### C. Beads Fetcher

**AC-09 — Config from environment.** `beads.Config` loads from `BEADS_HOST` (default `127.0.0.1`), `BEADS_PORT`
(`49209`), `BEADS_DATABASE` (`trk`), `BEADS_USER` (`root`), `BEADS_PASSWORD` (empty) and `BEADS_EVENTS_TZ`
(`Australia/Sydney`). No credential is committed. Check: unit test of `ConfigFromEnv` with and without overrides.

**AC-10 — Declared extraction parameters.** `beads.Declaration()` returns source `beads`, cadence 15 minutes, and
exactly seven tables as inspectable data: `events`, `dolt_history_issues`, `dolt_log` (grain `Hour`, lookback 24)
and `issues`, `comments`, `dependencies`, `labels` (grain `Day`, lookback 2). Check: unit test asserting the
full declaration.

**AC-11 — Event-log window predicates.** Hourly tables select rows whose time column lies in `[Start, End)`.
`events.created_at` is compared against bounds converted to `BEADS_EVENTS_TZ` wall time (F1).
`dolt_history_issues.commit_date` and `dolt_log.date` use UTC bounds. Check: unit tests on the pure query
builder. Window 2026-09-15T03:00Z gives events bounds `2026-09-15 13:00:00` / `2026-09-15 14:00:00` and history
bounds `2026-09-15 03:00:00` / `2026-09-15 04:00:00`. Window 2026-10-03T16:00Z (Sydney DST start) gives events
bounds `2026-10-04 03:00:00` / `2026-10-04 04:00:00`.

**AC-12 — Snapshots via time travel.** Daily tables are read as `SELECT * FROM <table> AS OF
TIMESTAMP('<window End, UTC>')` (F2). Fetching the same closed day twice yields equal batches. A window before
the table's first commit returns an error naming the table and window, never an empty batch. Check: live test
with `BEADS_LIVE=1`.

**AC-13 — Deterministic, untransformed batches.** Every query orders by a unique key: events `(created_at, id)`,
dolt_history_issues `(commit_date, commit_hash, id)`, dolt_log `(date, commit_hash)`, issues / comments /
dependencies `(id)`, labels `(issue_id, label)`. Columns are the table's `SELECT *` columns in table order, with
kinds derived from the driver's column types; an unknown type is an error, TINYINT stays `Int64` and JSON stays
`String`. Values are exactly what the source served, so `events.created_at` stays Sydney wall time. Check: live
test. A closed hourly window fetched twice gives equal batches, and the summed events row count over the 24
windows of UTC day 2026-09-15 equals an independent `COUNT(*)` over the converted bounds.

**AC-14 — Read-only by construction.** Every fetch runs inside `BeginTx(ctx, &sql.TxOptions{ReadOnly: true})`,
and the package's non-test code contains no `INSERT|UPDATE|DELETE|REPLACE|CREATE|DROP|ALTER|CALL` statement text.
Check: grep in verification plus a unit test on the transaction options.

### D. Runner and backfill

**AC-15 — Backfill command.** `go run ./cmd/ingest --source beads --from 2026-09-14 --to 2026-09-15` exits 0.
It leaves exactly 48 files in each hourly table's directory and 2 in each daily table's directory under
`bronze/raw/beads/<table>/`, and prints a per-table summary (windows, rows, bytes).

**AC-16 — Idempotent re-run.** Running the AC-15 command again leaves the same file set, with no additions. For
every table, the rows excluding `_extracted_at` are identical in both directions (DuckDB `EXCEPT` returns 0 rows
each way against a copy taken before the re-run).

**AC-17 — Replay after loss.** After deleting `bronze/raw/beads/events/dt=2026-09-15/`, a backfill of
2026-09-15 alone restores 24 files. Their content, excluding `_extracted_at`, equals the content before
deletion.

**AC-18 — Lookback run.** `go run ./cmd/ingest --source beads --once --now 2026-09-15T00:30:00Z` writes exactly
the §3 lookback windows per table and nothing else: 24 hourly windows ending with 2026-09-15T00 per hourly
table, and days 2026-09-14 and 2026-09-15 per daily table. Without `--now` it uses the current time. Check: file
modification times before/after, or the command's own summary, plus a runner unit test with a fake clock.

**AC-19 — Failure handling.** A failing window is retried a bounded number of times. A persistent failure does
not stop other windows. The command exits non-zero, naming every failed table and window, and a failed window
leaves no partial file (its previous file is untouched). Check: runner unit tests with a fake Fetcher.

### E. dbt project

**AC-20 — Project wiring.** `dbt/` is a dbt-duckdb project run from the repo root as
`dbt <cmd> --project-dir dbt --profiles-dir dbt`, with the DuckDB file at `warehouse/dev.duckdb`, and `dbt parse`
succeeds. File paths and `read_parquet` appear only in `dbt/models/sources.yml`: a grep over `dbt/models/**/*.sql`
finds neither `read_parquet` nor `bronze/`.

**AC-21 — External sources with freshness.** `sources.yml` declares all seven raw beads tables as external
locations over their bronze globs (`hive_partitioning`, `union_by_name`). Each has
`loaded_at_field: _extracted_at` and warn / error thresholds. Right after `go run ./cmd/ingest --source beads
--once`, `dbt source freshness --project-dir dbt --profiles-dir dbt` exits 0, with every source at pass or warn.

**AC-22 — Staging views.** There is one staging model per raw table (7), each materialised as a view with an
explicit column list (no `select *`), casts and renames only, and no joins. Timestamps become UTC `timestamptz`:
`events.created_at` is interpreted as Australia/Sydney wall time (F1), all others as UTC. The events model also
exposes `created_date_local` (Australia/Sydney date) and is de-duplicated on `id`, since the DST fall-back hour
maps two UTC windows onto one local range. No DuckDB-only types or `QUALIFY` in staging. Check:
- a singular test that every `created` event's UTC `created_at` lies within 60 seconds of its issue's UTC
  `created_at`, which proves the F1 conversion;
- `grep -rniE 'select \*|qualify|join' dbt/models/staging` finds nothing;
- `information_schema` shows the staging models as views.

**AC-23 — Data tests.** The project declares:
- `unique` + `not_null` on every staging key, with composite keys tested as expressions;
- `accepted_values` on status, issue_type and event_type (the F7 domains). status and issue_type use error
  severity. event_type uses warn severity, because beads adds event kinds more often than statuses and a new
  kind must not halt the pipeline;
- `relationships` from events.issue_id to the issues snapshot staging model, at error severity and immune to the
  extraction race (an event whose issue was created after that day's snapshot was taken);
- a singular test that no issue is closed before it was created.

Check: `dbt build --select tag:pre_go` exits 0 with every test passing.

**AC-24 — Parquet marts, views-only DuckDB.** Five `pre_go` marts materialise `external` under
`warehouse/marts/<model>/`: `dim_issues`, `dim_issues_scd2`, `fct_issue_events`, `mart_daily_throughput` and
`mart_agent_activity_hourly`. Date-grained marts are hive-partitioned by a local `dt` with overwrite. After a
build, `warehouse/dev.duckdb` contains zero `BASE TABLE` relations. `dim_issues` has one row per issue from the
latest snapshot (`unique` + `not_null` on `issue_id`), with at least `issue_id, title, status, issue_type,
priority, created_at, closed_at, cycle_time_hours, description, design, acceptance_criteria, notes,
close_reason`.

**AC-25 — SCD Type 2.** `dim_issues_scd2` has one row per issue per contiguous run of unchanged tracked
attributes (status, priority, issue_type, assignee, title) across daily snapshots, with `valid_from`, `valid_to`
and `is_current`. Tests enforce exactly one current row per issue, `valid_to` null iff current, and no
overlapping intervals.

**AC-26 — Rebuild from bronze.** `rm -rf warehouse && mkdir -p warehouse/marts` followed by
`dbt build --select tag:pre_go` recreates every pre_go mart with the same row counts it had before deletion.

**AC-27 — Two-pass tags.** Every model carries exactly one of `pre_go` / `post_go`.
`dbt ls --resource-type model --select tag:pre_go` lists the 7 staging models and the 5 pre_go marts;
`--select tag:post_go` lists only `mart_issue_mention_drift`. The union of the two lists equals the full model
list (13 models), with no name in both.

### F. First Go transform

**AC-28 — Transform contract.** `internal/transform` defines `Contract{Name, Inputs, Output, Grain, Tests}`,
declared by every Go transform, and a registry the orchestrator can list. `issue_mentions` declares inputs
`[dim_issues]`, output `issue_mentions`, grain `Day`. Check: unit test over the registry.

**AC-29 — Mention extraction.** From each dim_issues text field (title, description, design, acceptance_criteria,
notes, close_reason), the transform extracts references to issue ids whose prefix occurs among dim_issues ids
(e.g. `trk-`), including child ids such as `trk-bam.1`. It emits one row per `(issue_id, mentioned_issue_id,
field)` with a `mention_count`. Self-mentions and unknown prefixes are ignored. Check: table-driven tests
covering punctuation and word boundaries, child ids, duplicates within a field, and several fields.

**AC-30 — Parquet in, bronze/derived out.** `go run ./cmd/transform issue_mentions --window 2026-09-18` reads
`warehouse/marts/dim_issues/` with parquet-go and writes
`bronze/derived/issue_mentions/dt=2026-09-18/part-0.parquet` through the shared Writer, provenance included. A
re-run overwrites that file.

**AC-31 — Declared derived source and post_go mart.** `sources.yml` declares source `derived`, table
`issue_mentions`, over its bronze path, with freshness on `_extracted_at`. `mart_issue_mention_drift` (post_go,
external Parquet) lists the mentions in the latest `dt` that have no dependency edge, in either direction,
between the two issues.

**AC-32 — Two-pass build.** Starting from a warehouse with no marts, `dbt build --select tag:pre_go`, then the
transform, then `dbt build --select tag:post_go` each exit 0, and `warehouse/marts/mart_issue_mention_drift/`
holds Parquet.

### G. Temporal orchestration

**AC-33 — MaterialiseWindow.** Workflow `MaterialiseWindow(source, window)` materialises every table of the
source whose grain matches the window. For each table it runs activity `FetchWindow` (Fetcher → spool file
under `local/spool/`, written atomically via temp file and rename), then `WriteWindow` (spool → Writer →
bronze), then `DeleteSpool`. `DeleteSpool` is an idempotent cleanup; a missing file counts as success, so a
retried `WriteWindow` always still finds its spool. The activities exchange only a
spool reference (path, row count, fetch instant), never rows (F3). Both have StartToClose timeouts, retry
policies and heartbeats, and `_extracted_at` equals the fetch instant. Check: testsuite tests, plus a spool
round-trip test that is lossless for every kind and for NULL.

**AC-34 — MaterialiseSource and determinism.** Workflow `MaterialiseSource(source)` computes each grain's lookback
windows from `workflow.Now`, starts one `MaterialiseWindow` child per window with a deterministic workflow id, and
fails if any child fails. Workflow code uses no wall clock, native goroutines, randomness or I/O. Check: testsuite
tests with mocked activities covering window enumeration, success and child failure.

**AC-35 — Schedule replaces the ticker.** `go run ./cmd/worker -apply-schedule` upserts Temporal schedule
`materialise-beads` (interval = declared cadence 15 m, overlap policy Skip), which starts `MaterialiseSource`.
`temporal schedule describe --schedule-id materialise-beads` shows it. No ticker or polling loop exists:
`grep -rE 'time\.(NewTicker|Tick)\(' cmd internal` is empty.

**AC-36 — Live materialisation.** With `temporal server start-dev` and the worker running (both addressed as
`127.0.0.1:7233`, F10),
`temporal schedule trigger --schedule-id materialise-beads` produces a `MaterialiseSource` execution that
completes, and the lookback files' `_extracted_at` values move past the trigger time.

**AC-37 — BuildMarts two-pass.** Workflow `BuildMarts(window)` runs activity `DbtBuild(tag:pre_go)`, then one
activity per registered Go transform, then `DbtBuild(tag:post_go)`. A failed first pass stops it before any
transform runs. A dbt failure is a non-retryable error naming the failing nodes from `run_results.json`.
`MaterialiseSource` starts `BuildMarts` only after all ingestion children succeed. Check: testsuite tests for
ordering and failure; live, a triggered run leaves mart files newer than the trigger.

### H. Observability

**AC-38 — OTel setup.** `internal/telemetry` installs global tracer and meter providers with
`service.name=data-platform-worker`. The exporter is chosen by `PLATFORM_OTEL_EXPORTER`: `otlp` (default: when
no `OTEL_EXPORTER_OTLP_*` endpoint env is set, it targets `127.0.0.1:4317` insecure, the workspace Alloy
collector, F8/F10), `stdout`, or `none`. Shutdown flushes. Check: unit tests for exporter selection.

**AC-39 — Spans and metrics.** Workflows and activities produce spans through the Temporal OTel interceptor.
`WriteWindow` adds counters `platform.rows_written` and `platform.bytes_written` (attributes source, table, zone).
Each activity records histogram `platform.activity.duration` (attributes activity, outcome = success|failure).
`run_results.json` becomes one span per dbt node, named `dbt.<resource_type>.<name>` with status and timing
taken from the file, plus counter `platform.dbt.nodes` by status. Check: unit tests with in-memory exporters and a
fixture `run_results.json`.

**AC-40 — Live emission.** A triggered `MaterialiseSource` run with `PLATFORM_OTEL_EXPORTER=stdout` prints spans
for MaterialiseSource, MaterialiseWindow, FetchWindow, WriteWindow and DbtBuild, and at least one span whose name
starts with `dbt.model.`, to the worker's output.

### I. Cross-cutting

**AC-41 — doc.go per package.** Every Go package under `cmd/` and `internal/` has a `doc.go` whose package
comment contains a line starting `Data-engineering concept:` naming the concept the package embodies. Check: a
loop over `go list -f '{{.Dir}}' ./...`.

**AC-42 — No CGO, no DuckDB, Parquet only.** `CGO_ENABLED=0 go build ./...` succeeds and
`go list -deps ./... | grep -i duckdb` is empty. In non-test Go code, file-creating calls (`os.Create`,
`os.CreateTemp`, `os.WriteFile`, `os.OpenFile`, `os.Rename`) appear only in the Writer (bronze root) and the
spool (`local/spool/`). Go reads marts only as Parquet files.

**AC-43 — Quality gates.** `gofmt -l .` prints nothing; `go vet ./...` and `BEADS_LIVE=1 go test ./...` pass.

**AC-44 — Repo hygiene.** On branch `phase-1`, `git ls-files` lists nothing under `bronze/`, `warehouse/`,
`local/` or `.beads/`, and no `*.duckdb`. `go.mod` declares module `github.com/DMokong/data-platform`, and its
`go` directive is `1.25` or `1.25.x` with x ≤ 5. There is no `toolchain` line requiring a newer Go (F11).
`go mod tidy -diff` reports no changes.

**AC-45 — Versions pinned.** The decision log in `docs/data-platform-spec.md` records the F9 toolchain versions
and the Go module versions from `go.mod` (parquet-go, go-sql-driver/mysql, Temporal SDK, OTel).

**AC-46 — Runbook.** `README.md` documents the end-to-end phase-1 run: dev server, worker and schedule, backfill,
two-pass build, transform, and the DuckDB UI over mart Parquet. A `Makefile` exposes the same steps.
- The one-shot targets `check`, `backfill`, `ingest-once`, `build-marts` (= `dbt-pre`, `transform`,
  `dbt-post`), `freshness` and `live`, plus the UI view file, were each executed in the final integration task,
  with evidence in its report.
- The long-running targets `temporal-dev` and `worker`, and the `schedule` / `trigger` steps, are exercised
  through `make live`, whose script performs exactly those steps; the runbook says so.
- Only the interactive `duckdb -ui` is not executed.

## 5. Self-review (conductor, Phase 2)

- **Placeholders:** none. Every threshold and name that later tasks depend on is fixed here (paths, table list,
  lookbacks, workflow and schedule names, mart names, metric names, env vars).
- **Contradictions checked:**
  - *Byte-identical* (AC-04) vs *`_extracted_at` from the clock*: resolved by injecting `extractedAt`. Re-runs
    are compared excluding `_extracted_at` (AC-16, AC-17).
  - *Ingestion never transforms* vs F1: resolved by keeping `events.created_at` raw in bronze and converting it
    in staging (AC-13, AC-22).
  - *Fetcher and Writer as activities* vs F3: resolved by the spool claim-check (AC-33).
- **Interpretations recorded** (none changes the design):
  - `dolt_log` is included because it is in the spec §1 table.
  - Daily files are named `part-0.parquet`, the spec's day-file name.
  - `MaterialiseSource` is the schedule entrypoint around the spec's `MaterialiseWindow` (§8 names the
    per-window workflow and the Schedule but not what the Schedule starts).
  - Mart names follow the §5 mart list.
- **Independent pre-execution review:** a fresh-context opus reviewer cold-read spec, plan and briefs on
  2026-09-18 and reported 16 findings.
  - The 3 blockers were: SDK v1.49.0 needs Go 1.26 (→ F11 pins), schedule catch-up swallowing the live
    trigger, and a DuckDB `rows` alias error.
  - The 6 majors were: runbook coverage, fixture clobbering, command timeouts, a nondeterminism grep matching
    tests, the shared git index, and AC verification gaps.

  All were verified by the conductor and fixed before the plan was presented, and the ACs above reflect the
  fixes (AC-05, AC-22 to AC-24, AC-27, AC-33, AC-44, AC-46).
- **Ambiguity left to implementers:** none that an AC depends on. Internal structure beyond the named
  interfaces is theirs.
