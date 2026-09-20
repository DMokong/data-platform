# Data Platform — Architecture Spec

Personal learning project. Goal: build a real, end-to-end data platform on data I actually use, in Go plus dbt, starting small and growing into a team-grade deployment on EKS.

Companion documents: [`review-2026-09-15.html`](review-2026-09-15.html) (architecture review) and [`duckdb-at-scale-2026-09-16.html`](duckdb-at-scale-2026-09-16.html) (self-hosted DuckDB viability). Both are dated point-in-time analyses and are deliberately left unrevised; later movement is recorded in the decision log at the foot of this file. The phase-1 build's own spec, evidence and Gate-5 report live in [`fable-streams/2026-09-18-phase-1-build/`](fable-streams/2026-09-18-phase-1-build/).

Interactive diagrams (archify; open the `.html` in a browser, the `.json` beside each is the source):

| | Data flow | Deployment components |
|---|---|---|
| Phase 1 | [`diagrams/phase1.dataflow.html`](diagrams/phase1.dataflow.html) | [`diagrams/phase1.architecture.html`](diagrams/phase1.architecture.html) |
| Phase 2 (DuckDB-lakehouse path) | [`diagrams/phase2.dataflow.html`](diagrams/phase2.dataflow.html) | [`diagrams/phase2.architecture.html`](diagrams/phase2.architecture.html) |

The two data flows are deliberately the same shape; the diff between them is the migration: bronze moves to S3, marts commit through the DuckLake catalog, and every reader (BI and Go transforms) goes through the Postgres-wire serving layer.

## Principles

1. **The warehouse is the contract, not the language.** dbt models and Go transforms both read tables and write tables. Neither knows the other exists; they agree only on table names.
2. **Extraction is persistent, not transient.** Raw data is written to Parquet once and kept forever. Everything downstream is derived and rebuildable; the Parquet is the only thing you'd miss.
3. **Ingestion never transforms.** If a field is being cleaned in Go before the write, it belongs in a staging model instead. Stamping ingestion metadata (`_extracted_at`, `_window_start`, `_window_end`, `_source_file`) on every row is not transformation — it is provenance, and the freshness tests depend on it.
4. **Storage is separate from compute.** Nothing durable lives on pod disk. Compute processes are disposable; the only state is object storage plus a catalog.
5. **Open-source tooling; no new vendor subscriptions.** dbt Core (Apache), DuckDB (MIT), Temporal server (MIT). AWS managed services already approved in org (S3, Redshift, Glue) are allowed. No hosted tiers, no new SaaS.
6. **Go never writes to the warehouse.** Go writes Parquet. The warehouse reads Parquet in place. This keeps the Go binary free of warehouse drivers (and of CGO, which `go-duckdb` requires) and makes the warehouse swappable.

## Requirement: idempotent re-runs

Running the same extract twice must leave the platform in the same state, never with doubled data.

- File paths are **deterministic**, derived from the time window being extracted — never from the wall clock at run time.
- One file per source per time window. Re-running a window **overwrites** that file; it never appends a second copy. (Parquet cannot be appended to in any case: the schema and row-group index live in a footer.)
- dbt rebuilds from whatever is on disk, so a re-run is just a re-run — no cleanup step, no manual dedupe.
- Staging-level dedupe on a natural key is the **fallback only**, for sources where duplicates cannot be prevented at write time. Prefer fixing it at the file level.

Three parameters, declared per source, make this operational:

| Parameter | Meaning | Beads default |
|---|---|---|
| `window` | The unit of one file. A window is fully re-fetchable from the source. | 1 hour for events; 1 day for snapshots |
| `cadence` | How often the Runner fires. | Every 15 minutes |
| `lookback` | How many trailing windows each run re-extracts, to absorb late or corrected data. | 24 hourly windows (one day) |

**Source shapes.** Everything above assumes a window can be re-read. Three shapes qualify, and the third is how a streaming source is admitted without relaxing a single rule:

| Shape | Example | How a window is re-read |
|---|---|---|
| **Re-fetchable** | beads `events` (a time column), `issues` (Dolt `AS OF`) | Query the source again for the window's bounds. The default. |
| **Current-value only** | a device that reports "now" and keeps no history | Cannot be re-extracted. One snapshot file per window, plus staging-level dedupe on the natural key. |
| **Push** | an SNS topic (§1) | The source is never re-read. A Receiver lands every message durably first, and the Fetcher re-reads the **landing zone** (§3) in its place. |

