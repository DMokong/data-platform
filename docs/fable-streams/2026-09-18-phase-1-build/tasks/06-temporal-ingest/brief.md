---
task: 06-temporal-ingest
parallel_safe: false
testable: true
tier: judgment
story: trk-bam.6
acs: [AC-33, AC-34, AC-35, AC-36]
---

# 06-temporal-ingest

## Goal

Move ingestion onto **Temporal** (spec §8, decision "Temporal adopted in phase 1").
- `MaterialiseWindow(source, window)` runs the Fetcher and the Writer as activities: `FetchWindow` then
  `WriteWindow`.
- `MaterialiseSource(source)` fans out one `MaterialiseWindow` child per lookback window.
- A Temporal **Schedule** at the source's declared cadence replaces any ticker.
- `cmd/worker` hosts all of it.

Activities pass a **spool file reference**, never row data: one hourly `dolt_history_issues` window is about 6 MB,
above Temporal's 2 MB payload limit (spec F3). This is the claim-check pattern. A live script proves the whole
path against the dev server. Task 09 later adds BuildMarts and telemetry on top of this.

## Done-check

```
cd /Users/dustincheng/projects/data-platform && go test -count=1 ./internal/orchestrator/... ./cmd/worker/... && bash scripts/live-materialise.sh
```

The done-check passes when the tests pass and the script's last line is `LIVE-MATERIALISE: PASS` with exit 0.

## File scope

**Working-directory contract.** The target checkout is `/Users/dustincheng/projects/data-platform` (no
worktree), and the expected branch is `phase-1`. Before any write, run:

    cd /Users/dustincheng/projects/data-platform && [ "$(git rev-parse --show-toplevel)" = "/Users/dustincheng/projects/data-platform" ] && [ "$(git rev-parse --abbrev-ref HEAD)" = "phase-1" ] && echo CWD-OK

If it does not print `CWD-OK`, write nothing and escalate `broken_harness`.

Create:
- `internal/orchestrator/doc.go`
- `internal/orchestrator/workflow_materialise.go`: **all workflow functions of this task live in files named
  `workflow_*.go`**.
- `internal/orchestrator/activity_ingest.go`: activities live in `activity_*.go`.
- `internal/orchestrator/spool.go`, `internal/orchestrator/schedule.go`, `internal/orchestrator/register.go`
- tests: `internal/orchestrator/*_test.go`
- `cmd/worker/doc.go`, `cmd/worker/main.go`
- `scripts/live-materialise.sh`

Modify:
- `go.mod`, `go.sum`: add the SDK with `GOTOOLCHAIN=local go get go.temporal.io/sdk@v1.48.0`, plus the SDK
  packages you import. **Not v1.49.0**: it requires Go 1.26 (spec F11). This task owns them in its wave; task 05, running before you in this wave, touches no Go.

Local, uncommitted artefacts: `local/temporal-live.db`, `local/logs/`, `local/bin/`, `local/run/`,
`local/spool/`, `bronze/`.

**Git.** Commit when verification passes, and at the end of every fix round.
- Stage only paths inside this File scope, with `git add -- <paths>`. Then commit with
  `git commit -m "<message>" -- <paths>`, so the commit contains only your paths even if a sibling task has
  staged files in the shared index. Never use `git add -A`, `git add .` or `git commit -a`.
- Never commit this task's `report.md`, anything under `bronze/`, `warehouse/`, `local/` or `.beads/`, or any
  `*.duckdb` file.
- Commit message: `<type>(<scope>): <summary>`, a blank line, `Stream-task: 2026-09-18-phase-1-build/06-temporal-ingest round <N>`,
  a blank line, then `Co-Authored-By: Claude <your model name, e.g. Opus 5> <noreply@anthropic.com>`.
- If git reports an `index.lock`, wait 5 s and retry, at most 6 times. Do not push.

## Required content

**Names** (task 09 and the runbook depend on them):

