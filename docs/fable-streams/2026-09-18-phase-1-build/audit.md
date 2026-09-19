## spec-auditor — round 1

- **Inputs (blinded):** the spec `docs/fable-streams/2026-09-18-phase-1-build/spec.md`, whose ACs are already numbered AC-01 to AC-46; the diff `git diff main...phase-1 -- . ':(exclude)docs/fable-streams'` (117 files); and the codebase at `phase-1` (HEAD 8759bc4). I did not read any task report, brief, plan or implementer note.
- **Environment (2026-09-19):**
  - The Dolt server at 127.0.0.1:49209 was up.
  - Alloy was up at 127.0.0.1:4317, but I sent it nothing. Every worker I ran used `PLATFORM_OTEL_EXPORTER=stdout`.
  - Temporal was not running. I started a temporary dev server for AC-35/36/37/40 and stopped it afterwards.
- **Isolation:** every ingest, dbt, transform and live run wrote only to copies under the session scratchpad (`$SCRATCH` below), never to the repo's own `bronze/`, `warehouse/` or `local/`. The one exception is `make check`, which rebuilds the gitignored `bin/`. Afterwards `git status --short` was empty.
- **Result:** of 46 ACs, 46 are satisfied, 0 violated and 0 unverifiable. There is no scope deficit and one minor, non-behavioural scope-surplus item.

### Per-AC verdicts