The push shape is the one worth understanding. SNS keeps nothing and cannot be asked for a past hour, so a Fetcher that merely subscribed would lose messages on a crash, and the overwrite rule would then delete the first run's rows on the retry. Putting a durable log in front of it restores the property every layer downstream depends on.

**Backfill** is a first-class Runner command: `ingest --source beads --from 2026-08-21 --to 2026-09-15` walks the windows and overwrites each file. Nothing else is needed, because of the rules above.

## Layers

### 1. Sources

**Source one: the beads tracker.** Dolt server on `127.0.0.1:49209`, database `trk`, MySQL wire protocol (`go-sql-driver/mysql`). Chosen because it is real data I use daily, it has clean timestamps, and it serves history:

| Table | Rows (2026-09-21) | Extraction shape | Why it's interesting |
|---|---|---|---|
| `events` | 736 | Hourly windows on `created_at` | Append-only log: `created`, `claimed`, `status_changed`, `closed`, `label_added`, … Natural event stream. |
| `issues` | 154 | Daily snapshot | Current state, 48 columns. Snapshots become a slowly-changing dimension in marts. |
| `dolt_history_issues` | 94,193 versions | Hourly windows on `commit_date` | Dolt's built-in row history: every version of every issue with its commit. Time travel for free. |
| `comments`, `dependencies`, `labels` | 389 / 116 / 278 | Daily snapshot | Joins for marts. |
| `dolt_log` | 1,104 commits | Hourly windows on `date` | Commit cadence = agent activity. |

Legacy `claw-*` history (726 issues) lives in `docs/archive/beads-legacy-2026-08-21/*.jsonl` and is a one-off backfill via a second Fetcher.

**Source two, designed here but not yet built: an SNS topic.** An ad hoc stream with no schedule and no retention. It reaches bronze through a durable landing zone:

```
SNS topic ──subscription──▶ SQS queue ──long poll──▶ Receiver ──▶ landing/<source>/dt=…/hh=…/<batch>.jsonl.gz
                                                                       │
                       MaterialiseWindow(source, window) ◀─────────────┘
                       The Fetcher reads landing/ for the window, filters on the SNS
                       publish Timestamp, dedupes on MessageId, hands rows to the Writer
```

- **Subscribe an SQS queue to the topic**, with raw message delivery **off** so the SNS envelope survives: `MessageId`, `Timestamp`, `TopicArn`, `MessageAttributes`. That envelope is the provenance. The queue buffers for 14 days, so a Receiver outage costs nothing.
- **The Receiver** long-polls, writes each batch to the landing zone, and only then deletes those messages from the queue. Write before acknowledge: a crash between the two steps produces a duplicate in landing, never a loss.
- **Windows are cut on publish time, not receive time.** A message published inside one window can land after that window closed, so the source's `lookback` absorbs it exactly as it absorbs a late row from a pull source.
- **Dedupe on `MessageId`.** SQS delivers at least once, so the Fetcher dedupes while building the window. Bronze is effectively-once and nothing downstream needs to know.
- **Kinesis Data Firehose is the managed service that does the Receiver's job**, and it is not available in this org. That absence is the only reason the Receiver exists.

The message body stays an opaque string in bronze and is unpacked in a staging model, because ingestion never transforms (principle 3).

**Later sources**, each a separate Fetcher: plant sensor readings (once a device exists; Home Assistant has none today), other APIs, household spending.

### 2. Go ingestion service
Four small pieces. The first three are the whole pipeline for a pull source; the fourth exists only for push sources.
- **Fetcher** — talks to the source, handles rate limits and formats, knows nothing else. One per source. For a push source it reads the landing zone rather than the source itself.
- **Writer** — takes records and writes one Parquet file per window using the deterministic path rule, stamps the provenance columns, overwrites on re-run. Shared by every Fetcher and by every Go transform, and the only component allowed to create files apart from the orchestrator's spool.
- **Runner** — wires them together; window, cadence, lookback, retries, logging, backfill.
- **Receiver** (push sources only) — long-polls SQS, writes each batch to the landing zone, then deletes from the queue. It is the one component Temporal does **not** schedule: it runs continuously, and the queue holds the backlog whenever it stops.