```go
const TaskQueue = "data-platform"
const (
    WorkflowMaterialiseSource = "MaterialiseSource"
    WorkflowMaterialiseWindow = "MaterialiseWindow"
    ActivityFetchWindow       = "FetchWindow"
    ActivityWriteWindow       = "WriteWindow"
    ActivityDeleteSpool       = "DeleteSpool"
)
type MaterialiseSourceInput struct { Source string }
type MaterialiseWindowInput struct { Source string; Window window.Window }
type FetchWindowInput      struct { Source, Table string; Window window.Window }
type SpoolRef struct {
    Path      string        // absolute path of the spool file under <root>/local/spool/
    Source    string
    Table     string
    Window    window.Window
    Rows      int
    FetchedAt time.Time     // UTC, the instant FetchWindow started reading the source → becomes _extracted_at
}
type Activities struct {
    Sources  map[string]source.Source  // declarations by source name
    Fetchers map[string]source.Fetcher
    Writer   *bronze.Writer
    SpoolDir string                    // <root>/local/spool
    Now      func() time.Time          // nil → time.Now
}
func (a *Activities) FetchWindow(ctx context.Context, in FetchWindowInput) (SpoolRef, error)
func (a *Activities) WriteWindow(ctx context.Context, ref SpoolRef) (bronze.Result, error)
func (a *Activities) DeleteSpool(ctx context.Context, ref SpoolRef) error // idempotent: a missing file is success
func MaterialiseWindow(ctx workflow.Context, in MaterialiseWindowInput) (MaterialiseWindowResult, error)
func MaterialiseSource(ctx workflow.Context, in MaterialiseSourceInput) (MaterialiseSourceResult, error)
func Register(r worker.Registry, a *Activities)                                   // registers with the explicit names above
func ApplySchedule(ctx context.Context, c client.Client, src source.Source) error // schedule id "materialise-<name>"
```

Declare `Activities` in `activity_ingest.go`: task 09 extends it there. Result types are yours. They should
carry per-table rows and bytes so the runbook can show them.

**Workflows** (deterministic).
- Use `workflow.Now`, `workflow.Go` / futures and `workflow.Sleep` only. No `time.Now`, `time.Sleep`, native
  `go` statements, `math/rand`, `os` or network calls in any `workflow_*.go`.
- The source declaration is static data, so reading it in workflow code is fine; expose a
  `map[string]source.Source` built from `beads.Declaration()`.
- `MaterialiseWindow`:
  - for each table of the source whose `Grain` equals `in.Window.Grain`, in declaration order, run `FetchWindow`,
    then `WriteWindow`, then `DeleteSpool`, with tables in parallel or sequentially, your choice;
  - `FetchWindow` activity options: StartToClose 5 min, HeartbeatTimeout 1 min, and a RetryPolicy (initial 2 s,
    backoff 2.0, max 5 attempts);
  - `WriteWindow`: StartToClose 2 min, HeartbeatTimeout 1 min, the same retry policy. `DeleteSpool`:
    StartToClose 30 s, the same retry policy.
- `MaterialiseSource`:
  - for each grain present in the declaration, taken in `Declaration().Tables` order, compute `window.Lookback(workflow.Now(ctx), grain,
    <that grain's lookback>)`. All tables of one grain share a lookback in the beads declaration; if they ever
    differ, use the maximum and document it. **Never range over a map to decide child-start order**: map order
    is random and breaks replay determinism;
  - start one `MaterialiseWindow` child per window, with workflow id `materialise/<source>/<grain>/<window>`
    (e.g. `materialise/beads/hour/2026-09-15T13`) and reuse policy allow-duplicate;
  - wait for all children, and fail with an error naming the failed children if any fail;
  - return totals.

**Activities.**
- `FetchWindow`:
  - `FetchedAt := a.Now().UTC()`, then `Fetch`;
  - write the batch to a spool file at
    `<SpoolDir>/<source>/<table>/<window>-<workflow run id>-<activity id>.<ext>` with a lossless encoding of your
    choice (e.g. `encoding/gob` with the needed `gob.Register` calls). Write it atomically, as a temp file in the
    same directory renamed into place, then return the `SpoolRef`;
  - heartbeat at stage boundaries (before the query, after the rows are read, after the spool is written). No
    tickers anywhere: AC-35 greps for them.
- `WriteWindow`:
  - read the spool, then `Writer.Write(ctx, bronze.Raw, <source>/<table>, ref.Window, batch, ref.FetchedAt)`;
  - does **not** delete the spool: the workflow runs `DeleteSpool` after `WriteWindow` succeeds. A retried
    `WriteWindow` whose earlier completion was lost therefore still finds its spool;
  - retries stay idempotent: the same `FetchedAt` gives byte-identical output.
- Limit concurrent activities on the worker (e.g. `MaxConcurrentActivityExecutionSize: 8`) so Dolt is not
  flooded.