| AC | Verdict | Evidence |
|---|---|---|
| AC-01 | satisfied | `internal/window/window.go:52-94`. `Containing` normalises to UTC, `Lookback` errors for n<1 (l.60), `Range` errors when from's date is after to's (l.84). Tests `TestLookback_HourExample_AC01`, `TestLookback_DayExample_AC01`, `TestRange_Examples_AC01` (48/2), `TestContaining_NonUTCLocationSameInstant_AC01` and `TestLookback_InvalidN_AC01` pass. |
| AC-02 | satisfied | `internal/record/record.go:9-122`. `Validate` rejects ragged rows, duplicate names and kind/type mismatches. The `TestBatchValidate_*_AC02` tests pass. |
| AC-03 | satisfied | `internal/bronze/path.go:34-64` is pure: it reads no clock, env or FS. It errors on an invalid window, an unknown zone, or a segment outside `^[a-z0-9_]+$`. `path_test.go:40-200` covers hh=00, hh=23, the day boundary and 15 error cases. |
| AC-04 | satisfied | `writer_test.go:205-228` compares SHA-256 for equal inputs and a changed hash for a different `extractedAt`. Golden-bytes tests also pass. |
| AC-05 | satisfied | `writer.go:139-184` writes a `.tmp-*` file in the target dir, syncs it, checks `ctx.Err()`, then renames. `TestWrite_OverwriteIsAtomicNotAppend_AC05`, `..._EncodeFailureLeavesPriorFileIntact_AC05` (seam `forceEncodeErr`), `..._ContextCancelledBeforeRename_AC05` and `..._SyncFailure..._AC05` pass. |
| AC-06 | satisfied | `writer.go:122-127` and `:335-339` append the four provenance columns after the batch columns. `:107-111` rejects colliding names. A DuckDB read-back of a written file is shown below (E1). |
| AC-07 | satisfied | `writer.go:290-324`: Timestamp is TIMESTAMP(MICROS, isAdjustedToUTC=false) and provenance is adjusted=true. DuckDB `DESCRIBE` shows `timestamp` for the batch column and `timestamp with time zone` for `_extracted_at` (E1). `TestWrite_ZeroRowBatchWritesFullSchema_AC07` passes. |
| AC-08 | satisfied | I ran my own mutation check: all 10 deliberately wrong writers were killed by `go test ./internal/bronze/` (E2). I did not inspect the stream's own test-adversary residue record, because the blindness rule puts it out of scope. |
| AC-09 | satisfied | `internal/source/beads/config.go:23-63` sets all six env vars and their defaults. The `TestConfigFromEnv_*_AC09` tests pass. No `.env` file or credential is tracked. The only password strings are dummy values in tests. |
| AC-10 | satisfied | `declaration.go:14-28` declares source beads, cadence 15m, 3 Hour/24 tables and 4 Day/2 tables. `TestDeclaration_AC10` passes. |
| AC-11 | satisfied | `query.go:32-91`. The events bounds are converted to `BEADS_EVENTS_TZ` and the history/log bounds are UTC. `query_test.go:57-64` asserts `13:00:00`/`14:00:00`, the DST window `2026-10-04 03:00:00`/`04:00:00`, and UTC `03:00:00`/`04:00:00` (l.104, 123). |
| AC-12 | satisfied | `query.go:43-46` uses `AS OF TIMESTAMP(?)` with the window End in UTC. `fetcher.go:73-79` names the table and window on error 1146. Live tests `TestLiveFetch_IssuesSnapshotIdempotent_AC12` and `..._SnapshotBeforeFirstCommitErrors_AC12` pass with `BEADS_LIVE=1`, not skipped. |
| AC-13 | satisfied | Each statement's ORDER BY matches its spec key (`query.go:33-46`). `schema.go:16-31`: TINYINT maps to Int64, JSON to String, and an unknown type errors. The live tests `..._ClosedHourlyWindowIdempotent_AC13`, `..._EventsRowCountMatchesIndependentCount_AC13` and `..._EventsCreatedAtStaysSourceWallTime_AC13` pass. |
| AC-14 | satisfied | `fetcher.go:50-65` opens `BeginTx` with `{ReadOnly: true}` and always rolls back. A grep of the package's non-test code for `INSERT\|UPDATE\|DELETE\|REPLACE\|CREATE\|DROP\|ALTER\|CALL` finds nothing (E3). `TestFetch_ReadOnlyTxRunsBuildQueryAndDecodes_AC14` checks the driver-level `TxOptions`. |
| AC-15 | satisfied | The backfill 2026-09-14..15 exits 0, leaving 48/48/48 files for the hourly tables and 2/2/2/2 for the daily ones, with no `.tmp-*` files. It prints a per-table summary (E4). |
| AC-16 | satisfied | After a re-run the file set is identical (152 files). Per table, `EXCEPT` with `_extracted_at` excluded returns 0 rows in both directions, and `_extracted_at` did advance (E4). |
| AC-17 | satisfied | After deleting `events/dt=2026-09-15/`, a backfill of that day alone restores 24 files. Both-way `EXCEPT` returns 0/0 and the row count is 18 before and after (E4). |
| AC-18 | satisfied | `--once --now 2026-09-15T00:30:00Z` into an empty root writes exactly 24 hourly files per hourly table (`dt=2026-09-14/hh=01` .. `dt=2026-09-15/hh=00`) and days 09-14/09-15 per daily table, 80 files in total (E5). Without `--now` the command uses `time.Now()` (`cmd/ingest/main.go:89`). `TestRunOnce_LookbackWindowSelection_AC18` passes. |
| AC-19 | satisfied | `runner.go:153-188` retries up to `Attempts` times (default 3), then records the failure and continues with other windows. It returns a joined error of `table window: err` entries. `cmd/ingest/main.go:123-129` prints each failed window and exits 1. `TestBackfill_RetriedWindowEventuallySucceeds_AC19` asserts exactly 3 calls. `..._PersistentFailureNamesWindowAndLeavesOthersWritten_AC19` shows the prior file's hash unchanged. |
| AC-20 | satisfied | `dbt/profiles.yml` points at `path: warehouse/dev.duckdb`. `dbt parse` returns rc=0. A grep of `dbt/models/**/*.sql` for `read_parquet\|bronze/` finds nothing (E3). Note: the mart `.sql` configs name their output `location='warehouse/marts/<model>'`. That is outside the AC's stated grep, and AC-20 itself puts `warehouse/dev.duckdb` in profiles.yml, so the "file paths only in sources.yml" sentence is scoped to input paths. |
| AC-21 | satisfied | `dbt/models/sources.yml:9-51` declares all 7 tables via `external_location` with `hive_partitioning = true, union_by_name = true`, `loaded_at_field: _extracted_at` and warn/error thresholds. Right after `ingest --once`, `dbt source freshness` exits 0 with 8/8 PASS (E6). |
| AC-22 | satisfied | There are 7 staging views with explicit column lists. `stg_beads__events.sql:39-40` uses `timezone('Australia/Sydney', …)` and `created_date_local`, and dedups with `row_number()` on id. The others use `timezone('UTC', …)`. The staging grep for `select \*\|qualify\|join` finds nothing (E3). `information_schema` shows all 7 as VIEW. The F1 singular test passes and is non-vacuous: 141 `created` events are joined, with a max diff of 1 s (E7). |
| AC-23 | satisfied | `_staging.yml` has unique+not_null on every staging key (composite keys as `a \|\| '\|' \|\| b` expressions). `accepted_values` on status and issue_type use the default error severity, and event_type uses `severity: warn`. `dbt/tests/assert_issue_not_closed_before_created.sql` is present. `dbt build --select tag:pre_go` gives PASS=56, WARN=0, ERROR=0 (E7). Form note: the events.issue_id → `stg_beads__issue_snapshots` check is the singular test `dbt/tests/assert_events_issue_id_exists.sql`, not the generic `relationships` test. It runs at the default error severity and is limited to events created before max(snapshot `_extracted_at`) − 2 h. The generic `relationships` test is used elsewhere, in `_post_go.yml`. |
| AC-24 | satisfied | There are five `pre_go` external marts under `warehouse/marts/<model>/`. `fct_issue_events`, `mart_daily_throughput` and `mart_agent_activity_hourly` use `partition_by: dt, overwrite: true`, with a Sydney-local `dt`. `dim_issues.sql` has all 13 required columns, and `_pre_go.yml` puts unique+not_null on `issue_id`. `information_schema` shows only VIEW (12) and 0 BASE TABLE (E7). |
| AC-25 | satisfied | `dim_issues_scd2.sql` uses gaps-and-islands over (status, priority, issue_type, assignee, title), with `valid_from`, `valid_to` and `is_current`. Three singular tests pass. An independent check found 188 rows / 141 issues / 45 multi-run issues, 0 adjacent runs with identical attributes, and 0 gaps (E8). |
| AC-26 | satisfied | `rm -rf warehouse && mkdir -p warehouse/marts`, then `dbt build --select tag:pre_go` again, reproduces identical row counts for all 5 marts (E8). |
| AC-27 | satisfied | `dbt ls` lists 12 models for `tag:pre_go` (7 staging + 5 marts) and only `mart_issue_mention_drift` for `tag:post_go`. The union is the full 13-model list, and 0 names appear in both (E9). |
| AC-28 | satisfied | `internal/transform/transform.go:18-24` defines `Contract{Name, Inputs, Output, Grain, Tests}`. The registry is `internal/transform/catalog/catalog.go` (`All`, `Lookup`). `mentions.go:63-71` declares issue_mentions with inputs `[dim_issues]`, output `issue_mentions` and grain Day. The `catalog_test.go` `*_AC28` tests pass. |
| AC-29 | satisfied | `extract.go:13-43` builds a boundary regex over prefixes derived from the dim_issues ids. It handles the `(\.[0-9]+)*` child suffix, drops self-mentions, and ignores unknown prefixes. `mentions.go:87-129` emits one row per (issue_id, mentioned_issue_id, field) with `mention_count`. The table cases in `extract_test.go:43-127` cover punctuation, child ids, duplicates and multiple fields, and pass. |
| AC-30 | satisfied | `transform issue_mentions --window 2026-09-18` reads the dim_issues Parquet via parquet-go (`mentions.go:186-211`). It writes `derived/issue_mentions/dt=2026-09-18/part-0.parquet` through `transform.Materialise` → `bronze.Writer`, with provenance present. A re-run replaces the single file, and its hash changes (E10). |
| AC-31 | satisfied | `sources.yml:55-72` declares the `derived.issue_mentions` source with freshness on `_extracted_at`. `mart_issue_mention_drift.sql` is post_go and external. An independent DuckDB recomputation of "latest-dt mentions with no dependency edge in either direction" matches the mart exactly: 92 expected, 92 got, 0 missing, 0 extra (E10). |
| AC-32 | satisfied | Starting from an empty `warehouse/marts`, the pre_go build (rc 0), the transform (rc 0) and the post_go build (rc 0, PASS=6) leave `warehouse/marts/mart_issue_mention_drift/data_0.parquet` (E10). |
| AC-33 | satisfied | `workflow_materialise.go:102-174` runs FetchWindow → WriteWindow → DeleteSpool per table whose grain matches. `spool.go:88-123` writes the spool to a temp file and renames it. `spool.go:143-148` treats a missing file as a successful delete. `SpoolRef` holds only the path, row count and fetch instant (`spool.go:20-27`). The fetch and write options set StartToClose, heartbeat timeout and a retry policy (`:80-90`), and both activities call `RecordHeartbeat`. `FetchedAt` becomes `_extracted_at`. The `*_AC33` tests pass, including `TestSpoolRoundTrip_AllKindsAndNull_AC33`. Live: 80 DeleteSpool runs and 0 spool files left behind. |
| AC-34 | satisfied | `workflow_materialise.go:223-296` takes `workflow.Now` and `window.Lookback` per grain, starts children with IDs `materialise/<src>/<grain>/<window>`, and fails listing each failed child. A grep of the workflow files for `time.Now`, `go func`, `rand.`, `os.` or map ranging finds nothing (E3). `TestMaterialiseSource_StartsExpectedChildren_AC34`, `..._FailsWhenChildFails_AC34` and `..._ReturnsPerTableTotals` pass. |
| AC-35 | satisfied | `schedule.go:51-102` upserts `materialise-beads` with interval = Cadence and overlap Skip, starting MaterialiseSource. Live, `-apply-schedule` returns rc 0 both times, and `temporal schedule describe` shows the schedule (E11). The grep for `time\.(NewTicker\|Tick)\(` over `cmd` and `internal` finds nothing (E3). |
| AC-36 | satisfied | With a dev server and the worker at 127.0.0.1:7233, `temporal schedule trigger` led to `MaterialiseSource` Completed, 26 MaterialiseWindow children completed, and every lookback file (24 per hourly table, 2 per daily table) was rewritten after the trigger time T. `_extracted_at` is greater than T wherever the files have rows (E11). `make live` in an isolated repo copy also reports PASS (E12). |
| AC-37 | satisfied | `workflow_buildmarts.go:72-102` runs pre_go, then each `catalog.All()` transform, then post_go, and returns on the first failure. `activity_buildmarts.go:78-87` raises a non-retryable `DbtBuildFailed` whose message names the failed unique_ids from `run_results.json`. BuildMarts starts only after every child succeeds (`workflow_materialise.go:279-294`). The `*_AC37` testsuite tests pass. Live, `build-marts/beads/2026-09-18` Completed and every mart file is newer than T (E11). |
| AC-38 | satisfied | `internal/telemetry/setup.go:54-147` selects otlp (the default), stdout or none. The otlp default targets `127.0.0.1:4317` insecure unless an `OTEL_EXPORTER_OTLP_*ENDPOINT` is set. Shutdown flushes both providers. The worker passes `service.name=data-platform-worker` (`cmd/worker/main.go:44,132`). The `*_AC38` tests pass. |
| AC-39 | satisfied | The worker installs `temporalotel.NewTracingInterceptor` (`cmd/worker/main.go:147-153`). `metrics.go` defines `platform.rows_written` and `platform.bytes_written` (source, table, zone) and `platform.activity.duration` (activity, outcome). `dbtspans.go` names spans `dbt.<resource_type>.<name>` with file timing, error status, and the `platform.dbt.nodes` counter by status. Tests use in-memory exporters and fixture `run_results.json`, and pass. All four metrics appeared in the live stdout export (E11). |
| AC-40 | satisfied | In the live worker stdout (`PLATFORM_OTEL_EXPORTER=stdout`) I counted MaterialiseSource=1, MaterialiseWindow=52, FetchWindow=160, WriteWindow=160 and DbtBuild=4 spans, plus 13 `dbt.model.*` spans (E11). |
| AC-41 | satisfied | All 15 packages from `go list ./...` have a `doc.go`, and each package comment (`go doc`) contains one `Data-engineering concept:` line (E3). |
| AC-42 | satisfied | `CGO_ENABLED=0 go build ./...` returns rc 0. `go list -deps ./... \| grep -i duckdb` is empty. File-creating calls appear only in `internal/bronze/writer.go:144,179` and `internal/orchestrator/spool.go:93,118` (E3). Go reads marts only via parquet-go. Note: `internal/dbt/build.go:56` runs `os.MkdirAll(warehouse/marts)`. That creates only the directory for dbt (spec F5) and is not one of the listed calls. |
| AC-43 | satisfied | `gofmt -l .` prints nothing, `go vet ./...` returns rc 0, and `BEADS_LIVE=1 go test -count=1 ./...` passes all 12 test packages. The live beads tests ran and were not skipped. `make check` returns rc 0 (E3). |
| AC-44 | satisfied | `git ls-files` lists nothing under `bronze/`, `warehouse/`, `local/` or `.beads/`, and no `*.duckdb` file. The module is `github.com/DMokong/data-platform` with `go 1.25.4` and no `toolchain` line. `GOTOOLCHAIN=local go mod tidy -diff` returns rc 0 (E3). |
| AC-45 | satisfied | The decision-log rows added at `docs/data-platform-spec.md:181-182` record the F9 versions and the module pins. These match `go.mod`: parquet-go v0.32.0, mysql v1.10.1, sdk v1.48.0, contrib/opentelemetry v0.8.1 and otel family v1.46.0. |
| AC-46 | satisfied | `README.md` covers the dev server, worker, schedule, trigger, backfill, the two-pass build, the transform, and the DuckDB UI over mart Parquet. It says `make live` performs steps 1–4. `Makefile` exposes every step. I ran each one-shot target myself: `make check` in the repo, and `backfill`, `ingest-once`, `build-marts`, `freshness` and `live` in an isolated copy, all rc 0. The UI view file loads non-interactively (E12). I did not inspect the integration task report's evidence (blindness rule). |