Fetcher and Writer are plain functions taking `context.Context` and returning an error, so they became Temporal Activities unchanged. Phase 1 went straight to Temporal instead of building a throwaway ticker first; see §8.

### 3. Bronze layer (raw)
Parquet files. Immutable, replayable, columnar, compressed. Read natively by DuckDB and Go. Phase 1 on local disk; phase 2 on S3.

Two zones, same Writer, same path rule:

```
bronze/raw/<source>/<table>/dt=YYYY-MM-DD/hh=HH.parquet      # written by Fetchers
bronze/derived/<transform>/dt=YYYY-MM-DD/hh=HH.parquet       # written by Go transforms
```

A daily window is named `dt=YYYY-MM-DD/part-0.parquet` instead of an `hh=` file.

**The landing zone** sits beside bronze rather than inside it, and exists only for push sources:

```
landing/<source>/dt=YYYY-MM-DD/hh=HH/<batch-uuid>.jsonl.gz    # written by the Receiver
```

It breaks the bronze rules on purpose: many small files per window, append-only, never overwritten, gzipped JSON instead of Parquet. It is a durable log, not a table, and it is precisely the thing a push source lacks. Bronze stays the clean window-derived layer everything downstream reads.

**Empty windows are data.** A window with no rows still writes a file, so "the source was quiet" stays distinguishable from "the Runner never ran". Such a file carries no `_extracted_at` value, which changes what freshness can tell you; see §7.

The Hive-style `dt=` directory is the partition every engine (DuckDB, Redshift Spectrum, Athena, Trino) prunes on. `hh=HH` is only the file name within a day: engines skip those files by their own min/max row-group statistics, not by name (phase-1 finding F4).

**Compaction** (later): once a day is closed, roll its hourly files into one `dt=YYYY-MM-DD/part-0.parquet`. DuckDB is indifferent; server engines over S3 are not, and 24 small files a day per table adds up. The compactor is a Go job under the same idempotency rule (output path derived from the day).

**Schema evolution**: when a source grows a column, older files lack it. DuckDB reads mixed files with `read_parquet(..., union_by_name = true)`. Staging models list columns explicitly so a new field cannot reach marts unnoticed.

### 4. Warehouse

**Raw tables are external tables over bronze. Nothing loads them.** In phase 1 dbt-duckdb declares each bronze path as an external source; the "raw table" is a name over files. In phase 2 the same declaration becomes a Redshift Spectrum external table (via Glue Data Catalog) or a DuckLake/Iceberg table. Go is never involved. If a native copy is ever wanted for query speed, it is a dbt model that materialises raw into a table, so it stays inside the lineage graph.

- Phase 1: **DuckDB** — embedded, no server, queries Parquet directly. Known constraint: a DuckDB database file is open to **one read-write process, or any number of read-only processes, never both**. Phase 1 sidesteps it: marts are materialised as **Parquet files** (dbt-duckdb `external` materialisation, `warehouse/marts/<model>/dt=….parquet`), so `dev.duckdb` holds only views and is rebuildable. Readers (DuckDB UI, Go transforms) open their own DuckDB over the Parquet and never touch the writer's file. This is a miniature of the phase-2 lakehouse: Parquet is the state, DuckDB processes are disposable.
- Phase 2, two candidates under evaluation (see the viability analysis):
  - **Redshift** (approved in org). Columnar, server-based, handles concurrent workers and team queries, native BI drivers. Requires Glue Data Catalog for Spectrum. Athena is not available in org.
  - **Self-hosted DuckDB lakehouse**: DuckDB as the execution engine, **DuckLake** (catalog in Postgres, data as Parquet on S3) or Iceberg as the shared table format, a Postgres-wire serving layer (`pg_duckdb`) for BI concurrency. Preferred if the viability analysis holds up, because it keeps principle 4 pure and principle 5 cheap.

    Two Postgres roles, and they are different databases:
    - **DuckLake catalog** — plain Postgres tables (table list, snapshots, file manifests). Any Postgres works, so this is **RDS** (approved, Multi-AZ, backups handled). Temporal's own persistence is plain Postgres too and can share this instance.
    - **Serving layer** — Postgres with the `pg_duckdb` extension loaded. RDS only permits AWS-approved extensions and `pg_duckdb` is not one, so this instance is a **pod in the cluster** (StatefulSet or plain Deployment; it holds no durable state of its own, it reads the catalog and S3). If pg_duckdb ever lands on RDS, the two collapse into one managed instance.
