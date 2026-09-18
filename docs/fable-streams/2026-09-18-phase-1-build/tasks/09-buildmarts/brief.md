---
task: 09-buildmarts
parallel_safe: true
testable: true
tier: judgment
story: trk-bam.9
acs: [AC-37, AC-39, AC-40]
---

# 09-buildmarts

## Goal

Complete the orchestrator.
- Add the `BuildMarts(window)` workflow (spec §8). It runs `dbt build --select tag:pre_go`, then every
  registered Go transform as its own activity, then `dbt build --select tag:post_go`.
- Make `MaterialiseSource` start `BuildMarts` only after all ingestion children succeed.
- Wire OpenTelemetry into the worker: Temporal's OTel tracing interceptor and metrics handler, the platform
  counters and histogram from `internal/telemetry`, and dbt node spans from `run_results.json`.
- Extend the live script so a single schedule trigger proves ingest → marts → spans end to end.

## Done-check

```
cd /Users/dustincheng/projects/data-platform && go test -count=1 ./... && bash scripts/live-materialise.sh
```

The done-check passes when all tests pass and the script's last line is `LIVE-MATERIALISE: PASS`, with the new
mart and span checks included.

## File scope

**Working-directory contract.** The target checkout is `/Users/dustincheng/projects/data-platform` (no
worktree), and the expected branch is `phase-1`. Before any write, run:

    cd /Users/dustincheng/projects/data-platform && [ "$(git rev-parse --show-toplevel)" = "/Users/dustincheng/projects/data-platform" ] && [ "$(git rev-parse --abbrev-ref HEAD)" = "phase-1" ] && echo CWD-OK

If it does not print `CWD-OK`, write nothing and escalate `broken_harness`.

Create:
- `internal/orchestrator/workflow_buildmarts.go`, `internal/orchestrator/activity_buildmarts.go`
- `internal/orchestrator/workflow_buildmarts_test.go`, `internal/orchestrator/activity_buildmarts_test.go`

Modify:
- `internal/orchestrator/workflow_materialise.go`: start the BuildMarts child
- `internal/orchestrator/activity_ingest.go`: telemetry calls
- `internal/orchestrator/register.go`, `internal/orchestrator/doc.go`
- existing `internal/orchestrator/*_test.go`, where the new child changes expectations
- `cmd/worker/main.go`, `cmd/worker/doc.go`
- `scripts/live-materialise.sh`
- `go.mod`, `go.sum`: add the contrib module with
  `GOTOOLCHAIN=local go get go.temporal.io/sdk/contrib/opentelemetry@v0.8.1`. It requires SDK ≥ v1.46.0; the SDK
  is at v1.48.0 (spec F11). Do not upgrade the SDK.

**Git.** Commit when verification passes, and at the end of every fix round.
- Stage only paths inside this File scope, with `git add -- <paths>`. Then commit with
  `git commit -m "<message>" -- <paths>`, so the commit contains only your paths even if a sibling task has
  staged files in the shared index. Never use `git add -A`, `git add .` or `git commit -a`.
- Never commit this task's `report.md`, anything under `bronze/`, `warehouse/`, `local/` or `.beads/`, or any
  `*.duckdb` file.
- Commit message: `<type>(<scope>): <summary>`, a blank line, `Stream-task: 2026-09-18-phase-1-build/09-buildmarts round <N>`,
  a blank line, then `Co-Authored-By: Claude <your model name, e.g. Opus 5> <noreply@anthropic.com>`.
- If git reports an `index.lock`, wait 5 s and retry, at most 6 times. Do not push.

## Required content

**Names:**

```go
const (
    WorkflowBuildMarts   = "BuildMarts"
    ActivityDbtBuild     = "DbtBuild"
    ActivityRunTransform = "RunTransform"
)
type BuildMartsInput    struct { Window window.Window } // a Day window
type DbtBuildInput      struct { Selector string }       // "tag:pre_go" | "tag:post_go"
type DbtBuildResult     struct { Selector string; Nodes int; Failed []string } // plus whatever else is useful
type RunTransformInput  struct { Name string; Window window.Window }
func BuildMarts(ctx workflow.Context, in BuildMartsInput) (BuildMartsResult, error)
```