### Evidence

**E1: sample bronze file read back by DuckDB (AC-06, AC-07).** I wrote the file with `BRONZE_SAMPLE_OUT=$SCRATCH/sample go test -run TestWriteSampleForInspection ./internal/bronze/`.
```
a_string varchar | an_int64 bigint | a_float64 double | a_bool boolean | a_timestamp timestamp
_extracted_at timestamp with time zone | _window_start timestamp with time zone
_window_end timestamp with time zone | _source_file varchar | dt date
select distinct _source_file, _window_start, _window_end:
raw/sample/kinds/dt=2026-09-15/hh=13.parquet | 2026-09-15 23:00:00+10 | 2026-09-16 00:00:00+10
row: NULL | NULL | NULL | NULL | NULL | 2026-09-15 23:05:00+10 | ...
```

**E2: writer mutation spot-check (AC-08).** Each mutant was applied to a scratch copy of `internal/bronze/writer.go`, then I ran `go test ./internal/bronze/`.
```
M1 wall-clock path                      KILLED (GoldenBytes_AC04, ProvenanceColumns_AC06, ...)
M2 keep-old-rows (skip replace)         KILLED (DeterministicBytes_AC04, OverwriteIsAtomicNotAppend_AC05)
M3 nondeterministic footer              KILLED (DeterministicBytes_AC04, GoldenBytes_AC04, ...)
M4 wrong _window_end                    KILLED (GoldenBytes_AC04, ProvenanceColumns_AC06, ...)
M5 wrong _source_file                   KILLED (DeterministicBytes_AC04, ProvenanceColumns_AC06, ...)
M6 _extracted_at from clock             KILLED (DeterministicBytes_AC04, ProvenanceColumns_AC06, ...)
M7 wrong _window_start                  KILLED (GoldenBytes_AC04, ProvenanceColumns_AC06, ...)
M8 second file beside old (append)      KILLED (DeterministicBytes_AC04, OverwriteIsAtomicNotAppend_AC05)
M9 empty _source_file value             KILLED (GoldenBytes_AC04, ProvenanceColumns_AC06, ...)
M10 _extracted_at zeroed                KILLED (DeterministicBytes_AC04, ProvenanceColumns_AC06, ...)
original restored: ok  internal/bronze
```

