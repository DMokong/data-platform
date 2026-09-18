# Plan — phase 1 build

Spec: [`spec.md`](spec.md) (46 ACs). Design: [`design.md`](design.md). Epic `trk-bam`; one story per task.

Plan-doc weave: `superpowers:writing-plans` was read. Its file map, Interfaces (consumes / produces) blocks,
task right-sizing and type-consistency self-review are applied here. Its full-code-per-step format is not used:
it would pre-write the implementation that the adversarial waves exist to produce. The per-task `brief.md`
files are the contracts.

## Global constraints (every brief inherits these)

- Target checkout `/Users/dustincheng/projects/data-platform`, branch `phase-1`; no push (no remote).
- Go module `github.com/DMokong/data-platform`, `go 1.25`, `CGO_ENABLED=0` clean; no DuckDB in Go.
- Go writes only Parquet under `bronze/` (Writer) and spool files under `local/spool/`. dbt owns `warehouse/`.
- Ingestion never transforms; provenance columns only. The Dolt server is read-only (read-only transactions).
- Every Go package has `doc.go` with a line `// Data-engineering concept: …`.
- Never commit `bronze/`, `warehouse/`, `local/`, `.beads/`, `*.duckdb`, or any task `report.md` (the conductor
  commits stream evidence at wave boundaries).
- Only a task that lists `go.mod` / `go.sum` in its File scope may change them; nobody else runs `go get` or
  `go mod tidy`.
- Every `go get` runs as `GOTOOLCHAIN=local go get <module>@<exact version>`, using the F11 pins: parquet-go
  v0.32.0, mysql v1.10.1, Temporal SDK v1.48.0, contrib/opentelemetry v0.8.1, otel v1.46.0. An over-new
  dependency must fail loudly instead of silently switching toolchains.
- `dbt` owns `warehouse/`. The one allowed Go touch is `MkdirAll(<root>/warehouse/marts)` before invoking dbt
  (spec F5).
- Commits use `git commit -m … -- <paths>`, so parallel tasks never capture each other's staged files.
- Use `127.0.0.1`, never `localhost`, for Temporal and OTLP (spec F10).
- Commands marked **(long)** in a brief need the Bash tool timeout raised to 600000 ms. The live script caps
  itself below 9 minutes.
- Conductor step between waves 4 and 5: snapshot a full pre_go `run_results.json` to
  `local/fixtures/run_results_pre_go.json` for task 08, so task 07's builds in the same wave cannot clobber it.
- Live Go tests are gated on `BEADS_LIVE=1`. SQL keywords in Go source are uppercase.

## Tasks

| # | Task | Tier | testable | Depends on | Story | ACs |
|---|---|---|---|---|---|---|
| 01 | foundation — module, `internal/window`, `internal/record`, toolchain rows | standard | yes | — | trk-bam.1 | 01, 02, 45 (toolchain) |
| 02 | bronze Writer — `internal/bronze` (paths, Parquet, provenance, atomic overwrite) | judgment | yes | 01 | trk-bam.2 | 03–08 |
| 03 | beads Fetcher — `internal/source`, `internal/source/beads` | judgment | yes | 01 | trk-bam.3 | 09–14 |
| 04 | Runner + `cmd/ingest` (lookback, backfill, retries) | standard | yes | 02, 03 | trk-bam.4 | 15–19 |
| 05 | dbt-duckdb project — sources, staging, pre_go marts, tests, freshness | standard | no | 04 | trk-bam.5 | 20–27 |
| 06 | Temporal ingest — MaterialiseWindow / MaterialiseSource, spool, Schedule, `cmd/worker` | judgment | yes | 04 | trk-bam.6 | 33–36 |
| 07 | Go transform `issue_mentions` + `cmd/transform` + derived source + post_go mart | standard | yes | 05 | trk-bam.7 | 28–32 |
| 08 | OTel setup (`internal/telemetry`) + dbt adapter (`internal/dbt`) | standard | yes | 01 (+05 for a real fixture) | trk-bam.8 | 38, 39 |
| 09 | BuildMarts two-pass workflow + telemetry wiring + live script | judgment | yes | 06, 07, 08 | trk-bam.9 | 37, 39, 40 |
| 10 | integration — Makefile, README runbook, module pins, tidy, end-to-end | standard | no | 09 | trk-bam.10 | 41–46 |

`judgment` → opus implementer + reviewer. `standard` → sonnet. Test-authors are sonnet; verifiers are always
haiku.

## Waves