**Schedule (`ApplySchedule`).**
- Schedule id `materialise-<source>`, interval = `src.Cadence` (15 min), overlap policy Skip, and
  **`CatchupWindow` 10 s**, the server minimum. The default of one year replays a missed run the moment a dev
  server restarts, and that run then swallows the live script's trigger under Skip.
- Action: start workflow `MaterialiseSource` with `MaterialiseSourceInput{Source: src.Name}` on `TaskQueue`, with
  workflow id `materialise-source-<source>`. The server appends the scheduled time.
- Upsert: create it, or update the existing one if it already exists. Calling it twice must succeed.

**`cmd/worker`.**
- Flags:
  - `-root` (repo root, default `.`);
  - `-apply-schedule`: upsert the schedule for every declared source and **exit 0** without starting a worker.
- Env: `TEMPORAL_ADDRESS` (default **`127.0.0.1:7233`**, not `localhost`: see spec F10, where `localhost`
  resolves to `::1` and gRPC hangs), `TEMPORAL_NAMESPACE` (default `default`); beads config via
  `beads.ConfigFromEnv()`.
- Without `-apply-schedule` it runs the worker on `TaskQueue` until SIGINT / SIGTERM (`worker.InterruptCh()`),
  then closes the Fetcher and the client.

**Tests (`go test ./internal/orchestrator/...`).** Use `go.temporal.io/sdk/testsuite`.
- `MaterialiseWindow`, hourly window with mocked activities: exactly the 3 hourly tables are fetched and
  written, and each `WriteWindow` receives its own `FetchWindow`'s `SpoolRef`.
- `MaterialiseWindow`, daily window: the 4 snapshot tables.
- `MaterialiseSource`: with `env.SetStartTime(2026-09-15T00:30Z)`, children are started for exactly 24 hourly
  and 2 daily windows with the expected ids (mock the child workflow).
- `MaterialiseSource` fails when a child fails.
- A spool round-trip is lossless for every `record.Kind`, including NULL and `Timestamp` wall fields.
- `FetchWindow` + `WriteWindow` + `DeleteSpool` through `TestActivityEnvironment`, with a fake Fetcher and temp
  dirs. Assert that:
  - the bronze file exists and `_extracted_at` equals `FetchedAt`;
  - the spool survives `WriteWindow` and is gone after `DeleteSpool`;
  - a second `DeleteSpool` succeeds;
  - running `WriteWindow` twice on one ref gives byte-identical bronze files.

**`scripts/live-materialise.sh`** (bash, `set -euo pipefail`, run from anywhere; it `cd`s to the repo root).
Script-wide rules:
- `export TEMPORAL_ADDRESS=127.0.0.1:7233` so every `temporal` CLI call and the worker avoid the `localhost`
  / `::1` hang (F10).
- Give every CLI call a `--command-timeout`.
- Prefer blocking waits (`temporal workflow result --workflow-id …`) over sleep loops. A short `sleep` *inside*
  the script is fine. The agent harness may refuse a bare foreground `sleep` typed as its own tool command, but
  not one inside a script.

It must:
0. Kill any stale worker recorded in `local/run/worker.pid` from an earlier run. Install
   `trap cleanup EXIT INT TERM HUP`.
1. If nothing listens on 127.0.0.1:7233, the script **owns** the server. It deletes `local/temporal-live.db*`
   and starts `temporal server start-dev --ip 127.0.0.1 --db-filename local/temporal-live.db` in the background,
   logging to `local/logs/temporal.log` and writing its pid to `local/run/temporal.pid`, then waits until
   healthy (timeout 60 s). The fresh DB means no stale runs and no catch-up. If a server is already listening,
   the script uses it but does not own it.
2. Build `local/bin/worker` from `./cmd/worker` and run `local/bin/worker -apply-schedule`. Start the worker in
   the background, logging to `local/logs/worker.log`, with its pid in `local/run/worker.pid`. If the server is
   not owned, wait up to 3 min for any Running `MaterialiseSource` / `BuildMarts` executions to close (the new
   worker processes them), and fail with a clear reason otherwise.
3. Record `T` (UTC now) and run `temporal schedule trigger --schedule-id materialise-beads`.
4. Fail fast if no `MaterialiseSource` execution started at or after `T` appears within 30 s. Otherwise wait,
   with a timeout of at most 6 min, until it is Completed, and fail fast if it Failed. The whole script must
   finish in under 9 min.
5. Print `temporal schedule describe --schedule-id materialise-beads` output, trimmed to the lines showing id,
   interval and overlap policy.