**E3: static gates (AC-14, AC-20, AC-22, AC-34, AC-35, AC-41 to AC-44).**
```
gofmt -l . -> (empty) rc=0 ; CGO_ENABLED=0 go build ./... rc=0 ; go vet ./... rc=0
go list -deps ./... | grep -i duckdb -> rc=1 (no match)
BEADS_LIVE=1 go test -count=1 ./... -> all 12 test packages ok (beads live tests PASS, none SKIP)
grep write-SQL in internal/source/beads non-test -> rc=1 ; grep read_parquet|bronze/ in dbt/models/**/*.sql -> rc=1
grep -rniE 'select \*|qualify|join' dbt/models/staging -> rc=1 ; grep time.(NewTicker|Tick)( cmd internal -> rc=1
grep time.Now|go func|rand.|os. in workflow_*.go -> rc=1
file-creating calls (non-test): internal/bronze/writer.go:144,179 ; internal/orchestrator/spool.go:93,118
doc.go: 15/15 packages carry one "Data-engineering concept:" line in go doc
git ls-files bronze|warehouse|local|.beads|*.duckdb -> none ; go 1.25.4 ; no toolchain ; tidy -diff rc=0
make check -> fmt-check OK, vet, test-live ok, build, doc.go OK, file-writer OK, hygiene OK (rc=0)
```