| Wave | Tasks | Execution | Why this shape |
|---|---|---|---|
| 1 | 01 | single | everything imports `window` / `record` |
| 2 | 02, 03 | serial (`parallel_safe: false`) | both add a module to `go.mod` / `go.sum` |
| — | test-adversary on 02 | 3 breakers | AC-08 is the test-suite AC |
| 3 | 04 | single | composes Writer + Fetcher |
| 4 | 05, 06 | serial (`parallel_safe: false`) | 06's live trigger rewrites bronze while 05's rebuild check compares row counts |
| — | conductor: snapshot `dbt/target/run_results.json` → `local/fixtures/run_results_pre_go.json` | — | task 08's fixture source |
| 5 | 07, 08 | **parallel** | disjoint scopes: 07 = transform + dbt post_go (no `go.mod`); 08 = telemetry + dbt adapter (owns `go.mod`); both verify package-scoped |
| 6 | 09 | single | touches orchestrator + worker, which 06 created |
| 7 | 10 | single | whole-repo checks and the end-to-end run |

## Interfaces (consumes → produces)

- **01 produces** `window.Grain{Hour,Day}`, `window.Window{Start,End,Grain}` (JSON tags `start,end,grain`),
  `window.Containing`, `window.Lookback`, `window.Range`, `Window.Validate`, `Window.String` (`2026-09-15T13` /
  `2026-09-15`); `record.Kind{String,Int64,Float64,Bool,Timestamp}`, `record.Column{Name,Kind}`,
  `record.Batch{Columns,Rows}`, `Batch.Validate`.
- **02 produces** `bronze.Zone{Raw,Derived}`, `bronze.Path(zone, dataset, w) (string, error)`,
  `bronze.Writer{Root}`, `(*Writer).Write(ctx, zone, dataset, w, batch, extractedAt) (bronze.Result, error)`,
  `bronze.Result{Path,Rows,Bytes}`, provenance-name constants.
- **03 produces** `source.Source{Name,Cadence,Tables}`, `source.Table{Name,Grain,Lookback,Shape}`,
  `source.Fetcher` interface `Fetch(ctx, table, w) (record.Batch, error)`, `beads.Config`,
  `beads.ConfigFromEnv`, `beads.Declaration()`, `beads.BuildQuery`, `beads.NewFetcher`.
- **04 produces** `runner.Runner{Source,Fetcher,Writer,Now,Attempts,Backoff,Concurrency,Log}`,
  `RunOnce(ctx, now)`, `Backfill(ctx, from, to)`, `runner.Summary`; CLI `cmd/ingest`.
- **05 produces** dbt models: 7 staging (`stg_beads__events`, `stg_beads__issue_snapshots`,
  `stg_beads__issue_history`, `stg_beads__commits`, `stg_beads__comment_snapshots`,
  `stg_beads__dependency_snapshots`, `stg_beads__label_snapshots`), 5 pre_go marts under
  `dbt/models/marts/pre_go/`, a `marts/post_go` folder config tagged `post_go`, and mart Parquet at
  `warehouse/marts/<model>/`.
- **06 produces** `orchestrator.TaskQueue = "data-platform"`, workflow names `MaterialiseSource`,
  `MaterialiseWindow`, activity names `FetchWindow`, `WriteWindow`, `DeleteSpool`, `orchestrator.Activities`
(declared in `activity_ingest.go`), `SpoolRef`,
  `orchestrator.Register`, `orchestrator.ApplySchedule`; `cmd/worker` (`-apply-schedule`, `-root`);
  `scripts/live-materialise.sh`.
- **07 produces** `transform.Contract`, `transform.Transform`, `transform.Env`, `transform.Materialise`,
  `catalog.All()` / `catalog.Lookup(name)` in `internal/transform/catalog`; CLI `cmd/transform`; dbt source
  `derived.issue_mentions` and mart `mart_issue_mention_drift`.
- **08 produces** `telemetry.Config`, `telemetry.ConfigFromEnv`, `telemetry.Setup`, `telemetry.RecordWrite`,
  `telemetry.RecordActivity`, `telemetry.EmitDbtResults`; `dbt.RunResults`, `dbt.ParseRunResults`,
  `dbt.Runner{Bin,ProjectDir,ProfilesDir,WorkDir}.Build(ctx, selector)`.
- **09 produces** workflow `BuildMarts`, activities `DbtBuild`, `RunTransform`; MaterialiseSource → BuildMarts;
  worker wired to OTel.

## Estimated agent count

Build waves: 38 first-round dispatches (8 test-authors, 10 implementers, 10 verifiers, 10 reviewers), about 5
for test-adversary, and about 12 more for fix rounds, so roughly **55**. Final audit: 1 spec-auditor plus a
5-voter haiku refute panel per AC (46 × 5 = 230). Total is about **285 agents**, about 240 of them haiku.