The `Activities` struct gains the dependencies these need: a `dbt.Runner`, the transform `Env`, and the Writer
it already has.

**`BuildMarts`** (in `workflow_buildmarts.go`, deterministic; the same rules as task 06):
1. Run `DbtBuild{Selector: "tag:pre_go"}`.
2. Then, for each `catalog.All()` transform in catalog order, run `RunTransform{Name, Window}`. The catalog is
   static, so reading it in workflow code is deterministic.
3. Then run `DbtBuild{Selector: "tag:post_go"}`.

A failure at any step fails the workflow; nothing after the failed step runs.
- `DbtBuild` options: StartToClose 15 min, at most 2 attempts.
- `RunTransform` options: StartToClose 5 min, the same retry policy as the ingest activities.

**Activities** (in `activity_buildmarts.go`).
- `DbtBuild`:
  - calls `dbt.Runner.Build(ctx, selector)`, then `telemetry.EmitDbtResults(ctx, selector, rr)`;
  - on dbt failure returns `temporal.NewNonRetryableApplicationError(msg, "DbtBuildFailed", err, failedNodeNames)`,
    whose message names the failed nodes;
  - heartbeats are optional, and there are no tickers.
- `RunTransform`:
  - looks the name up in `catalog.Lookup` and calls
    `transform.Materialise(ctx, t, env, writer, window, a.Now().UTC())`;
  - calls `telemetry.RecordWrite(ctx, "go_transform", <output>, "derived", rows, bytes)`.
- Every activity, the ingest ones included, calls `telemetry.RecordActivity(ctx, <name>, elapsed, err)`.
  `WriteWindow` also calls `telemetry.RecordWrite(ctx, source, table, "raw", rows, bytes)`.

**`MaterialiseSource`.** After every `MaterialiseWindow` child succeeds, start child `BuildMarts` with
`window.Containing(workflow.Now(ctx), window.Day)`, a deterministic id `build-marts/<source>/<day>`, and reuse
policy allow-duplicate. Its result is part of the returned result. If any ingestion child failed, BuildMarts is
not started.

**Worker (`cmd/worker/main.go`).**
1. Call `telemetry.Setup(ctx, telemetry.ConfigFromEnv("data-platform-worker"))` first, and defer shutdown with a
   timeout.
2. Create the Temporal client with the contrib OTel tracing interceptor (`opentelemetry.NewTracingInterceptor`)
   and `opentelemetry.NewMetricsHandler` on the global meter.
3. Build `dbt.Runner{Bin: env PLATFORM_DBT_BIN or "dbt", WorkDir: <root>}` and
   `transform.Env{WarehouseRoot: <root>/warehouse}`, and register everything.

`-apply-schedule` behaves exactly as before.