**E4: backfill, idempotent re-run and replay (AC-15, AC-16, AC-17).** Root: `$SCRATCH/ingest`.
```
table=events windows=48 rows=18 ... dolt_history_issues windows=48 rows=3340 ... dolt_log windows=48 rows=30
table=issues windows=2 rows=220 ... comments 2/479 ... dependencies 2/154 ... labels 2/473   rc=0
files: events 48, dolt_history_issues 48, dolt_log 48, issues 2, comments 2, dependencies 2, labels 2, tmp=0
rerun rc=0 ; file set identical (152 files)
EXCEPT excl _extracted_at (a-b|b-a|rows): events 0|0|18, dolt_history_issues 0|0|3340, dolt_log 0|0|30,
  issues 0|0|220, comments 0|0|479, dependencies 0|0|154, labels 0|0|473
max _extracted_at: copy 07:07:42.79+10 -> rerun 07:07:57.83+10 (changed, so equality is not vacuous)
replay: rm events/dt=2026-09-15 ; backfill 2026-09-15 rc=0 ; files restored 24 ; EXCEPT 0|0 ; rows 18|18
```

**E5: lookback run (AC-18).** `--once --now 2026-09-15T00:30:00Z` into the empty root `$SCRATCH/ingest18`.
```
events n=24 first=dt=2026-09-14/hh=01 last=dt=2026-09-15/hh=00 (same for dolt_history_issues, dolt_log)
issues/comments/dependencies/labels: dt=2026-09-14/part-0.parquet dt=2026-09-15/part-0.parquet
total files 80 ; rc=0
```