- Not a fit for the warehouse role: RDS row-store engines. ClickHouse would suit sensor time series but is a different model from dbt marts.

**Dialect discipline** so the migration is a profile change and not a rewrite: `read_parquet` and paths live in `sources.yml`, never in models; avoid DuckDB-only types (list, struct) and syntax (`QUALIFY`) in staging; run `dbt compile` against both targets in CI once phase 2 starts.

### 5. dbt Core
Three tiers, kept strictly separate:
- **sources.yml** — declares raw and derived tables as external locations over bronze, with **freshness** on `_extracted_at`. Includes tables written by Go transforms, so the lineage graph stays honest about what dbt doesn't own.
- **staging** — one model per source table. Casting, renaming, timezone normalisation, explicit column lists. No joins, no business logic. Boring on purpose. Materialised as **views**.

  Timezone normalisation is load-bearing, not cosmetic. The beads server stores `events.created_at` as naive **Australia/Sydney** wall time while every other timestamp it serves is naive UTC. Bronze keeps the value exactly as served (principle 3) and staging converts it. That conversion forces a dedupe: on the daylight-saving fall-back hour, two adjacent UTC windows cover the same local hour, so `stg_beads__events` keeps one row per source id. This is the sanctioned use of the staging-dedupe fallback, recorded here so a later reader does not mistake it for carelessness.
- **marts** — joins, aggregates, SCD Type 2 over issue snapshots, cycle-time and throughput, agent activity by hour, drift detection. Materialised as **tables** — in phase 1 as Parquet via dbt-duckdb's `external` materialisation, in phase 2 as DuckLake tables (or Redshift tables). Move to **incremental** only when a full rebuild is genuinely slow.

Models are tagged `pre_go` or `post_go` so the orchestrator can build in two passes around Go transforms (§8).

### 6. Go transforms (optional)
Where SQL is the wrong tool: row-by-row logic, free-text parsing (issue descriptions, close reasons), calling a model, custom quality checks needing real error handling. Rule of thumb: if it can be a SELECT without pain, it's a dbt model; otherwise it's Go.

Every transform, dbt or Go, satisfies the same contract: **inputs** (table names), **one output** (table name), **window**, **tests**. A dbt model gets this from `ref()` and `schema.yml`; a Go transform declares it in a struct the orchestrator can read.

Go transforms read **mart Parquet** (phase 1, via `parquet-go`/Arrow) or marts over the serving layer's Postgres wire (phase 2, via `pgx`) — never via an embedded DuckDB, and never staging views, which exist only inside DuckDB. They write to `bronze/derived/` through the same Writer. Their outputs are declared in `sources.yml`, so downstream marts can build on them and freshness tests catch a transform that silently stopped.

### 7. Tests and data quality
dbt tests are assertions about the **data**, not unit tests of SQL. They run every production run via `dbt build`, which builds each model then immediately tests it and halts on failure. Same tests run by hand in development.

Placement:
- `unique` and `not_null` on staging keys
- `accepted_values` on `status`, `issue_type`, `event_type`
- referential checks between events and issues. dbt 1.12.5 rejects a `ref()` inside a generic test's `where` clause, so the events-to-issues check is a **singular** test carrying a race guard, because an event can arrive after that day's issue snapshot was taken. The built-in `relationships` generic test is used wherever no guard is needed
- **source freshness** on raw and derived tables, which catches Go ingestion or a Go transform silently stopping. Thresholds follow the source shape. A daily snapshot always has rows, so it can be tight. An hourly event table goes quiet legitimately, and an ad hoc push source is quiet most of the time. Because an empty window carries no `_extracted_at` (§3), freshness on those sources reports the last window that *had* rows, not the last window attempted. For an ad hoc source, freshness is therefore a liveness check on the Receiver and its queue, using queue age and the Receiver's own heartbeat, rather than a statement about the data
- custom tests as SQL files returning rows (zero rows = pass): e.g. no issue closed before it was created; daily-average windows straddling a DST change

Go side: the Writer's path derivation is pure and test-driven — two runs of one window must produce byte-identical files.