**Tests** (testsuite, mocked activities):
- `BuildMarts` calls pre_go → each catalog transform → post_go, in that order;
- a failing pre_go `DbtBuild` means no `RunTransform` and no post_go call;
- a failing `RunTransform` means no post_go;
- `MaterialiseSource` starts `BuildMarts` after successful children, with the expected id and Day window, and
  does not start it when a child fails (update task 06's tests accordingly);
- a `DbtBuild` activity test with a fake `dbt.Runner` binary (reuse `internal/dbt/testdata/fake-dbt.sh`) returns
  a non-retryable application error on failure;
- an activity test with an in-memory meter shows `WriteWindow` recording `platform.rows_written`.

**`scripts/live-materialise.sh`** (extend it; keep everything task 06 checks):
- Run the worker with `PLATFORM_OTEL_EXPORTER=stdout`.
- After the MaterialiseSource run completes, also require that:
  - a `BuildMarts` child of that run completed;
  - `warehouse/marts/dim_issues` and `warehouse/marts/mart_issue_mention_drift` hold Parquet files modified
    after `T`;
  - `bronze/derived/issue_mentions/dt=<today UTC>/part-0.parquet` exists;
  - the worker log contains span names for `MaterialiseSource`, `MaterialiseWindow`, `FetchWindow`,
    `WriteWindow`, `DbtBuild` and `RunTransform`, plus at least one `dbt.model.`. Print the counts found.
- Stop the worker with SIGTERM **before** grepping, so telemetry shutdown flushes the stdout exporter.
- `mkdir -p warehouse/marts` before the trigger.
- Keep task 06's safeguards: owned-server fresh DB, stale-worker kill, trap on EXIT / INT / TERM / HUP, the
  30 s no-run fail-fast, and a total under 9 min. Raise the completion wait only as far as that cap allows,
  since BuildMarts adds two dbt builds.

## Inputs

- `/Users/dustincheng/projects/data-platform/docs/data-platform-spec.md` (§8 Orchestrator; §10 Observability)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/spec.md` (AC-33 to
  AC-40)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/plan.md`
- All of `/Users/dustincheng/projects/data-platform/internal/orchestrator/` and
  `/Users/dustincheng/projects/data-platform/cmd/worker/`
- `/Users/dustincheng/projects/data-platform/scripts/live-materialise.sh`
- `/Users/dustincheng/projects/data-platform/internal/telemetry/` and
  `/Users/dustincheng/projects/data-platform/internal/dbt/`: the exported APIs
- `/Users/dustincheng/projects/data-platform/internal/transform/transform.go` and
  `/Users/dustincheng/projects/data-platform/internal/transform/catalog/catalog.go`
- The stream reports of tasks 06, 07 and 08 (`tasks/0{6,7,8}-*/report.md`), for what already exists and how
  it behaved live

## Verification commands

```
cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l .)" && echo GOFMT-OK
cd /Users/dustincheng/projects/data-platform && go vet ./...
cd /Users/dustincheng/projects/data-platform && go test -count=1 ./... 2>&1 | tail -30
cd /Users/dustincheng/projects/data-platform && go test -count=1 -v -run 'BuildMarts|MaterialiseSource' ./internal/orchestrator/ 2>&1 | tail -30
cd /Users/dustincheng/projects/data-platform && echo "ticker-lines: $(grep -rnE 'time\.(NewTicker|Tick)\(' cmd internal | wc -l | tr -d ' ')"; echo "nondeterminism-lines: $(ls internal/orchestrator/workflow_*.go | grep -v '_test.go' | xargs grep -nE 'time\.Now\(|time\.Sleep\(|go func|^[[:space:]]*go [a-zA-Z]|"math/rand"|"os"|"net/' | wc -l | tr -d ' ')"
cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./... && ! go list -deps ./... | grep -qi duckdb && echo NO-CGO-NO-DUCKDB
cd /Users/dustincheng/projects/data-platform && bash scripts/live-materialise.sh 2>&1 | tail -30
cd /Users/dustincheng/projects/data-platform && grep -E '^(go|toolchain) ' go.mod
cd /Users/dustincheng/projects/data-platform && git log --oneline -3 && git status --short
```

The `go test ./...` and live-script commands are **(long)**: run them with the Bash tool timeout at 600000 ms.

Expected results: `ticker-lines: 0`, `nondeterminism-lines: 0`, and the script's last line is
`LIVE-MATERIALISE: PASS`, with the mart, derived-file and span checks shown.

## Report obligation

Append `## implementer — round <N>` to `tasks/09-buildmarts/report.md`. Include:
- the files changed;
- the contrib/opentelemetry version;
- the span names actually observed in the stdout export;
- the wall time of the live run;
- each verification command with its exit code and output tail (≤30 lines);
- the commit SHA.

## Tracker

Story **trk-bam.9**. Run `bd` from `/Users/dustincheng/projects/data-platform`.
- Before your first edit: `bd update trk-bam.9 --claim --actor claudeclaw`
- Once per round, after committing and before your final response:
  `bd comment trk-bam.9 --actor claudeclaw "<round N: what landed + commit SHA, or what stopped you>"`
- Never close the story, and never add or remove labels (never `agent`, never `human`).

## Out of scope

- Changing `internal/telemetry`, `internal/dbt`, `internal/transform/**`, dbt models, the Runner, the Fetcher or
  the Writer. If one is wrong, escalate `plan_invalidating_discovery` with evidence.
- A second Schedule.
- Grafana dashboards.
- The Makefile or README (task 10).