**E6: freshness right after `ingest --once` (AC-21).**
```
PASS freshness of beads.{comments,dependencies,dolt_history_issues,dolt_log,events,issues,labels}
PASS freshness of derived.issue_mentions ; freshness rc=0
```

**E7: pre_go build, views only, F1 test (AC-22, AC-23, AC-24).** The source is a copy of the repo's bronze in `$SCRATCH/root`.
```
dbt build --select tag:pre_go rc=0 ; Done. PASS=56 WARN=0 ERROR=0 SKIP=0
PASS accepted_values ... event_type (warn) / issue_type / status ; PASS assert_events_issue_id_exists
PASS assert_issue_not_closed_before_created ; PASS assert_event_created_matches_issue_created ; PASS assert_scd2_*
information_schema: VIEW 12 (no BASE TABLE) ; stg_beads__* x7 = VIEW
created events joined to dim_issues: 141, max |diff| = 1 s
```

**E8: SCD2 shape and rebuild from bronze (AC-25, AC-26).**
```
before: dim_issues 141 | dim_issues_scd2 188 | fct_issue_events 692 | mart_daily_throughput 18 | mart_agent_activity_hourly 182
rm -rf warehouse && mkdir -p warehouse/marts ; dbt build tag:pre_go rc=0 PASS=56 ; row counts identical after rebuild
scd2: rows 188, issues 141, multi-run issues 45, non-maximal adjacent runs 0, gaps 0
```