### 8. Orchestrator
Go, on **Temporal**. Airflow was considered and dropped: it would add a Python orchestration layer over the Go core and could only see the Go binaries as opaque pods. Dagster was considered for its asset-graph model; borrow the vocabulary (workflows named after the asset they materialise, not the job) without adopting the tool.

- One `MaterialiseWindow(source, window)` Workflow per source; Fetcher and Writer are Activities with retries and heartbeats. A Temporal **Schedule** replaces the ticker.
- **Rows never travel inside a workflow payload.** The largest hourly window measured is about 6 MB, well over Temporal's 2 MB default limit. `FetchWindow` writes rows to a spool file under `local/spool/` and returns only a reference: path, row count, fetch instant. `WriteWindow` reads the spool and writes bronze, and a third Activity deletes it. This claim-check belongs to the orchestrator rather than to any source, so it carries into phase 2 with the spool on a volume or in object storage.
- A `BuildMarts(window)` Workflow runs: `dbt build --select tag:pre_go` → Go transforms as Activities → `dbt build --select tag:post_go`. dbt's own DAG stays inside each `dbt build`.
- Phase 1: `temporal server start-dev` locally. Phase 2: Temporal server via Helm on EKS, Go workers as Deployments. Multiple workers writing to different bronze partitions is safe (separate files); only the warehouse needs to handle concurrency, which is the phase-2 warehouse question.

### 9. Visualisation
- Now: **DuckDB UI** (`duckdb -ui`) in its own process over the mart Parquet files, so it never collides with `dbt build`. Its job is to confirm marts contain what they should. No Metabase in phase 1: its DuckDB driver is a community plugin and it adds nothing to validation.
- Later: a real visualiser needing **concurrent reads by many users**. This is a phase-2 decision coupled to the warehouse choice: Redshift brings native drivers for Metabase, Grafana, QuickSight; a self-hosted DuckDB lakehouse serves BI through a Postgres-wire layer (`pg_duckdb`) or through readers that each open their own DuckDB over DuckLake/Parquet (Grafana DuckDB plugin, Evidence). Either way the reads never touch a `.duckdb` file that a writer holds.

### 10. Observability
The orchestrator emits OTel spans and metrics per workflow (rows written, bytes, duration, outcome) and parses dbt's `run_results.json` into the same stream. Reuses the existing Grafana Cloud pipeline in this workspace; no separate data-observability tool.

### 11. Configuration and secrets
One `Config` struct per Fetcher, populated from environment variables locally and Kubernetes Secrets on EKS. The Dolt server needs host, port and database; later sources add tokens. AWS access on EKS is by IAM Roles for Service Accounts, so no key ever reaches a pod's environment. Nothing in the repo.

## Deployment phases

| | Phase 1 — learning | Phase 2 — EKS |
|---|---|---|
| Raw storage | local disk | S3 |
| Push ingestion | not built; an SQS queue is reachable from a laptop if it is ever wanted | SNS → SQS → Receiver Deployment → `landing/` on S3 |
| Table format | plain Parquet, Hive partitions (bronze and marts) | Parquet + DuckLake or Iceberg (decision pending) |
| Catalog | none (paths in `sources.yml`) | Glue Data Catalog (Redshift path) or Postgres (DuckLake path) |
| Warehouse | DuckDB; marts as Parquet, `dev.duckdb` views only | Redshift **or** self-hosted DuckDB lakehouse |
| Orchestration | Temporal dev server | Temporal on EKS (Helm), workers as Deployments |
| Go service | single process | Deployment / CronJob |
| Visualisation | DuckDB UI | BI over Postgres wire or native Redshift drivers |
| Observability | OTel → Grafana Cloud | same |

Pods stay disposable because nothing durable lives in them.

## Later
- Compaction of hourly bronze files into daily files.
- Table format for phase 2: DuckLake if the DuckDB path is chosen, Iceberg if Redshift (Redshift reads Iceberg via Spectrum). Either gives schema evolution, time travel, and concurrent writes to the same table. Go tooling (`iceberg-go`) is read-heavy, which is fine: Go only ever writes bronze Parquet.
- Legacy `claw-*` JSONL backfill Fetcher.
- The Receiver and the SNS source of §1: designed here, not yet built.
- Plant sensors, once there is a device.

## Decision log

