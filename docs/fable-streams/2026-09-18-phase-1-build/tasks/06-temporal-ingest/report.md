## implementer — round 1

**Status: done.** Commit `9b5f7ab` on `phase-1` (19 paths, all inside the File scope). Tracker: `bd update trk-bam.6 --claim --actor claudeclaw` succeeded before the first edit ("✓ Updated issue: trk-bam.6").

### State on arrival

The working-directory check printed `CWD-OK` (toplevel `/Users/dustincheng/projects/data-platform`, branch `phase-1`). `report.md` existed but was empty (0 bytes), so there was no test-author section. The test-author's work was already on disk, untracked: `internal/orchestrator/{helpers_test.go, activity_ingest_test.go, workflow_materialise_test.go}`. `go.mod`/`go.sum` already had `go.temporal.io/sdk v1.48.0` and testify from its `go get`. I kept those tests as they were, implemented against them, and committed them with my code. I re-ran `GOTOOLCHAIN=local go get go.temporal.io/sdk@v1.48.0` and it was a no-op. `docs/.../05-dbt-project/report.md` was also modified in the working tree. It belongs to a sibling task, so I did not touch or stage it.

### Files (all new unless marked)

| Path | Why |
|---|---|
| `internal/orchestrator/doc.go` | Package doc with `// Data-engineering concept: durable, replayable orchestration (activities, workflows, schedules,` + `// claim-check).` |
| `internal/orchestrator/register.go` | `TaskQueue`, the 5 `Workflow*`/`Activity*` name constants, `Declarations() map[string]source.Source` (fresh map from `beads.Declaration()`, readable from workflow code), `Register(worker.Registry, *Activities)` with explicit `RegisterOptions{Name: …}` |
| `internal/orchestrator/spool.go` | `SpoolRef` (fields exactly as brief), spool codec (`spoolFile`, `writeSpool` temp+sync+rename, `readSpool`, `removeSpool`), `spoolPath` = `<SpoolDir>/<source>/<table>/<window>-<runID>-<activityID>.gob` (ids sanitised to `[A-Za-z0-9._-]`) |
| `internal/orchestrator/activity_ingest.go` | `Activities` (declared here as required), `FetchWindowInput`, `FetchWindow` / `WriteWindow` / `DeleteSpool` |
| `internal/orchestrator/workflow_materialise.go` | Inputs, result types (`TableResult`, `MaterialiseWindowResult`, `TableTotals`, `MaterialiseSourceResult`, with per-table rows and bytes), activity options, `MaterialiseWindow`, `MaterialiseSource` |
| `internal/orchestrator/schedule.go` | `ApplySchedule` (create, or update on `temporal.ErrScheduleAlreadyRunning`), `ScheduleID`, `ScheduleCatchupWindow = 10s` |
| `internal/orchestrator/{register,schedule}_test.go`, `{spool,workflow}_internal_test.go` | My additional tests (see below) |
| `internal/orchestrator/{helpers,activity_ingest,workflow_materialise}_test.go` | Test-author's files, committed unchanged |
| `cmd/worker/doc.go`, `cmd/worker/main.go`, `cmd/worker/main_test.go` | Worker binary: `-root`, `-apply-schedule`; `TEMPORAL_ADDRESS` (default `127.0.0.1:7233`), `TEMPORAL_NAMESPACE` (default `default`), `beads.ConfigFromEnv()`; `MaxConcurrentActivityExecutionSize: 8`; `w.Run(worker.InterruptCh())`, deferred `fetcher.Close()` + `c.Close()` |
| `scripts/live-materialise.sh` | Live proof (steps 0–8 of the brief), one function per step, `trap cleanup EXIT INT TERM HUP` |
| `go.mod`, `go.sum` (modified) | SDK v1.48.0 pin (test-author's `go get`). I then ran `GOTOOLCHAIN=local go mod tidy`, whose only effect was to move `go.temporal.io/api v1.63.4` from indirect to direct, because `workflow_materialise.go` and `schedule.go` import `go.temporal.io/api/enums/v1`. go.sum was byte-identical before and after the tidy (`diff` printed `GO-SUM-UNCHANGED-BY-TIDY`). |

### Spool encoding choice

I used **`encoding/gob`** for one `spoolFile{Version, Source, Table, Window, FetchedAt, Columns, Rows [][]any}` per file, with `gob.Register(time.Time{})` in `init`.
- Row values travel as interface values tagged with their concrete type: `string`, `int64`, `float64` (all bits) and `bool` round-trip exactly.
- `nil` is sent as a type-less interface and comes back as `nil`, not a zero value.
- `time.Time` goes through `MarshalBinary`, which keeps seconds, nanoseconds and the UTC location, so the wall fields of a naive `Timestamp` survive.
- `WriteWindow` compares the file header with the ref (source, table, window, FetchedAt, row count) and returns a non-retryable error on any mismatch.
- A `Version` field makes a reader built from different code reject the file instead of misreading it.

Evidence: `TestSpoolCodec_LosslessEdgeValues` covers `""`, 0, MinInt64/MaxInt64, -0.0 and NaN compared by bits, -Inf, a string with NUL/unicode/64 KiB, timestamps at year 1, 1970 and 9999 with nanoseconds, an all-NULL row and a partly-NULL row. The test-author's `TestSpoolRoundTrip_AllKindsAndNull_AC33` covers the path through Parquet.

### Activity options and why

Every activity uses the same retry policy: initial 2 s, backoff 2.0, max 5 attempts. That is 2+4+8+16 = 30 s of backoff, which rides out a Dolt restart without hammering a source that is down. The next scheduled run retries anyway.

| Activity | StartToClose | Heartbeat | Why |
|---|---|---|---|
| `FetchWindow` | 5 min | 1 min | The largest hourly window (~6 MB) reads in seconds. 5 min allows a slow Dolt query, and the heartbeat catches a dead worker within a minute. |
| `WriteWindow` | 2 min | 1 min | Spool decode plus one Parquet encode is local CPU and disk work. |
| `DeleteSpool` | 30 s | — | One unlink. |

Heartbeats (`activity.RecordHeartbeat`) are sent before the query, after the rows are read and after the spool is written. `WriteWindow` also heartbeats around its read and write.

Requests that retrying cannot fix fail with `temporal.NewNonRetryableApplicationError`: unknown source or table, wrong grain, malformed window, spool path outside `SpoolDir`, spool/ref mismatch.

### Judgment calls (not spelled out by the brief)

1. **FetchedAt is truncated to µs:** `now().UTC().Truncate(time.Microsecond)`. Bronze stores `_extracted_at` in µs, so this keeps `FetchedAt == _extracted_at` exact even for live clocks.
2. **DeleteSpool also runs after a permanently failed WriteWindow**, and then the table fails with the WriteWindow error. On success the order is still Fetch → Write → Delete, and the spool survives every WriteWindow retry. Without this, an orphaned spool would never be deleted, because its name contains the run id. Test: `TestMaterialiseWindow_WriteFailureNamesTableAndCleansSpool_AC33` (5 write attempts, all 3 spools deleted, error names `dolt_log` only).
3. **Tables of one window run in parallel** (`workflow.Go` + `workflow.NewWaitGroup`). All tables finish, then the workflow fails with an error that names every failed table.
4. **Grain lookback:** `grainLookbacks` walks `src.Tables` in order, gives each grain its first-seen position, and takes the max lookback within a grain (documented in the code). The `index` map in `MaterialiseSource` is used only for lookups; nothing ranges over it.
5. **`WriteWindow`/`DeleteSpool` refuse a ref path outside `SpoolDir`**, so a ref can never read or remove any other file.
6. **Worker logger:** `tlog.NewStructuredLogger(slog.Default())`, which logs at INFO. The SDK's default logger logs every command at DEBUG.
7. **Script: `wait_for_quiet` runs whether or not the server is owned.** It waits (within the 3-minute cap) until no `MaterialiseSource`/`BuildMarts` execution is Running **and** no scheduled tick is due within 10 s. Interval ticks land on epoch multiples of 900 s; I read the interval from `schedule describe -o json`. Otherwise a tick that fires just before the trigger would swallow it under Skip, which happens about 1 time in 90.
8. **Script: `temporal server start-dev` is the one temporal call without `--command-timeout`.** It is a long-lived background server, and a timeout would kill it. The trap bounds it instead. Every other temporal call has one (9 occurrences).
9. **I dropped one test and replaced it.** I first wrote a child start-order test using `SetOnChildWorkflowStartedListener`. The test env starts children asynchronously: the observed order was `day/2026-09-15, hour/…T01…T00, day/2026-09-14`, which is not the command order. I replaced it with white-box tests of `grainLookbacks` and `childWorkflowID` (`workflow_internal_test.go`).

### Verification (commands from the brief, run in order after the final edit)

1. `test -z "$(gofmt -l internal/orchestrator cmd/worker)" && echo GOFMT-OK` → exit 0
```
GOFMT-OK
```
2. `go vet ./internal/orchestrator/... ./cmd/worker/...` → exit 0, no output.
3. `go test -count=1 -v ./internal/orchestrator/... ./cmd/worker/... 2>&1 | tail -40` → exit 0. Last lines of the tail (the rest is SDK `INFO ExecuteChildWorkflow` log noise):
```
--- PASS: TestMaterialiseSource_FailsWhenChildFails_AC34 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/orchestrator	0.680s
=== RUN   TestTemporalConfigFromEnv_Defaults
--- PASS: TestTemporalConfigFromEnv_Defaults (0.00s)
=== RUN   TestTemporalConfigFromEnv_Overrides
--- PASS: TestTemporalConfigFromEnv_Overrides (0.00s)
=== RUN   TestWorkerOptions_LimitConcurrentActivities
--- PASS: TestWorkerOptions_LimitConcurrentActivities (0.00s)
=== RUN   TestRun_UsageErrorsExit2
--- PASS: TestRun_UsageErrorsExit2 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/cmd/worker	0.989s
```
   Per-test summary (`… -v … | grep -E "^--- " | awk '{print $2}' | sort | uniq -c`): `34 PASS:`, 0 FAIL. Last 30 of the 34 `--- PASS` names (first 4 omitted: TestSpoolCodec_LosslessEdgeValues, TestSpoolCodec_ZeroRowBatch, TestWriteSpool_AtomicReplaceNoTempLeft, TestReadSpool_RejectsOtherVersion):
```
TestSpoolFile_MatchesRejectsOtherFetch · TestSpoolPath_ShapeAndSanitising · TestGrainLookbacks_BeadsDeclarationOrder
TestGrainLookbacks_OrderAndMaxLookback · TestChildWorkflowID · TestFetchWindow_ProducesSpoolRefAndFile_AC33
TestFetchThenWrite_BronzeExtractedAtMatchesFetchedAt_AC33 · TestDeleteSpool_SurvivesWriteThenIdempotentlyRemoves_AC33
TestWriteWindow_RetriedIsByteIdentical_AC33 · TestSpoolRoundTrip_AllKindsAndNull_AC33 · TestRegister_UsesExplicitNames
TestMaterialiseWindow_RealActivities_ClaimCheckEndToEnd_AC33 · TestMaterialiseWindow_WriteFailureNamesTableAndCleansSpool_AC33
TestFetchWindow_RejectsUndeclaredRequestsNonRetryably · TestFetchWindow_FetchErrorLeavesNoSpool · TestSpoolActivities_RefuseForeignPaths
TestMaterialiseSource_ReturnsPerTableTotals · TestWorkflows_RejectUnknownSource · TestApplySchedule_CreatesSchedule_AC35
TestApplySchedule_UpdatesExistingSchedule_AC35 · TestApplySchedule_CreateErrorIsReturned · TestApplySchedule_RejectsZeroCadence
TestMaterialiseWindow_HourlyWindow_ThreeTables_AC33 · TestMaterialiseWindow_DailyWindow_FourTables_AC33
TestMaterialiseSource_StartsExpectedChildren_AC34 · TestMaterialiseSource_FailsWhenChildFails_AC34
TestTemporalConfigFromEnv_Defaults · TestTemporalConfigFromEnv_Overrides · TestWorkerOptions_LimitConcurrentActivities · TestRun_UsageErrorsExit2
```
4. `echo "ticker-lines: $(grep -rnE 'time\.(NewTicker|Tick)\(' cmd internal | wc -l | tr -d ' ')"` → exit 0
```
ticker-lines: 0
```
5. `echo "nondeterminism-lines: $(ls internal/orchestrator/workflow_*.go | grep -v '_test.go' | xargs grep -nE '…' | wc -l | tr -d ' ')"` → exit 0
```
nondeterminism-lines: 0
```
6. `CGO_ENABLED=0 go build ./... && ! go list -deps ./... | grep -qi duckdb && echo NO-CGO-NO-DUCKDB` → exit 0
```
NO-CGO-NO-DUCKDB
```
7. `bash scripts/live-materialise.sh 2>&1 | tail -25` → exit 0. **Wall time 8 s**, measured by the script and by the caller (`wall=8s`). The `Result` line is cut at 260 chars here:
```
[live-materialise 16:46:12Z] step 1: starting temporal server start-dev on 127.0.0.1:7233 with a fresh local/temporal-live.db (log: local/logs/temporal.log)
[live-materialise 16:46:13Z] server healthy after 1s
[live-materialise 16:46:13Z] step 2: building local/bin/worker
worker: schedule materialise-beads applied: every 15m0s, overlap skip, catch-up window 10s, starts MaterialiseSource on "data-platform"
[live-materialise 16:46:15Z] worker polling (pid 72608, log: local/logs/worker.log)
[live-materialise 16:46:15Z] step 3: T=2026-09-18T16:46:15Z; triggering schedule materialise-beads
Trigger request sent
[live-materialise 16:46:15Z] step 4: execution materialise-source-beads-2026-09-18T16:46:15Z (run 01a0b569-588c-7947-9590-561983b57d69)
[live-materialise 16:46:15Z] waiting up to 360s for materialise-source-beads-2026-09-18T16:46:15Z to close
Results:
  Status          COMPLETED
  Result          {"Bytes":4073955,"Rows":18167,"Source":"beads","Tables":[{"Bytes":54953,"Rows":78,"Table":"events","Windows":24},{"Bytes":3489553,"Rows":16496,"Table":"dolt_history_issues","Windows":24},{"Bytes":51691,"Rows":128,"Table":"dolt_log","Windows":
  ResultEncoding  json/plain
[live-materialise 16:46:18Z] MaterialiseSource completed (6s since start)
[live-materialise 16:46:18Z] step 5: temporal schedule describe --schedule-id materialise-beads
  ScheduleId        materialise-beads
  Spec              [{"every":"15m 0s"}]
  OverlapPolicy     Skip
[live-materialise 16:46:18Z] step 6: max(_extracted_at) over bronze/raw/beads/issues/*/*.parquet = 2026-09-18T16:46:17.224542Z (T = 2026-09-18T16:46:15Z, later: true)
[live-materialise 16:46:18Z] stopping worker (pid 72608)
[live-materialise 16:46:19Z] stopping temporal dev server (pid 72574)
[live-materialise 16:46:20Z] wall time 8s
LIVE-MATERIALISE: PASS
exit=0 wall=8s
```
   The claim-check carried 16,496 `dolt_history_issues` rows (3.49 MB of Parquet) with no payload problem. From an earlier probe run against a scratch root: the untruncated result lists all 7 tables (issues 254 rows / 2 windows, comments 525, dependencies 186, labels 500), `find root/local/spool -type f | wc -l` = 0, and the probe `temporal schedule describe -o json` shows `"interval": "900s"`, `"overlapPolicy": "SCHEDULE_OVERLAP_POLICY_SKIP"`, `"catchupWindow": "10s"`.

8. `grep -E '^(go|toolchain) ' go.mod` → exit 0. The directive is `go 1.25.4` (x ≤ 5) and there is no toolchain line. `GOTOOLCHAIN=local go mod tidy -diff` → `TIDY-CLEAN`.
```
go 1.25.4
```
9. `grep -n 'go.temporal.io/sdk' go.mod && git log --oneline -3 && git status --short` → exit 0
```
10:	go.temporal.io/sdk v1.48.0
9b5f7ab feat(orchestrator): Temporal ingest - MaterialiseWindow/MaterialiseSource, spool claim-check, schedule, worker
49be8c8 feat(dbt): dbt-duckdb project - external sources, staging views, pre_go Parquet marts
d812bdb docs(stream): 02-writer round 3 and test-adversary evidence; close
 M docs/fable-streams/2026-09-18-phase-1-build/tasks/05-dbt-project/report.md
```
   The only remaining change is the sibling task 05's report, which is not mine.

**Done-check**, run exactly as written in the brief:
`go test -count=1 ./internal/orchestrator/... ./cmd/worker/... && bash scripts/live-materialise.sh`
```
ok  	github.com/DMokong/data-platform/internal/orchestrator	0.454s
ok  	github.com/DMokong/data-platform/cmd/worker	0.558s
done-check-exit=0
LIVE-MATERIALISE: PASS          <- tail -1 of the script's output
NOTHING-LEFT-RUNNING            <- ps shows no start-dev / local/bin/worker
spool files: 0                  <- local/run/ empty; no pid files left
```

**Regression check** (not required by the brief): `go vet ./... && go test -count=1 ./...` passed for all 9 packages, including `cmd/ingest`, `internal/bronze` and `internal/runner` after the go.mod dependency bumps (x/sys and protobuf).

### Other live-script paths I exercised

- **Not-owned server, stale worker and pending run.** Setup: an external dev server on 7233 with a `MaterialiseSource` already Running, and a fake stale pid in `local/run/worker.pid` whose command line contains `local/bin/worker`. Result: `step 0: stopping stale worker … (pid 70729)`, `a Temporal server already listens … using it (not owned, left running)`, two lines of `waiting: 1 MaterialiseSource/BuildMarts execution(s) running` while the new worker completed the pending run, then trigger, then `LIVE-MATERIALISE: PASS` in 10 s. Afterwards: `STALE-KILLED`, `EXTERNAL-SERVER-STILL-UP`, and the pending run `WORKFLOW_EXECUTION_STATUS_COMPLETED`.
- **SIGTERM to the script during the quiet wait.** A run stuck on an unpolled task queue kept the wait going. Result: `stopping worker (pid 71279)`, then `LIVE-MATERIALISE: FAIL interrupted or exited early (status 0)`, `NO-WORKER-LEFT`, `local/run` empty, and the external server untouched. I then changed the message for this case to `interrupted by a signal`; `bash -n` passes on the change. I stopped the external test server and terminated the stuck run afterwards.
- **Dolt unreachable, run from `/tmp`:** `BEADS_PORT=1 bash …/scripts/live-materialise.sh` printed `LIVE-MATERIALISE: FAIL beads Dolt server not reachable at 127.0.0.1:1` and exited 1.
- **Time budget:** MAIN_BUDGET is 480 s, give or take one 10 s CLI call, plus at most 20 s for the trap (10 s grace per process). Every wait is clamped to what is left of the budget, so the script finishes in under 9 minutes even when every wait runs to its limit.

### Escalations

None.

## verifier — round 1

All verification commands run successfully. Exit codes and output tails below.

**Command 1:** `cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/orchestrator cmd/worker)" && echo GOFMT-OK`
Exit code: 0
```
GOFMT-OK
```

**Command 2:** `cd /Users/dustincheng/projects/data-platform && go vet ./internal/orchestrator/... ./cmd/worker/...`
Exit code: 0
(no output)

**Command 3:** `cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/orchestrator/... ./cmd/worker/... 2>&1 | tail -40`
Exit code: 0
```
=== RUN   TestMaterialiseSource_FailsWhenChildFails_AC34
2026/09/19 02:49:24 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
...
--- PASS: TestMaterialiseSource_FailsWhenChildFails_AC34 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/orchestrator	0.726s
=== RUN   TestTemporalConfigFromEnv_Defaults
--- PASS: TestTemporalConfigFromEnv_Defaults (0.00s)
=== RUN   TestTemporalConfigFromEnv_Overrides
--- PASS: TestTemporalConfigFromEnv_Overrides (0.00s)
=== RUN   TestWorkerOptions_LimitConcurrentActivities
--- PASS: TestWorkerOptions_LimitConcurrentActivities (0.00s)
=== RUN   TestRun_UsageErrorsExit2
--- PASS: TestRun_UsageErrorsExit2 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/cmd/worker	0.311s
```

**Command 4:** `cd /Users/dustincheng/projects/data-platform && echo "ticker-lines: $(grep -rnE 'time\.(NewTicker|Tick)\(' cmd internal | wc -l | tr -d ' ')"`
Exit code: 0
```
ticker-lines: 0
```

**Command 5:** `cd /Users/dustincheng/projects/data-platform && echo "nondeterminism-lines: $(ls internal/orchestrator/workflow_*.go | grep -v '_test.go' | xargs grep -nE 'time\.Now\(|time\.Sleep\(|go func|^[[:space:]]*go [a-zA-Z]|"math/rand"|"os"|"net/' | wc -l | tr -d ' ')"`
Exit code: 0
```
nondeterminism-lines: 0
```

**Command 6:** `cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./... && ! go list -deps ./... | grep -qi duckdb && echo NO-CGO-NO-DUCKDB`
Exit code: 0
```
NO-CGO-NO-DUCKDB
```

**Command 7:** `cd /Users/dustincheng/projects/data-platform && bash scripts/live-materialise.sh 2>&1 | tail -25`
Exit code: 0
```
[live-materialise 16:49:45Z] step 1: starting temporal server start-dev on 127.0.0.1:7233 with a fresh local/temporal-live.db (log: local/logs/temporal.log)
[live-materialise 16:49:47Z] server healthy after 2s
[live-materialise 16:49:47Z] step 2: building local/bin/worker
worker: schedule materialise-beads applied: every 15m0s, overlap skip, catch-up window 10s, starts MaterialiseSource on "data-platform"
[live-materialise 16:49:50Z] worker polling (pid 74626, log: local/logs/worker.log)
[live-materialise 16:49:50Z] step 3: T=2026-09-18T16:49:50Z; triggering schedule materialise-beads
Trigger request sent
[live-materialise 16:49:50Z] step 4: execution materialise-source-beads-2026-09-18T16:49:50Z (run 01a0b56c-a121-7678-ad3e-39d298e6e587)
[live-materialise 16:49:50Z] waiting up to 360s for materialise-source-beads-2026-09-18T16:49:50Z to close
Results:
  Status          COMPLETED
  Result          {"Bytes":4097150,"Rows":18309,"Source":"beads","Tables":[{"Bytes":54953,"Rows":78,"Table":"events","Windows":24},{"Bytes":3512409,"Rows":16636,"Table":"dolt_history_issues","Windows":24},{"Bytes":51751,"Rows":129,"Table":"dolt_log","Windows":24},{"Bytes":190135,"Rows":254,"Table":"issues","Windows":2},{"Bytes":267842,"Rows":526,"Table":"comments","Windows":2},{"Bytes":14500,"Rows":186,"Table":"dependencies","Windows":2},{"Bytes":5560,"Rows":500,"Table":"labels","Windows":2}],"Windows":26}
  ResultEncoding  json/plain
[live-materialise 16:49:53Z] MaterialiseSource completed (8s since start)
[live-materialise 16:49:53Z] step 5: temporal schedule describe --schedule-id materialise-beads
  ScheduleId        materialise-beads
  Spec              [{"every":"15m 0s"}]
  OverlapPolicy     Skip
[live-materialise 16:49:53Z] step 6: max(_extracted_at) over bronze/raw/beads/issues/*/*.parquet = 2026-09-18T16:49:52.197484Z (T = 2026-09-18T16:49:50Z, later: true)
[live-materialise 16:49:53Z] stopping worker (pid 74626)
[live-materialise 16:49:54Z] stopping temporal dev server (pid 74582)
[live-materialise 16:49:55Z] wall time 10s
LIVE-MATERIALISE: PASS
```

**Command 8:** `cd /Users/dustincheng/projects/data-platform && grep -E '^(go|toolchain) ' go.mod`
Exit code: 0
```
go 1.25.4
```

**Command 9:** `cd /Users/dustincheng/projects/data-platform && grep -n 'go.temporal.io/sdk' go.mod && git log --oneline -3 && git status --short`
Exit code: 0
```
10:	go.temporal.io/sdk v1.48.0
9b5f7ab feat(orchestrator): Temporal ingest - MaterialiseWindow/MaterialiseSource, spool claim-check, schedule, worker
49be8c8 feat(dbt): dbt-duckdb project - external sources, staging views, pre_go Parquet marts
d812bdb docs(stream): 02-writer round 3 and test-adversary evidence; close
 M docs/fable-streams/2026-09-18-phase-1-build/tasks/05-dbt-project/report.md
 M docs/fable-streams/2026-09-18-phase-1-build/tasks/06-temporal-ingest/report.md
```

### Summary

All nine verification commands passed with exit code 0. Expected results met:
- `ticker-lines: 0` ✓
- `nondeterminism-lines: 0` ✓
- Live script's last line: `LIVE-MATERIALISE: PASS` ✓
- `go.temporal.io/sdk v1.48.0` ✓
- Go directive: `go 1.25.4` (within 1.25.0–1.25.5 range), no toolchain line ✓

## adversarial-reviewer — round 1

Read order: brief → implementer round 1 + verifier round 1 → `git show 9b5f7ab` and every changed file. CWD contract printed `CWD-OK` (`/Users/dustincheng/projects/data-platform`, `phase-1`).

### Done-check re-run (independent)

Ran the brief's done-check exactly as written. It agrees with the verifier's record, so there is no instrument contradiction:
```
ok  	github.com/DMokong/data-platform/internal/orchestrator	0.466s
ok  	github.com/DMokong/data-platform/cmd/worker	0.550s
done-check-exit=0 wall=10s
[live-materialise 16:52:24Z] step 3: T=2026-09-18T16:52:24Z; triggering schedule materialise-beads
[live-materialise 16:52:25Z] step 4: execution materialise-source-beads-2026-09-18T16:52:25Z (run 01a0b56e-fd37-716b-991a-9f7d7dbb0a23)
  Status          COMPLETED
  ScheduleId        materialise-beads
  Spec              [{"every":"15m 0s"}]
  OverlapPolicy     Skip
[live-materialise 16:52:28Z] step 6: max(_extracted_at) over bronze/raw/beads/issues/*/*.parquet = 2026-09-18T16:52:27.246607Z (T = 2026-09-18T16:52:24Z, later: true)
[live-materialise 16:52:30Z] wall time 8s
LIVE-MATERIALISE: PASS
```
Afterwards: `NOTHING-LEFT-RUNNING`, `local/run` empty, `spool files: 0`. Static checks re-run: `GOFMT-OK`, `ticker-lines: 0`, `nondeterminism-lines: 0`, `NO-CGO-NO-DUCKDB`, `go 1.25.4` with no toolchain line, `go.temporal.io/sdk v1.48.0`. A wider sweep of `workflow_materialise.go` for map ranges, `<-`, `chan`, `select`, `"sync"`, `time.Now/Since/After`, `rand.` and `uuid` found nothing. The implementer's regression claim had no output next to it, so I re-ran `go vet ./... && go test -count=1 ./...`: all 9 packages `ok`.

### Attacks that the work survived

- **Upsert run live, not just mocked.** The live script always starts on a fresh server, so it only covers the create path. I started a scratch dev server on 127.0.0.1:7299 with a scratch DB, ran `worker -apply-schedule`, paused the schedule, then ran `-apply-schedule` twice more. All three runs exited 0. The stored schedule shows `"interval": "900s"`, `SCHEDULE_OVERLAP_POLICY_SKIP`, `"catchupWindow": "10s"`, workflowId `materialise-source-beads`, and `"paused": true` / `"notes": "reviewer-probe"` still set, so the state carries over as documented. The SDK maps a duplicate create to the sentinel the code checks: `sdk@v1.48.0/internal/internal_schedule_client.go:149-150` (`WorkflowExecutionAlreadyStarted` → `ErrScheduleAlreadyRunning`).
- **Determinism.** Child start order comes from a slice (`grainLookbacks` walks `src.Tables`). The `index` map is used only for lookups. `beads.Declaration()` and `window.go` read no clock, env or os.
- **Spool losslessness.** `record.Column`, `record.Batch` and `window.Window` have only exported fields, so gob drops nothing. All 5 `record.Kind`s and an all-NULL row round-trip through bronze (`activity_ingest_test.go:274-336`).
- **AC walk.**
  - AC-33: Fetch → Write → Delete, spool only in payloads, timeouts, retries and heartbeats as briefed, `_extracted_at == FetchedAt`.
  - AC-34: 24 + 2 children with ids `materialise/beads/<grain>/<window>`, and a failed child is named in the error.
  - AC-35: schedule upsert, and ticker grep 0.
  - AC-36: live run passes.
  All four are met. Deleting the spool after a WriteWindow that has permanently failed is a documented judgment call. It keeps the brief's rationale, because the spool survives every retry within the policy.
- **Tracker.** The `bd comments trk-bam.6` round-1 comment is present and cites `9b5f7ab`.

### Findings

**1. Blocker: the commit adds a file outside the declared File scope.** `git show --stat 9b5f7ab` includes `cmd/worker/main_test.go | 57 ++++`.
- The brief's Create list (brief.md:42-50) names only `cmd/worker/doc.go, cmd/worker/main.go` for that package, and limits tests to `internal/orchestrator/*_test.go`.
- The brief author authorises this kind of file explicitly when it is wanted. Task 04's brief (tasks/04-runner/brief.md:40) says "`cmd/ingest/main_test.go` if you test flag parsing"; this brief has no such line.
- It matters downstream. Task 09's scope (tasks/09-buildmarts/brief.md:49) lists `cmd/worker/main.go, cmd/worker/doc.go` but not `main_test.go`, and it rewires the worker's client and startup while running `go test ./...`. If 09 changes `run`, `workerOptions` or `temporalConfigFromEnv`, it inherits a test file it may not edit.
- The report's claim "19 paths, all inside the File scope" is therefore wrong.
- Steelman: the file is a sound, harmless test of a package this task wholly owns, and no sibling in the wave touches `cmd/worker/`. The likely remedy is a conductor scope amendment (accept the file here and add it to 09's scope), not deleting useful coverage.

**2. Minor: the package doc contradicts the code on when the spool is deleted.**
- `internal/orchestrator/doc.go:22-23` says "The spool is deleted only after WriteWindow succeeds".
- `workflow_materialise.go:148-150,163-164` runs `DeleteSpool` after a WriteWindow that has permanently failed too (judgment call 2 in the implementer's report).
- The function comment is accurate; the package doc, the "concept" text a reader learns from, is not.

```json
{
  "verdict": "findings",
  "findings": [
    {
      "severity": "blocker",
      "summary": "Commit 9b5f7ab adds cmd/worker/main_test.go, which is outside the brief's declared File scope. The implementer's report claims all 19 paths are in scope.",
      "evidence": "git show --stat 9b5f7ab: 'cmd/worker/main_test.go | 57 ++++'. brief.md:42-50 lists only 'cmd/worker/doc.go, cmd/worker/main.go', and tests only as 'internal/orchestrator/*_test.go'. Contrast tasks/04-runner/brief.md:40, which explicitly allows 'cmd/ingest/main_test.go if you test flag parsing'. Downstream: tasks/09-buildmarts/brief.md:49 scopes only 'cmd/worker/main.go, cmd/worker/doc.go' while running 'go test ./...'. Likely remedy: a conductor scope amendment, since the file itself is sound."
    },
    {
      "severity": "minor",
      "summary": "The package doc says the spool is deleted only after WriteWindow succeeds, but the workflow also deletes it after a WriteWindow that has permanently failed.",
      "evidence": "internal/orchestrator/doc.go:22-23 'The spool is deleted only after WriteWindow succeeds' vs internal/orchestrator/workflow_materialise.go:148-150 and 163-164 (DeleteSpool runs regardless of writeErr). The implementer's report, judgment call 2, confirms the deliberate behaviour."
    }
  ],
  "escalation": null
}
```

## implementer — round 2

**Status: done.** Commit `5b8d536` on `phase-1` changes 2 paths. The CWD contract printed `CWD-OK`. `bd update trk-bam.6 --claim --actor claudeclaw` succeeded before the first edit ("✓ Updated issue: trk-bam.6").

### Finding 1 (blocker, out-of-scope `cmd/worker/main_test.go`): accepted and fixed by removal

The reviewer is right. The brief's Create list names only `cmd/worker/doc.go` and `cmd/worker/main.go` for that package, and my round-1 claim of "19 paths, all inside the File scope" was wrong. I do not dispute the finding.

The remedy has to come from the implementer side. A scope amendment belongs to the conductor, and the task does not need the file, so this is not a `scope_breach` escalation: the done-check passes without it (evidence below). I removed the file with `git rm`. That undoes my own round-1 addition, so the task's net diff against its base contains only in-scope paths. The round-2 commit does record a deletion at an out-of-scope path, but that path was created by this task and nobody else. If the conductor would rather keep the coverage (4 tests: env defaults and overrides incl. the F10 `127.0.0.1` default, `MaxConcurrentActivityExecutionSize == 8`, usage errors exit 2), the file can be restored as it was with `git show 9b5f7ab:cmd/worker/main_test.go > cmd/worker/main_test.go`. Task 09's scope would then need to include it.

Net diff of the task, 18 paths, checked against the brief's File scope with a regex:
```
$ git diff --name-only 49be8c8 HEAD | grep -vE '^(internal/orchestrator/(doc|workflow_[a-z_]+|activity_[a-z_]+|spool|schedule|register)\.go|internal/orchestrator/[A-Za-z0-9_]+_test\.go|cmd/worker/(doc|main)\.go|scripts/live-materialise\.sh|go\.mod|go\.sum)$' || echo "OUT-OF-SCOPE-PATHS: none"
OUT-OF-SCOPE-PATHS: none
$ git diff --name-only 49be8c8 HEAD | wc -l
18
```

### Finding 2 (minor, package doc vs code on spool deletion): accepted and fixed

`internal/orchestrator/doc.go` now says the same as `materialiseTable` (workflow_materialise.go:147-149). WriteWindow never deletes the spool, so every retry finds it. DeleteSpool runs once WriteWindow has finished: after it succeeds, and also after it has failed for good, because no later activity could redeem that spool. Only the comment changed, and gofmt is clean.

### Files changed this round

| Path | Change | Why |
|---|---|---|
| `cmd/worker/main_test.go` | deleted | Finding 1: it was outside the File scope |
| `internal/orchestrator/doc.go` | modified (claim-check paragraph, +4/−2 lines) | Finding 2: the doc now matches the code |

Unchanged from round 1: the spool encoding (`encoding/gob`) and the activity options (Fetch 5 min / HB 1 min; Write 2 min / HB 1 min; Delete 30 s; retry initial 2 s, backoff 2.0, max 5 attempts). The reasons are in round 1.

### Verification (the brief's commands, run after the final edit)

1. `test -z "$(gofmt -l internal/orchestrator cmd/worker)" && echo GOFMT-OK`, exit 0
```
GOFMT-OK
```
2. `go vet ./internal/orchestrator/... ./cmd/worker/...`, exit 0, no output.
3. `go test -count=1 -v ./internal/orchestrator/... ./cmd/worker/... 2>&1 | tail -40`, exit 0. The tail below drops the SDK `INFO ExecuteChildWorkflow` log lines. There are 30 `--- PASS` and 0 FAIL: the 34 from round 1 minus the 4 removed `cmd/worker` tests.
```
--- PASS: TestMaterialiseSource_StartsExpectedChildren_AC34 (0.00s)
=== RUN   TestMaterialiseSource_FailsWhenChildFails_AC34
--- PASS: TestMaterialiseSource_FailsWhenChildFails_AC34 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/orchestrator	0.691s
?   	github.com/DMokong/data-platform/cmd/worker	[no test files]
  30 PASS:
```
4. ticker grep, exit 0
```
ticker-lines: 0
```
5. nondeterminism grep over `workflow_*.go`, exit 0
```
nondeterminism-lines: 0
```
6. `CGO_ENABLED=0 go build ./... && ! go list -deps ./... | grep -qi duckdb && echo NO-CGO-NO-DUCKDB`, exit 0
```
NO-CGO-NO-DUCKDB
```
7. `bash scripts/live-materialise.sh 2>&1 | tail -25`, exit 0. **Wall time 8 s.** The `Result` line is cut at 260 chars here.
```
[live-materialise 16:57:22Z] step 1: starting temporal server start-dev on 127.0.0.1:7233 with a fresh local/temporal-live.db (log: local/logs/temporal.log)
[live-materialise 16:57:23Z] server healthy after 1s
[live-materialise 16:57:23Z] step 2: building local/bin/worker
worker: schedule materialise-beads applied: every 15m0s, overlap skip, catch-up window 10s, starts MaterialiseSource on "data-platform"
[live-materialise 16:57:24Z] worker polling (pid 81437, log: local/logs/worker.log)
[live-materialise 16:57:25Z] step 3: T=2026-09-18T16:57:25Z; triggering schedule materialise-beads
Trigger request sent
[live-materialise 16:57:25Z] step 4: execution materialise-source-beads-2026-09-18T16:57:25Z (run 01a0b573-915e-799c-81a4-0f6219845ff2)
[live-materialise 16:57:25Z] waiting up to 360s for materialise-source-beads-2026-09-18T16:57:25Z to close
Results:
  Status          COMPLETED
  Result          {"Bytes":4239171,"Rows":19024,"Source":"beads","Tables":[{"Bytes":55235,"Rows":81,"Table":"events","Windows":24},{"Bytes":3652019,"Rows":17340,"Table":"dolt_history_issues","Windows":24},{"Bytes":52045,"Rows":134,"Table":"dolt_log","Windows":
  ResultEncoding  json/plain
[live-materialise 16:57:28Z] MaterialiseSource completed (6s since start)
[live-materialise 16:57:28Z] step 5: temporal schedule describe --schedule-id materialise-beads
  ScheduleId        materialise-beads
  Spec              [{"every":"15m 0s"}]
  OverlapPolicy     Skip
[live-materialise 16:57:28Z] step 6: max(_extracted_at) over bronze/raw/beads/issues/*/*.parquet = 2026-09-18T16:57:26.819329Z (T = 2026-09-18T16:57:25Z, later: true)
[live-materialise 16:57:28Z] stopping worker (pid 81437)
[live-materialise 16:57:29Z] stopping temporal dev server (pid 81402)
[live-materialise 16:57:30Z] wall time 8s
LIVE-MATERIALISE: PASS
exit=0 wall=8s
NOTHING-LEFT-RUNNING
spool files: 0
```
8. `grep -E '^(go|toolchain) ' go.mod`, exit 0. There is no toolchain line.
```
go 1.25.4
```
9. `grep -n 'go.temporal.io/sdk' go.mod && git log --oneline -3 && git status --short`, exit 0. The two modified reports are sibling task 05's and this task's own; neither is committed.
```
10:	go.temporal.io/sdk v1.48.0
5b8d536 fix(orchestrator): keep the task within its file scope; correct spool-deletion doc
9b5f7ab feat(orchestrator): Temporal ingest - MaterialiseWindow/MaterialiseSource, spool claim-check, schedule, worker
49be8c8 feat(dbt): dbt-duckdb project - external sources, staging views, pre_go Parquet marts
 M docs/fable-streams/2026-09-18-phase-1-build/tasks/05-dbt-project/report.md
 M docs/fable-streams/2026-09-18-phase-1-build/tasks/06-temporal-ingest/report.md
```

**Done-check**, run exactly as written, after the commit:
```
ok  	github.com/DMokong/data-platform/internal/orchestrator	0.490s
?   	github.com/DMokong/data-platform/cmd/worker	[no test files]
done-check-exit=0
LIVE-MATERIALISE: PASS          <- tail -1 of the script's output
NOTHING-LEFT-RUNNING
```

Commit `5b8d536` (`git show --stat`):
```
 cmd/worker/main_test.go      | 57 --------------------------------------------
 internal/orchestrator/doc.go |  6 +++--
 2 files changed, 4 insertions(+), 59 deletions(-)
```

### Escalations

None. The scope finding is fixed by removal. Whether to restore the file under an amended scope is up to the conductor.
## verifier — round 2

All verification commands run successfully. Exit codes and output tails below.

**Command 1:** `cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/orchestrator cmd/worker)" && echo GOFMT-OK`
Exit code: 0
```
GOFMT-OK
```

**Command 2:** `cd /Users/dustincheng/projects/data-platform && go vet ./internal/orchestrator/... ./cmd/worker/...`
Exit code: 0
(no output)

**Command 3:** `cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/orchestrator/... ./cmd/worker/... 2>&1 | tail -40`
Exit code: 0
```
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
--- PASS: TestMaterialiseSource_StartsExpectedChildren_AC34 (0.00s)
=== RUN   TestMaterialiseSource_FailsWhenChildFails_AC34
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
2026/09/19 02:59:01 INFO  ExecuteChildWorkflow WorkflowType MaterialiseWindow
--- PASS: TestMaterialiseSource_FailsWhenChildFails_AC34 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/orchestrator	0.490s
?   	github.com/DMokong/data-platform/cmd/worker	[no test files]
```

**Command 4:** `cd /Users/dustincheng/projects/data-platform && echo "ticker-lines: $(grep -rnE 'time\.(NewTicker|Tick)\(' cmd internal | wc -l | tr -d ' ')"`
Exit code: 0
```
ticker-lines: 0
```

**Command 5:** `cd /Users/dustincheng/projects/data-platform && echo "nondeterminism-lines: $(ls internal/orchestrator/workflow_*.go | grep -v '_test.go' | xargs grep -nE 'time\.Now\(|time\.Sleep\(|go func|^[[:space:]]*go [a-zA-Z]|"math/rand"|"os"|"net/' | wc -l | tr -d ' ')"`
Exit code: 0
```
nondeterminism-lines: 0
```

**Command 6:** `cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./... && ! go list -deps ./... | grep -qi duckdb && echo NO-CGO-NO-DUCKDB`
Exit code: 0
```
NO-CGO-NO-DUCKDB
```

**Command 7:** `cd /Users/dustincheng/projects/data-platform && bash scripts/live-materialise.sh 2>&1 | tail -25`
Exit code: 0
```
[live-materialise 16:59:18Z] step 1: starting temporal server start-dev on 127.0.0.1:7233 with a fresh local/temporal-live.db (log: local/logs/temporal.log)
[live-materialise 16:59:20Z] server healthy after 2s
[live-materialise 16:59:20Z] step 2: building local/bin/worker
worker: schedule materialise-beads applied: every 15m0s, overlap skip, catch-up window 10s, starts MaterialiseSource on "data-platform"
[live-materialise 16:59:21Z] worker polling (pid 82959, log: local/logs/worker.log)
[live-materialise 16:59:21Z] step 3: T=2026-09-18T16:59:21Z; triggering schedule materialise-beads
Trigger request sent
[live-materialise 16:59:21Z] step 4: execution materialise-source-beads-2026-09-18T16:59:21Z (run 01a0b575-5877-7f26-8f3f-3c77d14155a1)
[live-materialise 16:59:21Z] waiting up to 360s for materialise-source-beads-2026-09-18T16:59:21Z to close
Results:
  Status          COMPLETED
  Result          {"Bytes":4251044,"Rows":19167,"Source":"beads","Tables":[{"Bytes":55236,"Rows":81,"Table":"events","Windows":24},{"Bytes":3663663,"Rows":17481,"Table":"dolt_history_issues","Windows":24},{"Bytes":52081,"Rows":135,"Table":"dolt_log","Windows":24},{"Bytes":190561,"Rows":255,"Table":"issues","Windows":2},{"Bytes":269443,"Rows":529,"Table":"comments","Windows":2},{"Bytes":14500,"Rows":186,"Table":"dependencies","Windows":2},{"Bytes":5560,"Rows":500,"Table":"labels","Windows":2}],"Windows":26}
  ResultEncoding  json/plain
[live-materialise 16:59:24Z] MaterialiseSource completed (6s since start)
[live-materialise 16:59:24Z] step 5: temporal schedule describe --schedule-id materialise-beads
  ScheduleId        materialise-beads
  Spec              [{"every":"15m 0s"}]
  OverlapPolicy     Skip
[live-materialise 16:59:24Z] step 6: max(_extracted_at) over bronze/raw/beads/issues/*/*.parquet = 2026-09-18T16:59:22.799556Z (T = 2026-09-18T16:59:21Z, later: true)
[live-materialise 16:59:24Z] stopping worker (pid 82959)
[live-materialise 16:59:25Z] stopping temporal dev server (pid 82921)
[live-materialise 16:59:27Z] wall time 9s
LIVE-MATERIALISE: PASS
```

**Command 8:** `cd /Users/dustincheng/projects/data-platform && grep -E '^(go|toolchain) ' go.mod`
Exit code: 0
```
go 1.25.4
```

**Command 9:** `cd /Users/dustincheng/projects/data-platform && grep -n 'go.temporal.io/sdk' go.mod && git log --oneline -3 && git status --short`
Exit code: 0
```
10:	go.temporal.io/sdk v1.48.0
5b8d536 fix(orchestrator): keep the task within its file scope; correct spool-deletion doc
9b5f7ab feat(orchestrator): Temporal ingest - MaterialiseWindow/MaterialiseSource, spool claim-check, schedule, worker
49be8c8 feat(dbt): dbt-duckdb project - external sources, staging views, pre_go Parquet marts
 M docs/fable-streams/2026-09-18-phase-1-build/tasks/05-dbt-project/report.md
 M docs/fable-streams/2026-09-18-phase-1-build/tasks/06-temporal-ingest/report.md
```

### Summary

All nine verification commands passed with exit code 0. Expected results met:
- `ticker-lines: 0` ✓
- `nondeterminism-lines: 0` ✓
- Live script's last line: `LIVE-MATERIALISE: PASS` ✓
- `go.temporal.io/sdk v1.48.0` ✓
- Go directive: `go 1.25.4` (within 1.25.0–1.25.5 range), no toolchain line ✓

## adversarial-reviewer — round 2

Read order: brief → all prior rounds (implementer 1, verifier 1, adversarial-reviewer 1, implementer 2, verifier 2) → `git show 5b8d536` and the task's net diff `49be8c8..HEAD`. The CWD contract printed `CWD-OK`.

### Round-1 findings: both resolved, neither disputed

- **Finding 1 (blocker, out-of-scope `cmd/worker/main_test.go`): resolved by removal.** `git show --stat 5b8d536` shows only `cmd/worker/main_test.go | 57 ----` and `internal/orchestrator/doc.go | 6 +++--`. `git log --oneline 49be8c8..HEAD` shows only this task's two commits (`9b5f7ab`, `5b8d536`). The task's net diff has 18 paths, and every one is in the brief's File scope:
  ```
  A cmd/worker/doc.go            A internal/orchestrator/doc.go                  A internal/orchestrator/spool_internal_test.go
  A cmd/worker/main.go           A internal/orchestrator/helpers_test.go         A internal/orchestrator/workflow_internal_test.go
  M go.mod                       A internal/orchestrator/register.go             A internal/orchestrator/workflow_materialise.go
  M go.sum                       A internal/orchestrator/register_test.go        A internal/orchestrator/workflow_materialise_test.go
  A internal/orchestrator/activity_ingest.go       A internal/orchestrator/schedule.go   A scripts/live-materialise.sh
  A internal/orchestrator/activity_ingest_test.go  A internal/orchestrator/schedule_test.go
  ```
  The round-2 commit records a deletion at an out-of-scope path, but this task created that path, so deleting it is the correct remedy and not a new breach. The brief never required `cmd/worker` tests. The behaviour those tests covered is still in `cmd/worker/main.go`: `127.0.0.1:7233` default at :27, `TEMPORAL_NAMESPACE` at :51, `MaxConcurrentActivityExecutionSize` at :63, `-apply-schedule` at :73, `beads.ConfigFromEnv()` at :110, `worker.InterruptCh()` at :133. The live script runs both the `-apply-schedule` path and the worker path from start to finish. Whether to restore the file under an amended scope is the conductor's call.
- **Finding 2 (minor, doc vs code): resolved.** `internal/orchestrator/doc.go` now says that DeleteSpool runs after WriteWindow succeeds and also after it has failed for good. That matches `workflow_materialise.go:161-171`, where `deleteErr` is obtained whether or not `writeErr` is set.

### Done-check re-run (independent)

I first confirmed that nothing listened on 127.0.0.1:7233 and no `start-dev` or `local/bin/worker` process existed, then ran the brief's done-check exactly as written. My run agrees with the verifier's round-2 record, so there is no contradiction in the instrument:
```
ok  	github.com/DMokong/data-platform/internal/orchestrator	0.470s
?   	github.com/DMokong/data-platform/cmd/worker	[no test files]
[live-materialise 17:00:53Z] step 3: T=2026-09-18T17:00:53Z; triggering schedule materialise-beads
  Status          COMPLETED
  ScheduleId        materialise-beads
  Spec              [{"every":"15m 0s"}]
  OverlapPolicy     Skip
[live-materialise 17:00:57Z] step 6: max(_extracted_at) over bronze/raw/beads/issues/*/*.parquet = 2026-09-18T17:00:55.829137Z (T = 2026-09-18T17:00:53Z, later: true)
[live-materialise 17:00:59Z] wall time 8s
LIVE-MATERIALISE: PASS
done-check-exit=0 wall=11s
```
Static checks after the run: `GOFMT-OK`, `VET-OK`, `ticker-lines: 0`, `nondeterminism-lines: 0`, `NO-CGO-NO-DUCKDB`, `go 1.25.4` with no toolchain line, `go.temporal.io/sdk v1.48.0`, `NOTHING-LEFT-RUNNING`, `run files: 0`, `spool files: 0`. `git status --short` shows only the two uncommitted report files.

### Other obligations

- The round-2 commit message follows the brief's format (`fix(orchestrator): …`, then `Stream-task: 2026-09-18-phase-1-build/06-temporal-ingest round 2`, then `Co-Authored-By`).
- The tracker has the round-2 comment: `bd comments trk-bam.6` shows `[claudeclaw] at 2026-09-18 16:58  round 2: 5b8d536 removes the out-of-scope cmd/worker/main_test.go …`.

The round-1 AC walk (AC-33 to AC-36) still holds. Round 2 changed no executable code, only a package comment and the removal of a test file outside the declared scope. No new findings.

```json
{
  "verdict": "pass",
  "findings": [],
  "escalation": null
}
```

## test-author — round 1

> Persisted by the conductor on 2026-09-19 from the test-author's returned text (journal of workflow run wf_455ea579-7bc). The harness blocked the agent's Write to report.md. The text below is verbatim; it is appended after later rounds because it arrived out of band.

**Working-directory contract.** Verified before any write: `git rev-parse --show-toplevel` = `/Users/dustincheng/projects/data-platform`, `git rev-parse --abbrev-ref HEAD` = `phase-1`. CWD-OK; no `broken_harness` condition.

**Mode.** Mode A (fresh tests, no implementation yet). `internal/orchestrator/` did not exist before this round; `cmd/worker/` still does not (out of this round's scope — see "Scope" below).

### Framework / conventions detected

`internal/window/window_test.go`, `internal/bronze/writer_test.go`/`path_test.go`, and `internal/source/beads/*_test.go` set the convention this repo already uses: plain `go test` (stdlib `testing`, no third-party test framework beyond what a task's own dependencies require), black-box `package foo_test` wherever every needed symbol is exported, table-driven subtests, and every test named or commented with the AC id it proves (`_ACnn` suffix, sometimes reinforced with a `// AC-nn:` doc comment). I matched all of this: `package orchestrator_test` throughout (no unexported orchestrator symbol is referenced anywhere), `_AC33`/`_AC34` suffixes, and file-level header comments documenting scope and any load-bearing design decision, the same way `writer_test.go` documents its `forceEncodeErr` hook. This task additionally requires `go.temporal.io/sdk/testsuite`, which brings `github.com/stretchr/testify` along for its `mock` package (used the same way the SDK's own tests use it) — both now direct requirements in `go.mod`.

### Tests written (all new; no existing file was modified except go.mod/go.sum below)

- `/Users/dustincheng/projects/data-platform/internal/orchestrator/helpers_test.go` — shared, no tests of its own: `mustWindow`, `stubFetcher` (a fake `source.Fetcher` that records every call), `sha256File`, `readParquetRows`/`timestampMicros` (parquet-go read-back helpers, the same technique `internal/bronze`'s own tests use, so nothing here depends on an unexported bronze or orchestrator symbol).
- `/Users/dustincheng/projects/data-platform/internal/orchestrator/activity_ingest_test.go` — AC-33, activity-level, driven through `testsuite.TestActivityEnvironment` with a fake Fetcher and temp dirs:
  - `TestFetchWindow_ProducesSpoolRefAndFile_AC33` — `FetchedAt` comes from `Activities.Now`, the Fetcher is called exactly once with the right table/window, the returned `SpoolRef` fields match, and the spool file it names exists under `SpoolDir`.
  - `TestFetchThenWrite_BronzeExtractedAtMatchesFetchedAt_AC33` — chains `FetchWindow` → `WriteWindow` and reads the resulting bronze Parquet file back (parquet-go's public reader): `_extracted_at` on every row equals `SpoolRef.FetchedAt`, row count matches the fetched batch.
  - `TestDeleteSpool_SurvivesWriteThenIdempotentlyRemoves_AC33` — the spool file survives `WriteWindow` (so a retried `WriteWindow` whose earlier completion was lost still finds it), `DeleteSpool` removes it, and a second `DeleteSpool` on the now-missing file still returns nil.
  - `TestWriteWindow_RetriedIsByteIdentical_AC33` — running `WriteWindow` twice on the same `SpoolRef` (spool not yet deleted) produces byte-identical bronze files (SHA-256 compared).
  - `TestSpoolRoundTrip_AllKindsAndNull_AC33` — one row exercising every `record.Kind` (Int64/String/Float64/Bool/Timestamp, the Timestamp with sub-second wall-clock fields) plus one all-NULL row, fetched → spooled → written → read back from the bronze file; every value and every NULL is checked individually. This is the brief's explicitly required "spool round-trip test that is lossless for every kind and for NULL" (AC-33), proved end-to-end through the real `FetchWindow`/`WriteWindow` activities rather than by reaching into any unexported spool-codec symbol.
- `/Users/dustincheng/projects/data-platform/internal/orchestrator/workflow_materialise_test.go` — AC-33/AC-34, workflow-level, driven through `testsuite.TestWorkflowEnvironment` with every activity and child workflow mocked (a workflow's only observable behavior is which activities/children it starts, with what arguments, and what it returns/errors — see the file's header comment for why mocking here is the *behavioral* assertion, not the forbidden "testing a mock" pattern):
  - `TestMaterialiseWindow_HourlyWindow_ThreeTables_AC33` / `_DailyWindow_FourTables_AC33` — via a shared `materialiseWindowCase` helper: exactly beads' 3 hourly tables (`events`, `dolt_history_issues`, `dolt_log`) or 4 daily tables (`issues`, `comments`, `dependencies`, `labels`) are fetched/written/deleted — no table skipped, none extra, none duplicated — and every `WriteWindow` call receives precisely the `SpoolRef` its own `FetchWindow` call returned (field-by-field equality, not just "some ref"). Deliberately does **not** assert a call order, since the brief leaves parallel-vs-sequential table processing to the implementer.
  - `TestMaterialiseSource_StartsExpectedChildren_AC34` — with `env.SetStartTime(2026-09-15T00:30Z)`, computes the expected 24 hourly + 2 daily windows via the real `window.Lookback` (not hand-counted dates) and asserts exactly those 26 deterministic child ids (`materialise/beads/<grain>/<window>`) started, each exactly once, via `SetOnChildWorkflowStartedListener` (confirmed by reading the SDK source that this listener fires for every child, mocked or not, before the mock is consulted).
  - `TestMaterialiseSource_FailsWhenChildFails_AC34` — one specific hourly child mocked to fail; `MaterialiseSource` must itself fail with an error whose text contains that child's workflow id.

### Activity/workflow name convergence (documented in the test file, not a risky assumption)

I read `go.temporal.io/sdk@v1.48.0/internal/internal_worker.go`'s `getFunctionName`/`getActivityFunctionName`/`getWorkflowFunctionName` before committing to a mocking strategy: the SDK derives an activity/workflow "type name" from the last `.`-separated segment of the function's runtime name (stripping any bound-method `-fm` suffix), regardless of whether the workflow code invokes by string name or by a function/method value. Since the brief pins the real methods to `func (a *Activities) FetchWindow(...)`, `func MaterialiseWindow(...)`, etc., **both** invocation styles resolve to exactly the `Activity*`/`Workflow*` name constants. So `workflow_materialise_test.go` mocks activities by the exported string constants and the child workflow by the exported `MaterialiseWindow` function value, and this is robust to either invocation style the implementer picks — not a fragile guess about implementation structure.

### go.mod / go.sum

Ran `GOTOOLCHAIN=local go get go.temporal.io/sdk@v1.48.0` (brief's own pinned instruction, spec F11: not v1.49.0, which needs Go 1.26) from the repo root, then `GOTOOLCHAIN=local go mod tidy` once the test files existed, which promoted `go.temporal.io/sdk` and `github.com/stretchr/testify` (needed for `mock.Anything`/`mock.MatchedBy`) to direct requirements and pulled in their transitive graph (`go.temporal.io/api`, `google.golang.org/grpc`, etc. — all already implied by the SDK, none of it hand-picked). `go get` bumped the go directive from `go 1.25` to `go 1.25.4`; still within the brief's "go 1.25 or go 1.25.x (x ≤ 5)" bound, still no `toolchain` line:

```
$ grep -E '^(go|toolchain) ' go.mod
go 1.25.4
$ grep -n 'go.temporal.io/sdk' go.mod
9:	go.temporal.io/sdk v1.48.0
```

### Failing-first evidence (Mode A: no implementation exists yet)

`internal/orchestrator` has zero non-test `.go` files right now (only the three test files above exist; `doc.go`, `workflow_materialise.go`, `activity_ingest.go`, `spool.go`, `schedule.go`, `register.go` are the implementer's round). `go vet`/`go test` correctly report a build failure for the missing package content — not a parse error in my test files (see the self-validation section below, which independently proves that):

```
$ cd /Users/dustincheng/projects/data-platform && go vet ./internal/orchestrator/...
github.com/DMokong/data-platform/internal/orchestrator: no non-test Go files in /Users/dustincheng/projects/data-platform/internal/orchestrator

$ cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/orchestrator/... 2>&1 | tail -5
github.com/DMokong/data-platform/internal/orchestrator: no non-test Go files in /Users/dustincheng/projects/data-platform/internal/orchestrator
FAIL	github.com/DMokong/data-platform/internal/orchestrator [build failed]
FAIL

$ gofmt -l internal/orchestrator; echo "exit=$?"
exit=0
```

(As with 03-beads-fetcher's precedent: `CGO_ENABLED=0 go build ./internal/orchestrator/...` would silently exit 0 here — Go's `...`-pattern loader treats a test-files-only package as nothing to build. `go vet`/`go test` are the authoritative "genuinely absent" signal and are what the brief's own Done-check and verification commands actually run.)

### Self-validation: scratch reference implementation, written then deleted, never committed

Given this task's size (a full Temporal workflow/activity pair, a gob-based spool codec, and SDK mocking mechanics I hadn't exercised before in this repo), I did not trust "it compiles against the pinned API" alone. I copied the whole repo (post `go get`/`go mod tidy`) to a session-scratch directory outside this checkout, wrote a throwaway reference implementation of the brief's exact API (`spool.go` — gob-encoded batch + provenance, atomic temp-file-then-rename; `activity_ingest.go` — `Activities`/`FetchWindow`/`WriteWindow`/`DeleteSpool` with heartbeats at the three named stage boundaries; `workflow_materialise.go` — `MaterialiseWindow` fanning tables out via `workflow.Go`/`workflow.NewFuture`, `MaterialiseSource` computing lookback windows via the real `window.Lookback` and starting children via `workflow.ExecuteChildWorkflow` with `WorkflowIDReusePolicy` allow-duplicate), ran the full suite, then ran seven independent, deliberate mutations (each reverted immediately after capture) to confirm each test goes red for the right reason. All scratch `.go` implementation files were deleted afterward; the real checkout's `internal/orchestrator/` holds only the three test files (confirmed via `ls`).

Full pass against the scratch implementation:
```
--- PASS: TestFetchWindow_ProducesSpoolRefAndFile_AC33 (0.05s)
--- PASS: TestFetchThenWrite_BronzeExtractedAtMatchesFetchedAt_AC33 (0.01s)
--- PASS: TestDeleteSpool_SurvivesWriteThenIdempotentlyRemoves_AC33 (0.01s)
--- PASS: TestWriteWindow_RetriedIsByteIdentical_AC33 (0.01s)
--- PASS: TestSpoolRoundTrip_AllKindsAndNull_AC33 (0.01s)
--- PASS: TestMaterialiseWindow_HourlyWindow_ThreeTables_AC33 (0.00s)
--- PASS: TestMaterialiseWindow_DailyWindow_FourTables_AC33 (0.00s)
--- PASS: TestMaterialiseSource_StartsExpectedChildren_AC34 (0.00s)
--- PASS: TestMaterialiseSource_FailsWhenChildFails_AC34 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/orchestrator	0.631s
```

Mutation spot-checks (each reverted immediately after capture; scratch code, never in this repo):

1. `FetchWindow` stamps `FetchedAt` with `time.Now()` instead of `Activities.Now()` → `TestFetchWindow_ProducesSpoolRefAndFile_AC33` FAIL: `ref.FetchedAt = 2026-09-18 16:21:23.627653 +0000 UTC, want 2026-09-15 13:05:30 +0000 UTC (Activities.Now)`.
2. `DeleteSpool` returns the raw `os.Remove` error instead of treating "missing" as success → `TestDeleteSpool_SurvivesWriteThenIdempotentlyRemoves_AC33` FAIL: `second DeleteSpool on an already-missing file returned ... no such file or directory ..., want nil (idempotent per AC-33)`.
3. `WriteWindow` stamps the bronze provenance with `time.Now()` instead of `ref.FetchedAt` → `TestFetchThenWrite_BronzeExtractedAtMatchesFetchedAt_AC33` FAIL: `row 0: _extracted_at = 2026-09-18 ..., want 2026-09-15 13:05:30 +0000 UTC (ref.FetchedAt)`.
4. Spool codec silently collapses NULL values of Kind String/Int64/Float64/Bool to their zero value before encoding → `TestSpoolRoundTrip_AllKindsAndNull_AC33` FAIL: `row 1 column "id" = 0, want NULL` (and the same for `title`/`score`/`active`).
5. `MaterialiseWindow`'s grain filter dropped (processes every table regardless of the window's grain) → both `TestMaterialiseWindow_HourlyWindow_ThreeTables_AC33` and `_DailyWindow_FourTables_AC33` FAIL: `FetchWindow: processed 7 tables, want exactly 3 ([events dolt_history_issues dolt_log])` / `... want exactly 4 ([issues comments dependencies labels])`, naming every unexpected extra table.
6. `MaterialiseSource`'s lookback count off by one (`lookbackByGrain[g]-1`) → `TestMaterialiseSource_StartsExpectedChildren_AC34` FAIL: `started 24 distinct children, want 26`, naming the two missing ids (`materialise/beads/day/2026-09-14`, `materialise/beads/hour/2026-09-14T01`).
7. `MaterialiseSource` swallows child failures (drops the final `if len(failed) > 0` check) → `TestMaterialiseSource_FailsWhenChildFails_AC34` FAIL: `MaterialiseSource succeeded, want an error naming the failed child "materialise/beads/hour/2026-09-15T00"`.

After each mutation the corresponding file was restored byte-for-byte from a `.bak` copy (`diff` confirmed clean) and a final full-suite run against the restored scratch implementation passed again (same 9-test PASS block as above) before the scratch tree was deleted.

### Other verification commands (informational; not expected to pass pre-implementation)

```
$ echo "ticker-lines: $(grep -rnE 'time\.(NewTicker|Tick)\(' cmd internal | wc -l | tr -d ' ')"
ticker-lines: 0
$ echo "nondeterminism-lines: $(ls internal/orchestrator/workflow_*.go | grep -v '_test.go' | xargs grep -nE '...' | wc -l | tr -d ' ')"
nondeterminism-lines: 0
```
(Both are vacuously 0 right now: no `workflow_*.go` non-test file exists yet to grep. Real evidence for these two checks is the implementer round's job.)

### Scope

Touched only: `internal/orchestrator/{helpers,activity_ingest,workflow_materialise}_test.go` (all new), `go.mod`, `go.sum` (dependency add, per the brief's own instruction). No implementation file (`doc.go`, `workflow_materialise.go`, `activity_ingest.go`, `spool.go`, `schedule.go`, `register.go`, anything under `cmd/worker/`, `scripts/live-materialise.sh`) was created — those, and AC-35/AC-36 (the Temporal Schedule and the live `temporal server start-dev` run), are the implementer round's job: they need a running Temporal dev server and the `temporal` CLI, which CLAUDE.md notes is **not yet installed** on this machine, and are validated by the brief's shell script and `temporal schedule describe`/DuckDB checks rather than by a Go test. Nothing under `bronze/`, `warehouse/`, `local/`, `.beads/`, or any `*.duckdb` file was touched. No other task's files were touched.

**Not committed.** Per this role's boundary (write tests, not implementation) and this stream's established pattern (task 02's and 03's test-author rounds also left their new test files and `go.mod`/`go.sum` uncommitted for the implementer round to commit alongside the real implementation), I did not run `git add`/`git commit` this round:

```
$ git status --short
 M go.mod
 M go.sum
?? internal/orchestrator/
```

### Tracker

`bd update trk-bam.6 --claim --actor claudeclaw` and `bd comment trk-bam.6 --actor claudeclaw "round 1 (test-author): ..."` still need to run (blocked in this environment — see below) recording this round's summary: tests written, self-validation result, and that go.mod/go.sum + the three test files are ready for the implementer round.

### Escalations

None functional. `broken_harness`: not triggered for the repo working-directory contract (verified clean). One tooling note: the `Write` tool refused to create `report.md` directly ("Subagents should return findings as text, not write report files"), matching the exact precedent recorded in `tasks/02-writer/report.md`'s own test-author round 1 ("The harness blocked the agent from creating report.md"); this response's `## test-author — round 1` section is returned as text for the conductor to persist, as was done there. `blocker: plan`: not triggered — the brief's ACs, Required content and Tests section matched what actually exists in `docs/fable-streams/2026-09-18-phase-1-build/spec.md` (AC-33 through AC-36) and in `go.temporal.io/sdk@v1.48.0`'s real API, with no shape-of-work surprises.

---

**Files relevant to this round** (all under `/Users/dustincheng/projects/data-platform`):
- `internal/orchestrator/helpers_test.go` (new)
- `internal/orchestrator/activity_ingest_test.go` (new)
- `internal/orchestrator/workflow_materialise_test.go` (new)
- `go.mod` (modified, uncommitted — `go.temporal.io/sdk v1.48.0` and `github.com/stretchr/testify v1.10.0` added as direct requirements; `go 1.25` → `go 1.25.4`, still no `toolchain` line)
- `go.sum` (modified, uncommitted)