**E9: two-pass tags (AC-27).**
```
pre_go (12): dim_issues dim_issues_scd2 fct_issue_events mart_agent_activity_hourly mart_daily_throughput + 7 stg_beads__*
post_go (1): mart_issue_mention_drift ; all: 13 ; in both: 0 ; union==all: yes
```

**E10: transform and two-pass build (AC-30, AC-31, AC-32).**
```
empty warehouse/marts ; pass1 rc=0 ; transform rc=0 rows=142 path=derived/issue_mentions/dt=2026-09-18/part-0.parquet
sha256 393d12f8... -> rerun rc=0 -> 68f18c8a... (single file in dir) ; pass2 rc=0 PASS=6 -> mart_issue_mention_drift/data_0.parquet
derived provenance: _source_file=derived/issue_mentions/dt=2026-09-18/part-0.parquet, window 2026-09-18 UTC day
mentions: child ids 15, self 0, total 142 ; drift independent recompute: expected 92 got 92 missing 0 extra 0
```

**E11: live Temporal run (AC-35, AC-36, AC-37, AC-39, AC-40).** A temporary dev server ran on 127.0.0.1:7233 with the worker `-root $SCRATCH/root` and `PLATFORM_OTEL_EXPORTER=stdout`.
```
-apply-schedule rc=0 ; again rc=0 ; describe: ScheduleId materialise-beads, Spec every 15m, OverlapPolicy Skip,
  action Workflow MaterialiseSource, TaskQueue data-platform
T=2026-09-18T21:13:03Z ; trigger -> materialise-source-beads-2026-09-18T21:13:04Z Completed
build-marts/beads/2026-09-18 Completed ; MaterialiseWindow Completed = 26
files newer than T: 24 per hourly table, 2 per daily table ; rows with _extracted_at > T in all 7 tables
marts/derived newer than T: dim_issues, dim_issues_scd2, fct_*, mart_*, mart_issue_mention_drift, derived/issue_mentions
spool leftovers: 0
worker stdout spans: RunWorkflow:MaterialiseSource 1, *MaterialiseWindow 52, *FetchWindow 160, *WriteWindow 160,
  *DbtBuild 4, dbt.model.* 13, dbt.test.* 49
metrics: platform.activity.duration, platform.bytes_written, platform.dbt.nodes, platform.rows_written
```