6. Check with DuckDB that `max(_extracted_at)` over `bronze/raw/beads/issues/*/*.parquet` is later than `T`.
7. On exit, via the trap: stop the worker (SIGTERM, then SIGKILL after 10 s), stop the dev server **only if the
   script owns it**, and remove the pid files.
8. Print `LIVE-MATERIALISE: PASS` and exit 0, or `LIVE-MATERIALISE: FAIL <reason>` and exit 1.

Structure the script as small functions: task 09 extends it with mart and span checks.

`doc.go` for each package includes a line starting `// Data-engineering concept: `:
- `orchestrator`: durable, replayable orchestration (activities, workflows, schedules, claim-check);
- `cmd/worker`: the long-lived worker process.

## Inputs

- `/Users/dustincheng/projects/data-platform/docs/data-platform-spec.md` (§2 Triggering; §8 Orchestrator; the
  decision log)
- `/Users/dustincheng/projects/data-platform/docs/review-2026-09-15.html` (Orchestrator section; build-order
  step 5). Text only; skim with `sed -e 's/<[^>]*>//g'`.
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/spec.md` (F3, §3,
  AC-33 to AC-36)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/plan.md`
- `/Users/dustincheng/projects/data-platform/internal/window/window.go`,
  `/Users/dustincheng/projects/data-platform/internal/record/record.go`,
  `/Users/dustincheng/projects/data-platform/internal/bronze/writer.go`,
  `/Users/dustincheng/projects/data-platform/internal/source/source.go`,
  `/Users/dustincheng/projects/data-platform/internal/source/beads/declaration.go`,
  `/Users/dustincheng/projects/data-platform/internal/runner/runner.go` (for how lookback is computed today)

## Verification commands

```
cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/orchestrator cmd/worker)" && echo GOFMT-OK
cd /Users/dustincheng/projects/data-platform && go vet ./internal/orchestrator/... ./cmd/worker/...
cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/orchestrator/... ./cmd/worker/... 2>&1 | tail -40
cd /Users/dustincheng/projects/data-platform && echo "ticker-lines: $(grep -rnE 'time\.(NewTicker|Tick)\(' cmd internal | wc -l | tr -d ' ')"
cd /Users/dustincheng/projects/data-platform && echo "nondeterminism-lines: $(ls internal/orchestrator/workflow_*.go | grep -v '_test.go' | xargs grep -nE 'time\.Now\(|time\.Sleep\(|go func|^[[:space:]]*go [a-zA-Z]|"math/rand"|"os"|"net/' | wc -l | tr -d ' ')"
cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./... && ! go list -deps ./... | grep -qi duckdb && echo NO-CGO-NO-DUCKDB
cd /Users/dustincheng/projects/data-platform && bash scripts/live-materialise.sh 2>&1 | tail -25
cd /Users/dustincheng/projects/data-platform && grep -E '^(go|toolchain) ' go.mod
cd /Users/dustincheng/projects/data-platform && grep -n 'go.temporal.io/sdk' go.mod && git log --oneline -3 && git status --short
```

The `go test` and live-script commands are **(long)**: run them with the Bash tool timeout at 600000 ms.

Expected results:
- `ticker-lines: 0` and `nondeterminism-lines: 0`.
- The script's last line is `LIVE-MATERIALISE: PASS`.
- `go.temporal.io/sdk v1.48.0`.
- The go directive is `go 1.25` or `go 1.25.x` (x ≤ 5), with no `toolchain` line.

## Report obligation

Append `## implementer — round <N>` to `tasks/06-temporal-ingest/report.md`. Include:
- the files changed;
- the spool encoding choice;
- the activity options and why;
- wall time of the live run;
- each verification command with its exit code and output tail (≤30 lines);
- the commit SHA.

## Tracker

Story **trk-bam.6**. Run `bd` from `/Users/dustincheng/projects/data-platform`.
- Before your first edit: `bd update trk-bam.6 --claim --actor claudeclaw`
- Once per round, after committing and before your final response:
  `bd comment trk-bam.6 --actor claudeclaw "<round N: what landed + commit SHA, or what stopped you>"`
- Never close the story, and never add or remove labels (never `agent`, never `human`).

## Out of scope

- `BuildMarts`, dbt execution, Go transforms and OpenTelemetry (tasks 08 / 09).
- Changing `internal/runner` or `cmd/ingest`: the CLI backfill stays a plain Runner command.
- Deleting `bronze/`.
- Leaving a dev server or worker running after the script exits.
- Editing any file under `dbt/`.
