## implementer — round 1

**Status: done.** Commit `43eccfd` on `phase-1` (16 paths, all inside the File scope; tree clean afterwards). The CWD contract printed `CWD-OK`. `bd update trk-bam.9 --claim --actor claudeclaw` succeeded before the first edit (`✓ Updated issue: trk-bam.9`), and the round-1 comment is posted (`✓ Comment added to trk-bam.9`).

### State on arrival

`report.md` was empty (0 bytes), so there was no test-author section. The test-author's work was already on disk and uncommitted: new `internal/orchestrator/{workflow_buildmarts_test.go, activity_buildmarts_test.go}`; modified `{activity_ingest_test.go, register_test.go, workflow_materialise_test.go}`; and `go.mod`/`go.sum` (testify v1.12.1 `h1:` sums and `go.yaml.in/yaml/v3 v3.0.5 // indirect`). I implemented against those tests without changing them and committed them with my code. The test-author assumed the field names `DbtRunner` and `TransformEnv`, and I used exactly those.

### Files

| Path | Change and why |
|---|---|
| `internal/orchestrator/workflow_buildmarts.go` (new) | `BuildMartsInput`, `TransformResult`, `BuildMartsResult{Window, PreGo, Transforms, PostGo}`, and `dbtBuildOptions` (StartToClose 15 min, `MaximumAttempts: 2`). `transformOptions` is StartToClose 5 min with the shared ingest `retryPolicy`. `BuildMarts` runs `DbtBuild{tag:pre_go}`, then `RunTransform{Name, Window}` for each `catalog.All()` entry in order, one at a time, then `DbtBuild{tag:post_go}`. It returns at the first error, so no later step runs. A malformed window or a non-Day window is a non-retryable `BadInput`. |
| `internal/orchestrator/activity_buildmarts.go` (new) | `DbtBuildInput`, `DbtBuildResult{Selector, InvocationID, Nodes, Statuses, Failed}`, `RunTransformInput`. `DbtBuild` calls `DbtRunner.Build`, then `telemetry.EmitDbtResults` (also after a failed build). On failure it returns `temporal.NewNonRetryableApplicationError("dbt build --select S failed for N node(s): <unique ids>", "DbtBuildFailed", err, failedIDs)`. `RunTransform` looks the name up with `catalog.Lookup`, checks the window's grain against the contract, runs `transform.Materialise(ctx, t, env, writer, w, a.now().UTC())` and calls `telemetry.RecordWrite(ctx, "go_transform", <Output>, "derived", rows, bytes)`. It has no heartbeats and no tickers. |
| `internal/orchestrator/activity_ingest.go` | `Activities` gains `DbtRunner dbt.Runner` and `TransformEnv transform.Env`. A new `recordActivity` is deferred in all 5 activities, over named error results, and calls `telemetry.RecordActivity(ctx, <name>, elapsed, err)`. `WriteWindow` also calls `telemetry.RecordWrite(ctx, source, table, "raw", rows, bytes)`. `a.now()` replaces the inline nil-clock fallback. |
| `internal/orchestrator/workflow_materialise.go` | `MaterialiseSourceResult.BuildMarts`. Once every child has succeeded, `MaterialiseSource` starts child `BuildMarts` with `window.Containing(now, window.Day)`, id `build-marts/<source>/<day>` and `ALLOW_DUPLICATE`, and waits for it. If BuildMarts fails, the run fails. If any child failed, the function returns before BuildMarts is started. |
| `internal/orchestrator/register.go` | Constants `WorkflowBuildMarts`, `ActivityDbtBuild`, `ActivityRunTransform`, and their registrations. |
| `internal/orchestrator/doc.go` | Package doc now covers the two-pass build and telemetry. |
| `cmd/worker/main.go`, `cmd/worker/doc.go` | Worker mode, in this order: `telemetry.Setup(ctx, telemetry.ConfigFromEnv("data-platform-worker"))` (with `Stdout` = the run's stdout), then a deferred shutdown with a 5 s timeout. Next come `temporalotel.NewTracingInterceptor(TracerOptions{})` in `client.Options.Interceptors` and `temporalotel.NewMetricsHandler(...)` on the global meter. Then `dbt.Runner{Bin: $PLATFORM_DBT_BIN or "dbt", WorkDir: <root>}` and `transform.Env{WarehouseRoot: <root>/warehouse}`. `-apply-schedule` uses the same code path as before. |
| `scripts/live-materialise.sh` | Worker runs with `PLATFORM_OTEL_EXPORTER=stdout`. `mkdir -p warehouse/marts` happens in `prepare_dirs`, which runs before the trigger. New steps 7-10: the BuildMarts child of this run (queried by `ParentWorkflowId`/`ParentRunId`) is Completed; the Parquet in `dim_issues` and `mart_issue_mention_drift` is newer than T; `bronze/derived/issue_mentions/dt=<day>/part-0.parquet` exists and is newer than T; the worker is stopped with SIGTERM and must exit 0 on its own; then the span counts are printed and checked. |
| `go.mod`, `go.sum` | Added with `GOTOOLCHAIN=local go get go.temporal.io/sdk/contrib/opentelemetry@v0.8.1` (`go: added go.temporal.io/sdk/contrib/opentelemetry v0.8.1`). The SDK stays at v1.48.0 and otel at v1.46.0. I moved the contrib line from `// indirect` to the direct block, because `cmd/worker` imports it. I did not run `go mod tidy`: it would also drop pre-existing stale lines (go-spew, go-difflib, yaml.v3), which belongs to task 10's tidy. |
| `internal/orchestrator/*_test.go` (5 files) | The test-author's files, committed unchanged. |

**contrib/opentelemetry version: v0.8.1** (its go.mod requires `go.temporal.io/sdk v1.46.0`, `otel v1.43.0`; MVS keeps SDK v1.48.0 / otel v1.46.0).

### Judgment calls

1. **`-apply-schedule` sets up no telemetry.** The brief requires `-apply-schedule` to behave "exactly as before". With telemetry on, the tracing interceptor would emit a `CreateSchedule` span. Under the default `otlp` exporter with no collector running, shutdown would also block for up to its timeout, and the command would print an error. So `run` parses the flags and, for `-apply-schedule`, dials a plain client as before. Worker mode calls `telemetry.Setup` first, before the client is dialled. The live script shows the `-apply-schedule` output line unchanged.
2. **The BuildMarts day comes from the instant MaterialiseSource started.** That is the `now := workflow.Now(ctx)` already taken for the lookback windows, not a second `workflow.Now` read after the children finish. A run that crosses midnight UTC therefore builds for the day it started in, which matches T in the live script.
3. **DbtBuild only stays retryable when its own context ended** (`ctx.Err() != nil`: a timeout or worker shutdown). Every other build error is `DbtBuildFailed` and non-retryable. When no node failed (for example dbt could not run at all), the message carries the underlying error instead of a node list.
4. **RunTransform rejects bad input without retrying.** An unknown name, a malformed window or a wrong-grain window returns a non-retryable `BadInput`. An empty `WarehouseRoot` or a nil Writer returns `Setup`. `DbtBuild` also refuses an empty `DbtRunner.WorkDir`, so dbt never runs relative to the worker's own working directory.
5. **`recordActivity` treats a panicking activity as a failure and re-panics**, so a panic is never recorded as `outcome=success`. I checked the recover and re-panic pattern with a standalone program in the scratchpad: outcomes `ok→success`, `fail→failure`, `panic→failure`, and the caller still recovered `kaboom`.
6. **Metrics handler `OnError`.** It logs through slog instead of the contrib default, which panics: a metric error should not kill the worker.
7. **Script budget.** `MAIN_BUDGET` goes from 480 to 500 s and `COMPLETE_TIMEOUT` from 360 to 420 s. The completion wait is `min(420, remaining − POST_RESERVE=45)`. The quiet wait now leaves 120 s instead of 60. Worst case is 500 + one 10 s CLI call + 20 s trap = 530 s, under 540. The derived-file check also requires the file to be newer than T; the brief only asked that it exist, so this is stricter.

### Verification (brief's commands, run in order after the final edit; exit codes captured with `echo exit=$?`)

1. `test -z "$(gofmt -l .)" && echo GOFMT-OK` → **exit 1, caused by a file that is not mine**:
```
--- gofmt -l . lists:
local/tmp/tcheck/main.go
--- gofmt over tracked + new task files:
GOFMT-OK-REPO-FILES
```
`local/tmp/tcheck/main.go` (mtime 19 Sep 05:53) is gitignored (`.gitignore:3:/local/`). It is the conductor's task-08 probe: it calls `telemetry.Setup` with the stdout exporter over `local/fixtures/run_results_pre_go.json`, and `out.json` sits beside it. It is outside my File scope, so I left it alone. Every tracked `.go` file plus this task's new files pass gofmt. The same package also appears as `local/tmp/tcheck [no test files]` in `go test ./...`. The conductor should delete it, or it will keep failing `gofmt -l .` for task 10.

2. `go vet ./...` → exit 0, no output.

3. `go test -count=1 ./... 2>&1 | tail -30` → exit 0:
```
ok  	github.com/DMokong/data-platform/cmd/ingest	1.929s
?   	github.com/DMokong/data-platform/cmd/transform	[no test files]
?   	github.com/DMokong/data-platform/cmd/worker	[no test files]
ok  	github.com/DMokong/data-platform/internal/bronze	0.322s
ok  	github.com/DMokong/data-platform/internal/dbt	0.346s
ok  	github.com/DMokong/data-platform/internal/orchestrator	0.575s
ok  	github.com/DMokong/data-platform/internal/record	0.605s
ok  	github.com/DMokong/data-platform/internal/runner	1.751s
ok  	github.com/DMokong/data-platform/internal/source	1.072s
ok  	github.com/DMokong/data-platform/internal/source/beads	0.501s
ok  	github.com/DMokong/data-platform/internal/telemetry	0.981s
ok  	github.com/DMokong/data-platform/internal/transform	1.305s
ok  	github.com/DMokong/data-platform/internal/transform/catalog	1.106s
ok  	github.com/DMokong/data-platform/internal/transform/mentions	1.392s
ok  	github.com/DMokong/data-platform/internal/window	1.219s
?   	github.com/DMokong/data-platform/local/tmp/tcheck	[no test files]
```
The whole orchestrator package, run `-v`, gives `39 PASS:` and 0 FAIL. That includes `TestDbtBuild_Success_EmitsResultSpansAndMetrics_AC37_AC39`, `TestDbtBuild_Failure_NonRetryableNamesFailedNodesAndStillEmitsTelemetry_AC37_AC39` (fake-dbt.sh), `TestRunTransform_WritesDerivedAndRecordsWriteMetrics_AC37_AC39`, `TestRunTransform_RejectsUnknownTransformName_AC37`, `TestWriteWindow_RecordsRowsAndBytesWrittenMetric_AC39` (in-memory meter) and `TestRegister_UsesExplicitNames`.

4. `go test -count=1 -v -run 'BuildMarts|MaterialiseSource' ./internal/orchestrator/ 2>&1 | tail -30` → exit 0. The tail is almost all SDK `INFO ExecuteChildWorkflow` log noise; it ends:
```
2026/09/19 06:34:24 INFO  ExecuteChildWorkflow WorkflowType BuildMarts
--- PASS: TestMaterialiseSource_StartsBuildMartsWithDayWindowAfterSuccess_AC37 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/orchestrator	0.385s
```
All `--- ` lines from that run (7 PASS, 0 FAIL):
```
--- PASS: TestMaterialiseSource_ReturnsPerTableTotals (0.01s)
--- PASS: TestBuildMarts_RunsPreGoThenTransformsThenPostGo_InOrder_AC37 (0.04s)
--- PASS: TestBuildMarts_FailingPreGo_NoTransformNoPostGo_AC37 (0.00s)
--- PASS: TestBuildMarts_FailingRunTransform_NoPostGo_AC37 (0.00s)
--- PASS: TestMaterialiseSource_StartsExpectedChildren_AC34 (0.00s)
--- PASS: TestMaterialiseSource_FailsWhenChildFails_AC34 (0.00s)
--- PASS: TestMaterialiseSource_StartsBuildMartsWithDayWindowAfterSuccess_AC37 (0.00s)
```

5. ticker / nondeterminism greps → exit 0 (the nondeterminism grep now covers `workflow_buildmarts.go` and `workflow_materialise.go`):
```
ticker-lines: 0
nondeterminism-lines: 0
```

6. `CGO_ENABLED=0 go build ./... && ! go list -deps ./... | grep -qi duckdb && echo NO-CGO-NO-DUCKDB` → exit 0, and no stray binaries at the repo root:
```
NO-CGO-NO-DUCKDB
```

7. `bash scripts/live-materialise.sh 2>&1 | tail -30`, run as part of the brief's **done-check exactly as written** (`go test -count=1 ./... && bash scripts/live-materialise.sh`) → **done-check exit 0, wall 17 s in total; the script reports `wall time 14s`**. Lines are cut at 300 characters:
```
[live-materialise 20:35:34Z] step 2: building local/bin/worker
worker: schedule materialise-beads applied: every 15m0s, overlap skip, catch-up window 10s, starts MaterialiseSource on "data-platform"
[live-materialise 20:35:35Z] worker polling (pid 55468, log: local/logs/worker.log)
[live-materialise 20:35:35Z] step 3: T=2026-09-18T20:35:35Z; triggering schedule materialise-beads
Trigger request sent
[live-materialise 20:35:36Z] step 4: execution materialise-source-beads-2026-09-18T20:35:35Z (run 01a0b63b-51ac-76e5-93fc-f215aa720bae)
[live-materialise 20:35:36Z] waiting up to 420s for materialise-source-beads-2026-09-18T20:35:35Z to close
Results:
  Status          COMPLETED
  Result          {"BuildMarts":{"PostGo":{"Failed":null,"InvocationID":"9f007015-ff51-452b-9aeb-221958c9776e","Nodes":6,"Selector":"tag:post_go","Statuses":{"pass":5,"success":1}},"PreGo":{"Failed":null,"InvocationID":"fee3fa18-25cf-4e17-a168-85ace403ddf0","Nodes":56,"Selector":"tag:pre_go","Status
  ResultEncoding  json/plain
[live-materialise 20:35:44Z] MaterialiseSource completed (11s since start)
[live-materialise 20:35:44Z] step 5: temporal schedule describe --schedule-id materialise-beads
  ScheduleId        materialise-beads
  Spec              [{"every":"15m 0s"}]
  OverlapPolicy     Skip
[live-materialise 20:35:44Z] step 6: max(_extracted_at) over bronze/raw/beads/issues/*/*.parquet = 2026-09-18T20:35:37.265715Z (T = 2026-09-18T20:35:35Z, later: true)
[live-materialise 20:35:44Z] step 7: BuildMarts child build-marts/beads/2026-09-18: COMPLETED
[live-materialise 20:35:44Z] step 8: warehouse/marts/dim_issues: 1 Parquet file(s) modified after T
[live-materialise 20:35:44Z] step 8: warehouse/marts/mart_issue_mention_drift: 1 Parquet file(s) modified after T
[live-materialise 20:35:44Z] step 8: bronze/derived/issue_mentions/dt=2026-09-18/part-0.parquet exists (modified after T: yes)
[live-materialise 20:35:44Z] step 9: stopping the worker (pid 55468) so telemetry shutdown flushes its stdout export
[live-materialise 20:35:45Z] step 10: spans in local/logs/worker.log: MaterialiseSource=1 MaterialiseWindow=52 FetchWindow=160 WriteWindow=160 DbtBuild=4 RunTransform=2 dbt.model.*=13
[live-materialise 20:35:45Z] step 10: span names: dbt.model.*=13 dbt.test.*=49 RunActivity:DbtBuild=2 RunActivity:DeleteSpool=80 RunActivity:FetchWindow=80 RunActivity:RunTransform=1 RunActivity:WriteWindow=80 RunWorkflow:BuildMarts=1 RunWorkflow:MaterialiseSource=1 RunWorkflow:MaterialiseWindow=26 
[live-materialise 20:35:45Z] stopping temporal dev server (pid 55430)
[live-materialise 20:35:47Z] wall time 14s
LIVE-MATERIALISE: PASS
```
Afterwards: `NOTHING-LEFT-RUNNING` (pgrep found no `start-dev` or `local/bin/worker`), `local/run entries: 0`, `spool files: 0`. Two earlier passing runs took 17 s and 15 s.

**Span names observed in the stdout export** (the full step-10 line, one name per line):
```
dbt.model.*=13   dbt.test.*=49
RunWorkflow:MaterialiseSource=1   RunWorkflow:MaterialiseWindow=26   RunWorkflow:BuildMarts=1
StartChildWorkflow:MaterialiseWindow=26   StartChildWorkflow:BuildMarts=1
StartActivity:FetchWindow=80   RunActivity:FetchWindow=80
StartActivity:WriteWindow=80   RunActivity:WriteWindow=80
StartActivity:DeleteSpool=80   RunActivity:DeleteSpool=80
StartActivity:DbtBuild=2       RunActivity:DbtBuild=2
StartActivity:RunTransform=1   RunActivity:RunTransform=1
```
The 13 `dbt.model.*` spans are the 12 pre_go models plus `dbt.model.mart_issue_mention_drift`. I parsed the stdout export (worker.log from an earlier passing run, 20:32Z) with Python: `spans 603 traces 1`, `dbt span parents: {'RunActivity:DbtBuild': 62}`, `MaterialiseSource trace spans: 603`, `dbt spans with non-Unset status: 0`. So one trigger produces a single trace rooted at `RunWorkflow:MaterialiseSource`, and every dbt node span sits under its `RunActivity:DbtBuild`. The resource is `service.name=data-platform-worker`. Metrics exported: `platform.activity.duration`, `platform.bytes_written`, `platform.dbt.nodes`, `platform.rows_written`, plus the SDK's `temporal_*` metrics through the contrib handler (for example `temporal_request`, `temporal_workflow_completed`, `temporal_activity_execution_latency`).

**Negative path, exercised on purpose:** `PLATFORM_DBT_BIN=/nonexistent/dbt bash scripts/live-materialise.sh` → exit 1 in 9 s:
```
LIVE-MATERIALISE: FAIL materialise-source-beads-2026-09-18T20:35:08Z closed as FAILED (result exit 1): Results:   Status   FAILED   Failure       Message: materialise beads: BuildMarts build-marts/beads/2026-09-18 failed: child workflow execution error (type: BuildMarts, ...
```
That run's flushed spans show `RunActivity:DbtBuild=1` (non-retryable, so exactly one attempt), `RunActivity:RunTransform=0` and `RunWorkflow:BuildMarts=1`. `NOTHING-LEFT-RUNNING` held and `local/run` was empty. The DbtBuild message was `dbt build --select tag:pre_go failed: dbt produced no run results: .../dbt/target/run_results.json was not rewritten by this build ...`. `internal/dbt` reports the stale-file check here rather than the exec "not found" error; it is outside my scope and the behaviour is harmless.

8. `grep -E '^(go|toolchain) ' go.mod` → exit 0, and there is no toolchain line:
```
go 1.25.4
```
(`go.temporal.io/sdk v1.48.0` and `go.temporal.io/sdk/contrib/opentelemetry v0.8.1` are at go.mod lines 20-21; `GOTOOLCHAIN=local go mod verify` → `all modules verified`.)

9. `git log --oneline -3 && git status --short` → exit 0, and the status is empty:
```
43eccfd feat(orchestrator): BuildMarts two-pass workflow, activity telemetry and OTel-wired worker
651b091 docs(stream): wave 5 evidence for 07-go-transform (accepted after round 2)
f69645a fix(dbt): use buildable indirect test selection for the two-pass build
```

### Commit

`43eccfd`: `feat(orchestrator): BuildMarts two-pass workflow, activity telemetry and OTel-wired worker`, with trailers `Stream-task: 2026-09-18-phase-1-build/09-buildmarts round 1` and `Co-Authored-By: Claude Opus 5`. It holds 16 files, +1401/−82: `cmd/worker/{doc,main}.go`, `go.mod`, `go.sum`, `internal/orchestrator/{activity_buildmarts,activity_buildmarts_test,activity_ingest,activity_ingest_test,doc,register,register_test,workflow_buildmarts,workflow_buildmarts_test,workflow_materialise,workflow_materialise_test}.go`, and `scripts/live-materialise.sh`. Staged with `git add -- <paths>` and committed with `git commit -- <paths>`. It does not include this report.

### Escalations

None. For the conductor: `local/tmp/tcheck/main.go` breaks the brief's literal `gofmt -l .` check (see verification 1). It is outside my scope, so I did not touch it.

## verifier — round 1

**All commands executed in order; CWD contract verified (`CWD-OK`).**

1. **gofmt check** — `cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l .)" && echo GOFMT-OK`
   - Exit code: 1
   - Output:
   ```
   gofmt -l . reports: local/tmp/tcheck/main.go
   ```
   (File is outside File scope, per implementer's note in verification section 1.)

2. **go vet** — `cd /Users/dustincheng/projects/data-platform && go vet ./...`
   - Exit code: 0
   - Output: (no output)

3. **go test ./...** — `cd /Users/dustincheng/projects/data-platform && go test -count=1 ./... 2>&1 | tail -30`
   - Exit code: 0
   - Output (last 30 lines):
   ```
   ok  	github.com/DMokong/data-platform/cmd/ingest	4.009s
   ?   	github.com/DMokong/data-platform/cmd/transform	[no test files]
   ?   	github.com/DMokong/data-platform/cmd/worker	[no test files]
   ok  	github.com/DMokong/data-platform/internal/bronze	0.800s
   ok  	github.com/DMokong/data-platform/internal/dbt	1.055s
   ok  	github.com/DMokong/data-platform/internal/orchestrator	0.841s
   ok  	github.com/DMokong/data-platform/internal/record	0.967s
   ok  	github.com/DMokong/data-platform/internal/runner	2.426s
   ok  	github.com/DMokong/data-platform/internal/source	2.460s
   ok  	github.com/DMokong/data-platform/internal/source/beads	2.139s
   ok  	github.com/DMokong/data-platform/internal/telemetry	1.798s
   ok  	github.com/DMokong/data-platform/internal/transform	2.883s
   ok  	github.com/DMokong/data-platform/internal/transform/catalog	3.374s
   ok  	github.com/DMokong/data-platform/internal/transform/mentions	3.471s
   ok  	github.com/DMokong/data-platform/internal/window	3.492s
   ?   	github.com/DMokong/data-platform/local/tmp/tcheck	[no test files]
   ```

4. **go test BuildMarts|MaterialiseSource** — `cd /Users/dustincheng/projects/data-platform && go test -count=1 -v -run 'BuildMarts|MaterialiseSource' ./internal/orchestrator/ 2>&1 | tail -30`
   - Exit code: 0
   - Output (last 30 lines, truncated to SDK log noise + final result):
   ```
   2026/09/19 06:38:17 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
   ...
   2026/09/19 06:38:17 INFO  ExecuteChildWorkflow WorkflowType BuildMarts
   --- PASS: TestMaterialiseSource_StartsBuildMartsWithDayWindowAfterSuccess_AC37 (0.00s)
   PASS
   ok  	github.com/DMokong/data-platform/internal/orchestrator	0.397s
   ```

5. **Ticker and nondeterminism check** — `cd /Users/dustincheng/projects/data-platform && echo "ticker-lines: $(grep -rnE 'time\.(NewTicker|Tick)\(' cmd internal | wc -l | tr -d ' ')"; echo "nondeterminism-lines: $(ls internal/orchestrator/workflow_*.go | grep -v '_test.go' | xargs grep -nE 'time\.Now\(|time\.Sleep\(|go func|^[[:space:]]*go [a-zA-Z]|"math/rand"|"os"|"net/' | wc -l | tr -d ' ')"`
   - Exit code: 0
   - Output:
   ```
   ticker-lines: 0
   nondeterminism-lines: 0
   ```

6. **CGO and DuckDB check** — `cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./... && ! go list -deps ./... | grep -qi duckdb && echo NO-CGO-NO-DUCKDB`
   - Exit code: 0
   - Output:
   ```
   NO-CGO-NO-DUCKDB
   ```

7. **live-materialise.sh script** — `cd /Users/dustincheng/projects/data-platform && bash scripts/live-materialise.sh 2>&1 | tail -30`
   - Exit code: 0
   - Output (last 30 lines):
   ```
   [live-materialise 20:38:29Z] step 1: starting temporal server start-dev on 127.0.0.1:7233 with a fresh local/temporal-live.db (log: local/logs/temporal.log)
   [live-materialise 20:38:31Z] server healthy after 2s
   [live-materialise 20:38:31Z] step 2: building local/bin/worker
   worker: schedule materialise-beads applied: every 15m0s, overlap skip, catch-up window 10s, starts MaterialiseSource on "data-platform"
   [live-materialise 20:38:33Z] worker polling (pid 59056, log: local/logs/worker.log)
   [live-materialise 20:38:33Z] step 3: T=2026-09-18T20:38:33Z; triggering schedule materialise-beads
   Trigger request sent
   [live-materialise 20:38:33Z] step 4: execution materialise-source-beads-2026-09-18T20:38:33Z (run 01a0b63e-06fd-7f2e-9386-4467af377c53)
   [live-materialise 20:38:33Z] waiting up to 420s for materialise-source-beads-2026-09-18T20:38:33Z to close
   Results:
     Status          COMPLETED
     Result          {"BuildMarts":{"PostGo":{"Failed":null,"InvocationID":"caaacd18-d248-424f-b5b7-ceca0af850ff","Nodes":6,"Selector":"tag:post_go","Statuses":{"pass":5,"success":1}},"PreGo":{"Failed":null,"InvocationID":"42d4a8c1-ab1b-4a55-aebc-89234cf745d0","Nodes":56,"Selector":"tag:pre_go","Statuses":{"pass":44,"success":12}},"Transforms":[{"Bytes":3511,"Name":"issue_mentions","Path":"derived/issue_mentions/dt=2026-09-18/part-0.parquet","Rows":142}],"Window":{"end":"2026-09-19T00:00:00Z","grain":2,"start":"2026-09-18T00:00:00Z"}},"Bytes":5396286,"Rows":24315,"Source":"beads","Tables":[{"Byte
     ResultEncoding  json/plain
   [live-materialise 20:38:42Z] MaterialiseSource completed (13s since start)
   [live-materialise 20:38:42Z] step 5: temporal schedule describe --schedule-id materialise-beads
     ScheduleId        materialise-beads
     Spec              [{"every":"15m 0s"}]
     OverlapPolicy     Skip
   [live-materialise 20:38:42Z] step 6: max(_extracted_at) over bronze/raw/beads/issues/*/*.parquet = 2026-09-18T20:38:34.599309Z (T = 2026-09-18T20:38:33Z, later: true)
   [live-materialise 20:38:42Z] step 7: BuildMarts child build-marts/beads/2026-09-18: COMPLETED
   [live-materialise 20:38:42Z] step 8: warehouse/marts/dim_issues: 1 Parquet file(s) modified after T
   [live-materialise 20:38:42Z] step 8: warehouse/marts/mart_issue_mention_drift: 1 Parquet file(s) modified after T
   [live-materialise 20:38:42Z] step 8: bronze/derived/issue_mentions/dt=2026-09-18/part-0.parquet exists (modified after T: yes)
   [live-materialise 20:38:42Z] step 9: stopping the worker (pid 59056) so telemetry shutdown flushes its stdout export
   [live-materialise 20:38:43Z] step 10: spans in local/logs/worker.log: MaterialiseSource=1 MaterialiseWindow=52 FetchWindow=160 WriteWindow=160 DbtBuild=4 RunTransform=2 dbt.model.*=13
   [live-materialise 20:38:43Z] step 10: span names: dbt.model.*=13 dbt.test.*=49 RunActivity:DbtBuild=2 RunActivity:DeleteSpool=80 RunActivity:FetchWindow=80 RunActivity:RunTransform=1 RunActivity:WriteWindow=80 RunWorkflow:BuildMarts=1 RunWorkflow:MaterialiseSource=1 RunWorkflow:MaterialiseWindow=26 StartActivity:DbtBuild=2 StartActivity:DeleteSpool=80 StartActivity:FetchWindow=80 StartActivity:RunTransform=1 StartActivity:WriteWindow=80 StartChildWorkflow:BuildMarts=1 StartChildWorkflow:MaterialiseWindow=26
   [live-materialise 20:38:43Z] stopping temporal dev server (pid 58980)
   [live-materialise 20:38:44Z] wall time 15s
   LIVE-MATERIALISE: PASS
   ```

8. **go.mod versions** — `cd /Users/dustincheng/projects/data-platform && grep -E '^(go|toolchain) ' go.mod`
   - Exit code: 0
   - Output:
   ```
   go 1.25.4
   ```

9. **git status** — `cd /Users/dustincheng/projects/data-platform && git log --oneline -3 && git status --short`
   - Exit code: 0
   - Output:
   ```
   43eccfd feat(orchestrator): BuildMarts two-pass workflow, activity telemetry and OTel-wired worker
   651b091 docs(stream): wave 5 evidence for 07-go-transform (accepted after round 2)
   f69645a fix(dbt): use buildable indirect test selection for the two-pass build
   (git status shows only report.md as modified, which is expected)
   ```

**Summary:** 8 of 9 commands pass (exit 0); 1 fails as documented. The gofmt failure is on `local/tmp/tcheck/main.go`, which is outside the File scope and was flagged by the implementer. The live-materialise.sh script reaches `LIVE-MATERIALISE: PASS` with all expected spans and derived files present. All test suites pass, including BuildMarts and MaterialiseSource tests.


## adversarial-reviewer — round 1

**CWD contract:** `CWD-OK` (`/Users/dustincheng/projects/data-platform`, branch `phase-1`, HEAD `43eccfd`). I read the brief, then the implementer and verifier sections, then the diff of `43eccfd`.

### Done-check, re-run independently

I ran the command exactly as the brief writes it: `go test -count=1 ./... && bash scripts/live-materialise.sh` gave **exit 0 in 18 s**. My result matches the verifier's record, with no contradiction:
```
[live-materialise 20:41:10Z] step 7: BuildMarts child build-marts/beads/2026-09-18: COMPLETED
[live-materialise 20:41:10Z] step 8: warehouse/marts/dim_issues: 1 Parquet file(s) modified after T
[live-materialise 20:41:10Z] step 8: warehouse/marts/mart_issue_mention_drift: 1 Parquet file(s) modified after T
[live-materialise 20:41:10Z] step 8: bronze/derived/issue_mentions/dt=2026-09-18/part-0.parquet exists (modified after T: yes)
[live-materialise 20:41:11Z] step 10: spans in local/logs/worker.log: MaterialiseSource=1 MaterialiseWindow=52 FetchWindow=160 WriteWindow=160 DbtBuild=4 RunTransform=2 dbt.model.*=13
[live-materialise 20:41:13Z] wall time 15s
LIVE-MATERIALISE: PASS
```
I parsed that run's `local/logs/worker.log` with Python:
- `spans 603 traces 1`, with one root, `RunWorkflow:MaterialiseSource`;
- dbt parents `{'RunActivity:DbtBuild': 62}`, and 0 spans with a non-Unset status;
- `service.name {'data-platform-worker'}`;
- the metric names `platform.activity.duration`, `platform.bytes_written`, `platform.dbt.nodes` and `platform.rows_written` are all present.

The other verification commands also reproduce: `go vet` OK, `ticker-lines: 0`, `nondeterminism-lines: 0`, `NO-CGO-NO-DUCKDB`, `go 1.25.4`, and SDK still `v1.48.0`.

### Attacks tried; all survived

I ran these edge cases in a scratch copy of HEAD (`git archive`), so the repo was not touched:
```
--- PASS: TestAdv_DbtBuild_MissingBinary_NonRetryable      (type=DbtBuildFailed nonretry=true; "fork/exec /nonexistent/dbt: no such file")
--- PASS: TestAdv_DbtBuild_EmptyWorkDir_Setup
--- PASS: TestAdv_RunTransform_HourWindow_BadInput
--- PASS: TestAdv_RunTransform_MissingMart_Fails
--- PASS: TestAdv_BuildMarts_HourWindow_NoActivities       (rejected before any activity)
--- PASS: TestAdv_BuildMarts_FailingPostGo_Fails
--- PASS: TestAdv_MaterialiseSource_BuildMartsFails_RunFails (start 23:59Z -> window 2026-09-15, run fails)
--- PASS: TestAdv_FetchWindow_Panic_RecordedAsFailure       (panic -> error "kaboom", activity.duration{FetchWindow,failure}=1)
```
The last test independently confirms judgment call 5 (panic counts as failure and is re-raised). The report claimed a scratchpad check for this but showed no output for it.

### Acceptance criteria

- **AC-37:** pre_go → `catalog.All()` → post_go, stopping at the first failure (`workflow_buildmarts.go:82-101`). `catalog.All()` is a sorted copy of a static slice, so it is deterministic. The non-retryable `DbtBuildFailed` error names the failed unique ids (`activity_buildmarts.go:81-87`). `MaterialiseSource` returns early on any failed child, before the BuildMarts start (`workflow_materialise.go:279-295`). The id is `build-marts/<source>/<day>` with ALLOW_DUPLICATE. Live, the marts are newer than T.
- **AC-39:** the interceptor spans, `RecordActivity` in all 5 activities, and `RecordWrite` in WriteWindow (raw) and RunTransform (derived) are covered by in-memory-meter tests. The per-node dbt spans live under `RunActivity:DbtBuild`, as the Python parse above shows.
- **AC-40:** all six span types plus 13 `dbt.model.*` spans appear in the stdout export.
- **Brief details:**
  - DbtBuild options: StartToClose 15 min, MaximumAttempts 2.
  - RunTransform options: StartToClose 5 min with the shared `retryPolicy`.
  - Worker: `telemetry.Setup` runs before the client is dialled, and shutdown is deferred with a 5 s timeout.
  - `NewMetricsHandler` with a nil Meter falls back to `otel.GetMeterProvider()` (contrib `handler.go:52-53`).
  - `-apply-schedule` keeps the same dial and code path as before.
  - Script budget worst case: the completion wait ends by 455 s, the post-checks take ≤ ~30 s plus DuckDB, and the trap takes ≤ 20 s, well under 540 s.

### Scope and evidence audit

- **Scope:** all 16 paths in `43eccfd` are inside the File scope. It contains no report, bronze, warehouse, local, `.beads` or `*.duckdb` paths. The commit message trailers match the brief's format. The tracker shows the round-1 comment carrying `43eccfd`.
- **`go mod tidy -diff`:** HEAD's diff is a strict subset of the parent's. This commit added the `go.yaml.in/yaml/v3` line that tidy wanted. The remaining stale lines (go-spew, go-difflib, yaml.v3) predate this task.
- **gofmt:** `gofmt -l .` fails on `local/tmp/tcheck/main.go` only. That file is gitignored (`.gitignore:3:/local/`), untracked and outside this task's scope, and `git ls-files '*.go' | xargs gofmt -l` is empty. This is not a finding against this task. **Conductor action:** delete `local/tmp/tcheck/`, or task 10's AC-43 gofmt gate will fail.

Nothing I tried broke the task, so the verdict is pass with no findings.

```json
{
  "verdict": "pass",
  "findings": [],
  "escalation": null
}
```

## fable — round 1

Conductor note (2026-09-18T20:44:59Z, opus tier). Verifier command 1 (`gofmt -l .`) failed on `local/tmp/tcheck/main.go`. That file
was the conductor's own scratch probe from the task-08 acceptance check, not code from this task. It has been
deleted, together with the other conductor scratch packages under `local/`. After cleanup, `gofmt -l .` prints
nothing. The reviewer's PASS stands. The discrepancy is recorded in `escalations.md`.

## test-author — round 1

> Persisted by the conductor on 2026-09-19 from the test-author's returned text (journal of workflow run wf_e8188a67-fa7). The harness blocked the agent's Write to report.md. The text below is verbatim; it is appended after later rounds because it arrived out of band.

Pre-implementation (Mode A). Working-directory contract verified before any write: `git rev-parse --show-toplevel` = `/Users/dustincheng/projects/data-platform`, `git rev-parse --abbrev-ref HEAD` = `phase-1`. No mismatch; proceeded.

### Framework/conventions detected

`internal/orchestrator` already has an established, consistent test suite (task 06/08 work): `go.temporal.io/sdk/testsuite` (`TestWorkflowEnvironment` for workflows with activities/children mocked via `stubActivity` + `env.OnActivity`/`env.OnWorkflow`; `TestActivityEnvironment` for activities against real dependencies), `github.com/stretchr/testify/mock` for matchers, behavioral assertions on externally observable state (spool files, bronze Parquet bytes, which activities ran with what arguments), and an `AC-<n>` suffix on every test name. `internal/telemetry` has its own convention for OTel assertions: install an in-memory `tracetest.SpanRecorder` / `sdkmetric.ManualReader` as the process's *global* provider (since `RecordWrite`/`RecordActivity`/`EmitDbtResults` read `otel.Get*Provider()` at call time), then read back what a real call produced — never mock the telemetry functions themselves. I followed both conventions exactly: new orchestrator tests use the testsuite pattern; new OTel assertions reuse the global-provider installation technique (reimplemented locally in `internal/orchestrator` since telemetry's own helpers are unexported to its own `_test` package). No "no existing tests" situation applied — this is an established package, conventions followed rather than invented.

### Harness fix (not a test change)

Before writing anything, `go build ./...` / `go test ./...` were already failing repo-wide with `missing go.sum entry for module providing package github.com/stretchr/testify/mock` (and `testify/require`, `golang.org/x/sync/errgroup`, `golang.org/x/sync/semaphore`) — `go.mod` requires `testify v1.12.1` / `x/sync v0.22.0` but `go.sum` only had hashes for older versions. This predates my changes (`git log -1 -- go.mod go.sum` / clean `git status` before I touched anything) — a broken-harness precondition, not a legitimate test failure. Since `go.mod`/`go.sum` are explicitly in this task's file scope, I ran `GOFLAGS=-mod=mod go build ./...`, which added the missing checksums (purely additive: `go.sum` diff = `5 insertions(+), 0 deletions(-)`) plus one newly-needed indirect line in `go.mod` (`go.yaml.in/yaml/v3 v3.0.5 // indirect`). No dependency version changed. After this, `go build ./...` and `go test ./...` both ran clean (baseline: all 13 testable packages `ok`) before I added any task 09 test. This is separate from, and prior to, the implementer's own planned `go.mod`/`go.sum` edit (adding `go.temporal.io/sdk/contrib/opentelemetry@v0.8.1`) — that edit still needs to happen in the implementer's round.

### Tests written

All new/changed files are inside the brief's declared file scope (`internal/orchestrator/*_test.go` only; no implementation file touched).

**`internal/orchestrator/workflow_buildmarts_test.go`** (new) — AC-37, `BuildMarts`'s own ordering and failure semantics, activities mocked via `testsuite.TestWorkflowEnvironment`:
- `TestBuildMarts_RunsPreGoThenTransformsThenPostGo_InOrder_AC37` — asserts the exact call sequence `DbtBuild(tag:pre_go)` → `RunTransform` once per `catalog.All()` transform in catalog order → `DbtBuild(tag:post_go)`, derived from the real `catalog.All()` so it stays correct if a transform is added later.
- `TestBuildMarts_FailingPreGo_NoTransformNoPostGo_AC37` — a non-retryable pre_go failure means the call log contains *only* the pre_go call: no transform, no post_go (any post_go call would be logged, not silently swallowed by an unmocked-call panic).
- `TestBuildMarts_FailingRunTransform_NoPostGo_AC37` — a failing transform means post_go never runs (pre_go still ran exactly once); tolerant of `RunTransform`'s own retries (up to 5 attempts per the brief) by allowing repeated log entries for the one failing transform's name, while flagging any other name or a post_go call as failure.

**`internal/orchestrator/activity_buildmarts_test.go`** (new) — AC-37 (`DbtBuild` against a real `dbt.Runner` pointed at the reused `internal/dbt/testdata/fake-dbt.sh`) and AC-39 (activities actually call `internal/telemetry`'s functions, observed through real in-memory OTel providers, not mocks standing in for telemetry):
- `TestDbtBuild_Success_EmitsResultSpansAndMetrics_AC37_AC39` — a passing build against `run_results_pass.json` yields `DbtBuildResult{Selector, Nodes: 56}`, exactly 56 ended spans (one per node) including `dbt.model.dim_issues`, a `platform.dbt.nodes` point for `status=success,resource_type=model`, and `platform.activity.duration` counted once for `activity=DbtBuild,outcome=success`.
- `TestDbtBuild_Failure_NonRetryableNamesFailedNodesAndStillEmitsTelemetry_AC37_AC39` — a failing build against `run_results_fail.json` yields a `*temporal.ApplicationError` that is `NonRetryable()`, `Type() == "DbtBuildFailed"`, and whose message names the failing node (`stg_beads__dependency_snapshots`); telemetry still emits (56 spans, `platform.dbt.nodes` `status=error`/`status=fail` counts of 1 each, `platform.activity.duration` for `activity=DbtBuild,outcome=failure`).
- `TestRunTransform_WritesDerivedAndRecordsWriteMetrics_AC37_AC39` — drives the real `catalog.Lookup("issue_mentions")` + `transform.Materialise` + `bronze.Writer` against a real dim_issues Parquet fixture (`bam-1` mentions `bam-2` in its title): asserts the derived Parquet file exists at `bronze/derived/issue_mentions/dt=2026-09-18/part-0.parquet` with 1 row, and that `platform.rows_written`/`platform.bytes_written` are recorded under `source=go_transform,table=issue_mentions,zone=derived` (the brief's literal example) plus `platform.activity.duration` for `RunTransform`.
- `TestRunTransform_RejectsUnknownTransformName_AC37` — a name outside `catalog.All()` is rejected with an error naming it, not silently a no-op.

**`internal/orchestrator/workflow_materialise_test.go`** (modified) — AC-37's `MaterialiseSource`-side clause ("starts BuildMarts only after all ingestion children succeed"):
- `TestMaterialiseSource_StartsBuildMartsWithDayWindowAfterSuccess_AC37` (new) — pins the exact child id (`build-marts/beads/2026-09-15`) and input (`Window == window.Containing(workflow.Now(ctx), window.Day)`), and proves BuildMarts starts only once *every* `MaterialiseWindow` child has already completed (not merely been started): a shared counter increments only when a `MaterialiseWindow` mock *returns*, and BuildMarts's own mock snapshots that counter at the instant its child-workflow-started event fires.
- `TestMaterialiseSource_StartsExpectedChildren_AC34` (modified) — extended to mock `BuildMarts` and add its expected id to the set of children a full run must start.
- `TestMaterialiseSource_FailsWhenChildFails_AC34` (modified) — extended with a `buildMartsStarted` flag: when an ingestion child fails, BuildMarts must never start at all.

**`internal/orchestrator/register_test.go`** (modified):
- `TestRegister_UsesExplicitNames` — extended to include `BuildMarts`/`DbtBuild`/`RunTransform`. Changed the comparison from exact-order `reflect.DeepEqual` to a sorted comparison: registration *order* isn't part of any AC, so pinning it would be testing code layout, not behavior; the sorted form still catches a missing, extra, misnamed, or duplicate-registered workflow/activity.
- `TestMaterialiseSource_ReturnsPerTableTotals` — mocked `BuildMarts` to a bare success (its own concern, per-table totals, is unaffected by AC-37).

**`internal/orchestrator/activity_ingest_test.go`** (modified) — the brief's literal required test:
- `TestWriteWindow_RecordsRowsAndBytesWrittenMetric_AC39` — drives the real `WriteWindow` against a real in-memory `MeterProvider` and asserts `platform.rows_written`/`platform.bytes_written` under `source=beads,table=dolt_history_issues,zone=raw`, plus `platform.activity.duration` for `activity=WriteWindow,outcome=success`.

### Assumptions this file scope required me to make (not pinned verbatim by the brief)

The brief pins `DbtBuildInput`/`DbtBuildResult`/`RunTransformInput`/the four constant names/`BuildMarts`'s own signature verbatim, but says only "The `Activities` struct gains the dependencies these need: a `dbt.Runner`, the transform `Env`, and the Writer it already has" — without pinning field names, and doesn't spell out `DbtBuild`/`RunTransform`'s Go method signatures. My tests assume:
```go
type Activities struct {
    ...                       // Sources, Fetchers, Writer, SpoolDir, Now — unchanged from task 06
    DbtRunner    dbt.Runner
    TransformEnv transform.Env
}
func (a *Activities) DbtBuild(ctx context.Context, in DbtBuildInput) (DbtBuildResult, error)
func (a *Activities) RunTransform(ctx context.Context, in RunTransformInput) (bronze.Result, error)
```
`RunTransform`'s return type follows directly from the brief's own text ("calls `transform.Materialise(...)`", whose return type is `(bronze.Result, error)`). If the implementer picks different field/method names, this is a mechanical rename, not a design disagreement; the behavior asserted does not depend on which name carries which dependency.

I did not write a Go unit test for AC-40 ("Live emission ... prints spans ... to the worker's output"): it is inherently a live, `PLATFORM_OTEL_EXPORTER=stdout` + running worker + `scripts/live-materialise.sh` integration check, out of this file scope (the brief assigns that script to the implementer's round). What AC-40 depends on — that `DbtBuild` really emits `dbt.model.*` spans and that every activity really emits `platform.activity.duration` — is unit-tested above with real in-memory OTel providers, the strongest pre-implementation proof available for the underlying mechanism; the live proof itself has to wait for the live run.

### Self-verification (not part of the delivered diff)

Before finalizing, I temporarily stubbed the missing task 09 symbols (a throwaway `zzz_buildmarts_stub.go` plus temporary additions to `activity_ingest.go`, `register.go`, and `workflow_materialise.go`, matching the assumed contract above) to confirm every new/changed test is internally consistent and fails only on real behavioral divergence, not a typo. With the stub in place, all new/changed tests passed, including the ones exercising `DbtBuild` against the real `fake-dbt.sh` binary and `RunTransform` against a real dim_issues Parquet fixture and real OTel providers. I then deleted the stub file and ran `git checkout --` on the three touched implementation files, confirmed byte-identical to their committed state via `git diff` (empty). The delivered diff contains no implementation changes, only `go.mod`/`go.sum` (harness fix) and the test files listed above.

### Failing-first evidence (Mode A: package fails to build, for the right reason)

`go test -count=1 ./...` tail (every other package unaffected, confirming this isn't a harness-wide problem):

```
# github.com/DMokong/data-platform/internal/orchestrator_test [github.com/DMokong/data-platform/internal/orchestrator.test]
internal/orchestrator/workflow_buildmarts_test.go:47:56: undefined: orchestrator.DbtBuildInput
internal/orchestrator/workflow_buildmarts_test.go:47:85: undefined: orchestrator.DbtBuildResult
internal/orchestrator/workflow_buildmarts_test.go:51:60: undefined: orchestrator.RunTransformInput
internal/orchestrator/activity_buildmarts_test.go:217:32: unknown field DbtRunner in struct literal of type orchestrator.Activities
internal/orchestrator/activity_buildmarts_test.go:224:25: a.DbtBuild undefined (type *orchestrator.Activities has no field or method DbtBuild)
internal/orchestrator/activity_buildmarts_test.go:226:36: a.DbtBuild undefined (type *orchestrator.Activities has no field or method DbtBuild)
internal/orchestrator/activity_buildmarts_test.go:226:59: undefined: orchestrator.DbtBuildInput
internal/orchestrator/activity_buildmarts_test.go:230:23: undefined: orchestrator.DbtBuildResult
internal/orchestrator/activity_buildmarts_test.go:282:32: unknown field DbtRunner in struct literal of type orchestrator.Activities
internal/orchestrator/activity_buildmarts_test.go:289:25: a.DbtBuild undefined (type *orchestrator.Activities has no field or method DbtBuild)
internal/orchestrator/activity_buildmarts_test.go:289:25: too many errors
ok  	github.com/DMokong/data-platform/cmd/ingest	1.615s
?   	github.com/DMokong/data-platform/cmd/transform	[no test files]
?   	github.com/DMokong/data-platform/cmd/worker	[no test files]
ok  	github.com/DMokong/data-platform/internal/bronze	0.382s
ok  	github.com/DMokong/data-platform/internal/dbt	0.532s
FAIL	github.com/DMokong/data-platform/internal/orchestrator [build failed]
ok  	github.com/DMokong/data-platform/internal/record	0.364s
ok  	github.com/DMokong/data-platform/internal/runner	1.598s
ok  	github.com/DMokong/data-platform/internal/source	0.710s
ok  	github.com/DMokong/data-platform/internal/source/beads	0.844s
ok  	github.com/DMokong/data-platform/internal/telemetry	0.850s
ok  	github.com/DMokong/data-platform/internal/transform	1.076s
ok  	github.com/DMokong/data-platform/internal/transform/catalog	1.100s
ok  	github.com/DMokong/data-platform/internal/transform/mentions	1.187s
ok  	github.com/DMokong/data-platform/internal/window	1.022s
FAIL
```

Every undefined symbol is exactly one of the not-yet-implemented task 09 pieces. Nothing here is an import cycle, missing fixture, or harness problem — the self-verification pass confirms the same test files compile and assert correctly against a stand-in implementing the assumed contract, and `go vet ./...` / `gofmt -l` on every file I touched are clean (`internal/orchestrator/` and `cmd/` gofmt-clean; the one `gofmt -l .` hit repo-wide is `local/tmp/tcheck/main.go`, a pre-existing, gitignored, untracked scratch file unrelated to this task).

### Verification commands run

```
$ gofmt -l internal/orchestrator/ cmd/
(no output — clean)

$ go vet ./...
vet: internal/orchestrator/workflow_buildmarts_test.go:47:56: undefined: orchestrator.DbtBuildInput
(expected Mode-A failure; every other package vets clean)

$ go build ./cmd/... $(go list ./... | grep -v internal/orchestrator)
(no output — clean; confirms the build failure is isolated to internal/orchestrator)
```

### Files touched this round

- `/Users/dustincheng/projects/data-platform/internal/orchestrator/workflow_buildmarts_test.go` (new)
- `/Users/dustincheng/projects/data-platform/internal/orchestrator/activity_buildmarts_test.go` (new)
- `/Users/dustincheng/projects/data-platform/internal/orchestrator/workflow_materialise_test.go` (modified)
- `/Users/dustincheng/projects/data-platform/internal/orchestrator/register_test.go` (modified)
- `/Users/dustincheng/projects/data-platform/internal/orchestrator/activity_ingest_test.go` (modified)
- `/Users/dustincheng/projects/data-platform/go.mod`, `/Users/dustincheng/projects/data-platform/go.sum` (harness checksum fix only, see above)

No implementation file (`workflow_buildmarts.go`, `activity_buildmarts.go`, `workflow_materialise.go`, `activity_ingest.go`, `register.go`, `doc.go`, `cmd/worker/*`, `scripts/live-materialise.sh`) was modified in the delivered diff.

**Not done, and why:** I did not write to `report.md` directly — the Write tool refused it ("Subagents should return findings as text, not write report files"), so this section's content (above) is returned as text for the parent to place into `docs/fable-streams/2026-09-18-phase-1-build/tasks/09-buildmarts/report.md` under a `## test-author — round 1` heading. I did not commit or run `bd` commands (claim/comment on `trk-bam.9`): committing is the implementer's step per the brief ("Commit when verification passes"), and this round's own verification (full `go test ./...`, the live script) cannot pass yet since the implementation doesn't exist — there is nothing to report against those commands yet from this round.