**E12: Makefile targets in an isolated repo copy (AC-36, AC-46).** The copy is at `$SCRATCH/fullrepo`, including bronze.
```
make backfill rc=0 (FROM=2026-08-21..TO=today; events 696 windows ... labels 29 windows)
make ingest-once rc=0 ; make build-marts rc=0 (pre_go PASS=56, transform rows=142, post_go PASS=6)
make freshness rc=0 (8 PASS)
make live rc=0: schedule applied; trigger; MaterialiseSource COMPLETED; BuildMarts COMPLETED;
  issues max(_extracted_at) later than T: true; dim_issues and drift mart modified after T;
  spans MaterialiseSource=1 MaterialiseWindow=52 FetchWindow=160 WriteWindow=160 DbtBuild=4 RunTransform=2 dbt.model.*=13
  LIVE-MATERIALISE: PASS
duckdb -c ".read marts-views.sql" -> dim_issues 141, mart_issue_mention_drift 92
```

### Scope surplus

- **`CLAUDE.md`, Toolchain section (non-behavioural).** The diff rewrites the paragraph to say that dbt, DuckDB and Temporal are installed, and adds the install commands. No AC covers edits to `CLAUDE.md`. It is documentation only and does not affect runtime behaviour.

### Scope deficit

- None. Every AC from AC-01 to AC-46 has an implementation in the diff or codebase, as shown in the table.

`$SCRATCH` = `/private/tmp/claude-501/-Users-dustincheng-projects-data-platform/2c6992a9-90aa-4e2e-80cd-52b1465517f7/scratchpad`

## fable — round 1

Conductor triage of the final-audit result (2026-09-19T01:22:47Z, **opus tier, emulation mode**). This is triage only; the
whole-branch final review is handed to the Fable session. Workflow run `wf_d7a92be7-785`: 231 agents, the
blinded opus spec-auditor plus a 5-voter haiku refute panel per AC.

**Cold read:** 46/46 ACs `satisfied`. Deficit: none. Surplus: one item, the `CLAUDE.md` Toolchain paragraph edit.
That edit was in task 10's brief scope, which was docs-only, so no AC covers it by design.

**Refute-panel findings** (5), with the conductor's ruling on each:

| AC | refuted | ruling | evidence |
|---|---|---|---|
| AC-41 | 5/5 | **Not a branch defect.** Refute voters left untracked scratch packages `cmd/inspect`, `cmd/readdata` and `cmd/trace` (no doc.go, unformatted) in the working tree at 11:20. They were never committed. | After deleting them: `go list ./...` = 15 packages, every doc.go has the concept line, and `make check` exits 0 with `doc.go: OK` |
| AC-43 | 5/5 | **Not a branch defect.** Same scratch packages. | After cleanup: `gofmt -l .` prints nothing; `make check` exits 0 (fmt-check, vet, test-live, build all OK) |
| AC-45 | 5/5 | **Not a defect** (the voters conflated two things). The decision log records the installed *toolchain* go1.25.5. `go.mod`'s `go 1.25.4` is the *minimum language version*, raised by Temporal SDK v1.48.0's own `go 1.25.4` (F11). | `go.mod` line 3; F11. Optional clarity edit for the final review: name both numbers in the module-pin row. |
| AC-24 | 4/5 | **Not a defect.** Evidence imprecision: the auditor counted 12 views, but after the two-pass build there are 13, because the post_go mart's view exists. The AC's actual claim, zero `BASE TABLE`, holds. | `information_schema`: 13 VIEW, 0 BASE TABLE |
| AC-21 | 3/5 | **Not a defect.** One voter nitpicked the evidence line range. Another doubted dbt-duckdb's `{name}` substitution, but the conductor's own freshness run (task 05 acceptance) and the auditor's run both exit 0 with every source PASS. | tasks/05 report and the conductor re-run |

**Audit side effects cleaned by the conductor:** voters also left `internal/bronze/writer.go.bak` and `.orig`
(both byte-identical to HEAD), `internal/window/window_edge_cases_test.go` and a 43 MB `worker` binary at the repo
root. All were untracked and all were deleted. No tracked file was modified: `git status` showed only
`audit.md` changed.