| Date | Decision | Reasoning |
|---|---|---|
| 2026-09-15 | Raw tables are external tables; Go never writes to the warehouse | Honours principles 2 and 4; keeps Go free of CGO and warehouse drivers; makes the warehouse swappable |
| 2026-09-15 | Window / cadence / lookback declared per source; compaction later | Idempotency needs a re-fetchable window; small files hurt server engines |
| 2026-09-15 | Go transforms write `bronze/derived/`, declared as dbt sources; two-pass `dbt build` | Derived is not raw; lineage stays honest; freshness catches dead transforms |
| 2026-09-15 | Temporal adopted in phase 1 (dev server) rather than deferred | Go-native; ticker Runner would be throwaway; the point is to learn it |
| 2026-09-16 | Source one is beads (Dolt, MySQL wire) | Real daily data, clean timestamps, serves history via `events` and `dolt_history_issues`; no plant sensor exists yet |
| 2026-09-16 | Phase 2 warehouse: Redshift (approved) vs self-hosted DuckDB lakehouse, under evaluation | Athena not available in org; DuckDB path keeps principle 4 pure and cost near zero if it holds at team scale |
| 2026-09-16 | Airflow dropped; Dagster not adopted | Airflow adds a Python layer over the Go core; nice-to-have only. Dagster's asset vocabulary borrowed |
| 2026-09-16 | No Metabase in phase 1; DuckDB UI for validation. Concurrent-read visualiser is a phase-2 decision | DuckDB single-writer constraint; community driver; validation is all phase 1 needs |
| 2026-09-16 | Phase-1 marts materialised as Parquet (dbt-duckdb `external`); `dev.duckdb` holds views only | Go transforms and the DuckDB UI read Parquet in their own processes, so principle 6 holds and the single-writer constraint never bites; it is a miniature of the phase-2 lakehouse |
| 2026-09-18 | Toolchain pinned: Go 1.25.5; dbt-core 1.12.5 with dbt-duckdb 1.11.0 installed via `uv tool install --python 3.12 dbt-core --with dbt-duckdb` (bundling duckdb 1.5.5); DuckDB CLI 1.5.5 and Temporal CLI 1.9.1 (server 1.32.0, UI 2.54.1) via Homebrew | Free, no accounts (principle 5); exact versions make dbt-duckdb and Temporal behaviour reproducible |
| 2026-09-18 | Go module set pinned (`go.mod` after `go mod tidy`): `github.com/parquet-go/parquet-go` v0.32.0, `github.com/go-sql-driver/mysql` v1.10.1, `go.temporal.io/sdk` v1.48.0, `go.temporal.io/sdk/contrib/opentelemetry` v0.8.1, and the `go.opentelemetry.io/otel` family (otel, otel/metric, otel/sdk, otel/sdk/metric, otel/trace, and the otlp/stdout exporters) at v1.46.0 | Pure Go, no CGO (principle 6); exact versions make the build reproducible |
| 2026-09-19 | Phase 1 accepted at Gate 5 and delivered; `main` is the running platform | 46 of 46 acceptance criteria satisfied on a blinded cold read, re-verified live by the reviewing session. Evidence: [`fable-streams/2026-09-18-phase-1-build/gate5-report.md`](fable-streams/2026-09-18-phase-1-build/gate5-report.md) |
| 2026-09-19 | Rows travel through a spool file, never a Temporal payload | The largest hourly window is about 6 MB against a 2 MB default limit; the claim-check keeps workflow history small and carries into phase 2 |
| 2026-09-21 | A push source reaches bronze through a landing zone, not by relaxing the window rules | SNS has no retention and cannot serve a past window. An SQS subscription plus a write-before-acknowledge Receiver restores re-readability, so bronze keeps one overwrite-on-rerun file per window and nothing downstream changes |
| 2026-09-21 | Zero-row windows are written as empty files; freshness on an ad hoc source is a liveness check | "Quiet" and "stopped" must stay distinguishable. An empty file carries no `_extracted_at`, so data freshness cannot be the alarm for a source that is idle by design |
| 2026-09-21 | `stg_beads__events` dedupes on the source id | `events.created_at` is naive Sydney wall time, so the DST fall-back hour is covered by two UTC windows. This is the sanctioned use of the staging-dedupe fallback |
| 2026-09-21 | The hand-written mermaid diagram is retired; the archify pair is the only picture | Two hand-maintained drawings of one architecture drift apart, and these already had |
