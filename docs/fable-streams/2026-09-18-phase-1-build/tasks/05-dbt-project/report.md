## implementer — round 1

### What I changed

Working directory verified: `git rev-parse --show-toplevel` = `/Users/dustincheng/projects/data-platform`,
`git rev-parse --abbrev-ref HEAD` = `phase-1` (CWD-OK).

Created (all inside the brief's File scope, commit `49be8c8`):

- `dbt/dbt_project.yml` — project `data_platform`, profile `data_platform`; `models/staging` →
  `+materialized: view, +tags: [pre_go]`; `models/marts/pre_go` → `+materialized: external,
  +tags: [pre_go]`; `models/marts/post_go` → `+materialized: external, +tags: [post_go]` (no tags on
  the `marts` parent, per AC-27).
- `dbt/profiles.yml` — target `dev`, `type: duckdb`, `path: warehouse/dev.duckdb`, `threads: 4`, no secrets.
- `dbt/models/sources.yml` — source `beads`, `meta.external_location =
  "read_parquet('bronze/raw/beads/{name}/*/*.parquet', hive_partitioning = true, union_by_name =
  true)"`, `loaded_at_field: _extracted_at`, seven tables (events, dolt_history_issues, dolt_log,
  issues, comments, dependencies, labels) each with a freshness block: snapshot tables (issues,
  comments, dependencies, labels) warn 6h / error 48h; event-log tables (events,
  dolt_history_issues, dolt_log) warn 48h / error 30d, because a quiet-source window still writes a
  file but carries no row (hence no `_extracted_at` value) — documented in the yml description. This
  is the only file in the project containing `read_parquet` or a bronze path (AC-20; grep-verified,
  see below).
- `dbt/models/staging/*.sql` (7) + `dbt/models/staging/_staging.yml` — one view per source table,
  explicit column lists, casts/renames only, no joins, no `select *`. See "Design notes" below for
  the per-model column choices, the events de-dup, and the two timezone conversions.
- `dbt/models/marts/pre_go/*.sql` (5) + `dbt/models/marts/pre_go/_pre_go.yml` — `dim_issues`,
  `dim_issues_scd2`, `fct_issue_events`, `mart_daily_throughput`, `mart_agent_activity_hourly`, all
  `external` under `warehouse/marts/<model>/`.
- `dbt/models/marts/post_go/.gitkeep` — empty folder for task 07's mart (git tracks files, not
  directories, so this keeps the folder in the repo; not itemised in the brief's Create list, but
  needed to satisfy the brief's own Goal text "sets up the post_go folder that task 07's mart will
  live in").
- `dbt/tests/*.sql` (6 singular tests) — the two required by the brief
  (`assert_issue_not_closed_before_created.sql`, `assert_event_created_matches_issue_created.sql`),
  the three SCD2 invariants (`assert_scd2_one_current_row_per_issue.sql`,
  `assert_scd2_valid_to_null_iff_current.sql`, `assert_scd2_no_overlapping_intervals.sql`), and the
  events→issue_snapshots relationships check (`assert_events_issue_id_exists.sql` — see "Relationships
  test" below for why this is a singular test rather than the built-in `relationships` generic test).

Modified:
- `.gitignore` — added `dbt/.user.yml` (dbt-core writes its anonymous-usage-stats cookie file to
  `--profiles-dir`, which is `dbt/` here; confirmed it appears after the first successful command
  and is now correctly `!!`-ignored, verified with `git status --short --ignored dbt/`).

No `dbt/macros/*.sql`: not needed in the final design (see "Relationships test" below for why an
earlier macro-based attempt was abandoned).

No Go files touched.

### Prerequisite data (bronze backfill)

`go run ./cmd/ingest --source beads --from 2026-08-21 --to 2026-09-18` (today's UTC date via
`date -u +%F`; bronze already partially covered this range from a prior task's evidence, this
backfill re-fetched and overwrote the whole range per spec's idempotent-rerun rule): **exit 0, wall
time 83s**.

```
table=events windows=696 rows=651 bytes=1149656
table=dolt_history_issues windows=696 rows=63326 bytes=13044571
table=dolt_log windows=696 rows=889 bytes=808575
table=issues windows=29 rows=2382 bytes=1986666
table=comments windows=29 rows=4744 bytes=2518316
table=dependencies windows=29 rows=1874 bytes=178796
table=labels windows=29 rows=4959 bytes=72696
EXIT=0 WALL=83s
```

### Model list with row counts

Staging (7 views, from `warehouse/dev.duckdb` after `go run ./cmd/ingest --source beads --once`
added one more day's rows on top of the backfill):

```
stg_beads__events 656
stg_beads__issue_snapshots 2383
stg_beads__issue_history 64109
stg_beads__commits 895
stg_beads__comment_snapshots 4745
stg_beads__dependency_snapshots 1874
stg_beads__label_snapshots 4959
```

pre_go marts (5, from `warehouse/marts/<model>/**/*.parquet`, matching before/after the rebuild-from-bronze check):

```
dim_issues 131
dim_issues_scd2 178
fct_issue_events 656
mart_daily_throughput 18
mart_agent_activity_hourly 174
```

`dim_issues`: 131 issues, 88 with a non-null `cycle_time_hours` (i.e. closed).

### Design notes

- **Timezone conversion.** `events.created_at` (naive Australia/Sydney wall time, F1) converts with
  `timezone('Australia/Sydney', created_at)`. Every other source timestamp is naive UTC wall time
  and converts with `timezone('UTC', created_at)` — **not** a bare `::timestamptz` cast, because
  DuckDB's session `TimeZone` on this machine defaults to `Australia/Sydney` (from the OS), so a
  bare cast of a UTC-naive value would silently apply the wrong offset. Verified live:
  `'2026-01-15 10:00:00'::timestamp::timestamptz` → `2026-01-15 10:00:00+11` (wrong: treats it as
  Sydney) vs `timezone('UTC', '2026-01-15 10:00:00'::timestamp)` → `2026-01-15 21:00:00+11` (right:
  the same UTC instant, displayed in session tz).
- **`stg_beads__events` de-dup.** `row_number() over (partition by id order by _extracted_at desc)`
  computed alongside the rename (not via `QUALIFY`, which is DuckDB-only and forbidden in staging),
  filtered to `rn = 1` in the final select. `created_date_local` is `cast(created_at_sydney_naive as
  date)` — taken from the *naive* source column, not from the converted `timestamptz`, so it is
  never re-interpreted through a different timezone.
- **`dependency_snapshots` key choice: `(dependency_id, snapshot_date)`.** Verified against the live
  bronze data before writing the model: `select count(*), count(distinct id), count(distinct id ||
  '|' || dt) from read_parquet('bronze/raw/beads/dependencies/*/*.parquet', hive_partitioning=true,
  union_by_name=true)` → `1874, 99, 1874` (every (id, dt) pair is unique; `id` alone repeats because
  a dependency edge persists across daily snapshots once created) and 0 rows with `id is null`.
  Schema evolution check: `parquet_schema()` on the earliest (`dt=2026-08-21`) and latest
  (`dt=2026-09-18`) files showed the identical 15-column schema, so no column-presence issue to
  document beyond that. This is recorded in `_staging.yml`'s model description.
- **Relationships test.** `stg_beads__events.issue_id -> stg_beads__issue_snapshots.issue_id`, error
  severity, is **`dbt/tests/assert_events_issue_id_exists.sql`**, not the built-in `relationships`
  generic test with a `config.where`. I first tried the generic-test form (`config: {where: "created_at
  < (select max(_extracted_at) - interval '2 hours' from {{ ref('stg_beads__issue_snapshots') }})"}`)
  and hit a hard compilation error against this install:
  > `The stg_beads__events.issue_id column's "relationships" test references an undefined macro in
  > its where configuration argument. The macro 'ref' is undefined. Please note that the generic
  > test configuration parser currently does not support using custom macros to populate
  > configuration values.`

  This is dbt-core 1.12.5 behaviour, confirmed live, not a design choice I could route around within
  the generic-test yml config — even the built-in `ref()` is rejected inside a `where:` config value.
  A singular test gets full Jinja (and a correct DAG edge via `ref()`), so it is the correct way to
  express the same check. The cutoff logic: `where issue_id is not null and created_at < (select
  max(_extracted_at) - interval '2 hours' from stg_beads__issue_snapshots) and not exists (... in
  stg_beads__issue_snapshots)`. `stg_beads__issue_snapshots` is a **daily** snapshot; its row for the
  still-open day is fetched `AS OF` the Fetcher's own extraction instant, because Dolt resolves a
  future `AS OF` instant to "latest state" (spec F2) — so that row's `_extracted_at` is exactly the
  moment its coverage catches up to. Restricting to events created before that instant (less a
  2-hour margin) means every tested event's issue is guaranteed to already be in the snapshot; this
  is the race the brief names ("an event whose issue was created after that day's snapshot was
  fetched"). Documented in both the test file and a comment on `_staging.yml`'s `issue_id` column.
- **`accepted_values`/`relationships`/`unique` syntax.** Used dbt 1.9+ `data_tests:` and
  `arguments:` nesting throughout (not the legacy top-level-args form) after `dbt parse` flagged
  `MissingArgumentsPropertyInGenericTestDeprecation` warnings; `column_name:` for composite-key
  `unique`/`not_null` tests did not need `arguments:` nesting (no deprecation fired for it).
- **SCD2 (`dim_issues_scd2`).** Standard gaps-and-islands: a `lag()`-based change flag over
  `(status, priority, issue_type, assignee, title)` per issue ordered by `snapshot_date`, a running
  `sum()` of that flag numbers each contiguous run, then `min(snapshot_date)` per run is
  `valid_from` and `lead(valid_from)` (next run's start) is `valid_to` / `is_current`. The three
  invariants (one current row per issue, `valid_to` null iff current, no overlapping intervals) are
  the three singular tests listed above, since they compare rows across an issue's runs rather than
  testing one column.
- **`select *` scope fix (self-caught during verification, not a reviewer finding).** My first draft
  of every staging model used a `select * from {{ source(...) }}` passthrough CTE for readability.
  The brief's own verification grep (`grep -rniE 'select \*|qualify|join' dbt/models/staging`) is a
  per-line textual check and doesn't distinguish an internal passthrough CTE from a real
  under-specified projection — it flagged all 7 files. Restructured every staging model so the
  source relation is referenced directly in the final (or only) CTE's `FROM` clause with an explicit
  column list, and folded `stg_beads__events`'s de-dup `row_number()` into the same CTE that renames
  columns, rather than a second `select *, row_number() ...` step. Re-verified `staging-violations: 0`
  after the fix.

### Verification commands

```
$ cd /Users/dustincheng/projects/data-platform && ls bronze/raw/beads/issues | wc -l | tr -d ' '
29
```

```
$ dbt parse --project-dir dbt --profiles-dir dbt 2>&1 | tail -5
[WARNING]: Configuration paths exist in your dbt_project.yml file which do not apply to any resources.
There are 1 unused configuration paths:
- models.data_platform.marts.post_go
Performance info: dbt/target/perf_info.json
```
(dbt exits 0; this one warning is explicitly called out as acceptable in the brief: "The folder may
be empty until task 07; if dbt warns about an unused config path, that is acceptable.")

```
$ echo "path-leaks: $(grep -rlE 'read_parquet|bronze/' dbt/models --include='*.sql' | wc -l | tr -d ' ')"; \
  echo "staging-violations: $(grep -rniE 'select \*|qualify|join' dbt/models/staging | wc -l | tr -d ' ')"
path-leaks: 0
staging-violations: 0
```

```
$ mkdir -p local/tmp && go run ./cmd/ingest --source beads --once >/dev/null; echo "ingest-exit=$?"; \
  dbt source freshness --project-dir dbt --profiles-dir dbt > local/tmp/freshness.log 2>&1; \
  echo "freshness-exit=$?"; tail -12 local/tmp/freshness.log
ingest-exit=0
freshness-exit=0
Concurrency: 4 threads (target='dev')
1 of 7 START freshness of beads.comments ... [RUN]
2 of 7 START freshness of beads.dependencies ... [RUN]
1 of 7 PASS freshness of beads.comments ... [PASS in 0.03s]
5 of 7 START freshness of beads.events ... [RUN]
2 of 7 PASS freshness of beads.dependencies ... [PASS in 0.03s]
6 of 7 START freshness of beads.issues ... [RUN]
6 of 7 PASS freshness of beads.issues ... [PASS in 0.04s]
7 of 7 START freshness of beads.labels ... [RUN]
7 of 7 PASS freshness of beads.labels ... [PASS in 0.02s]
4 of 7 PASS freshness of beads.dolt_log ... [PASS in 0.11s]
3 of 7 PASS freshness of beads.dolt_history_issues ... [PASS in 0.13s]
5 of 7 PASS freshness of beads.events ... [PASS in 0.10s]
Finished running 7 sources in 0 hours 0 minutes and 0.19 seconds (0.19s).
```
All 7 sources PASS (no WARN, no ERROR).

```
$ mkdir -p warehouse/marts && dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go 2>&1 | tail -15
...
55 of 56 OK created sql external model main.mart_daily_throughput .............. [OK in 0.02s]
56 of 56 START test not_null_mart_daily_throughput_dt .......................... [RUN]
56 of 56 PASS not_null_mart_daily_throughput_dt ................................ [PASS in 0.01s]
Finished running 5 external models, 44 data tests, 7 view models in 0 hours 0 minutes and 0.78 seconds.
Completed successfully
Done. PASS=56 WARN=0 ERROR=0 SKIP=0 NO-OP=0 REUSED=0 TOTAL=56
```

```
$ duckdb -readonly warehouse/dev.duckdb -c "select table_type, count(*) from information_schema.tables group by 1"
table_type  count_star()
VIEW        12
```
Zero `BASE TABLE` rows.

```
$ find warehouse/marts -maxdepth 1 -mindepth 1 -type d | sort && \
  duckdb -c "select count(*) issues, sum(case when cycle_time_hours is not null then 1 else 0 end) closed from read_parquet('warehouse/marts/dim_issues/**/*.parquet')"
warehouse/marts/dim_issues
warehouse/marts/dim_issues_scd2
warehouse/marts/fct_issue_events
warehouse/marts/mart_agent_activity_hourly
warehouse/marts/mart_daily_throughput
issues  closed
131     88
```

```
$ mkdir -p local/tmp && for m in dim_issues dim_issues_scd2 fct_issue_events mart_daily_throughput mart_agent_activity_hourly; do
    echo "$m $(duckdb -noheader -list -c "select count(*) from read_parquet('warehouse/marts/$m/**/*.parquet')")"
  done > local/tmp/mart-counts-before.txt && rm -rf warehouse && mkdir -p warehouse/marts && \
  dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go > local/tmp/rebuild.log 2>&1; echo "rebuild-exit=$?"; \
  for m in dim_issues dim_issues_scd2 fct_issue_events mart_daily_throughput mart_agent_activity_hourly; do
    echo "$m $(duckdb -noheader -list -c "select count(*) from read_parquet('warehouse/marts/$m/**/*.parquet')")"
  done > local/tmp/mart-counts-after.txt; cat local/tmp/mart-counts-after.txt; \
  diff local/tmp/mart-counts-before.txt local/tmp/mart-counts-after.txt && echo REBUILD-SAME
rebuild-exit=0
dim_issues 131
dim_issues_scd2 178
fct_issue_events 656
mart_daily_throughput 18
mart_agent_activity_hourly 174
REBUILD-SAME
```
(`rebuild.log` tail: `Done. PASS=56 WARN=0 ERROR=0 SKIP=0 NO-OP=0 REUSED=0 TOTAL=56`.)

```
$ dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:pre_go --output name | sort
dim_issues
dim_issues_scd2
fct_issue_events
mart_agent_activity_hourly
mart_daily_throughput
stg_beads__comment_snapshots
stg_beads__commits
stg_beads__dependency_snapshots
stg_beads__events
stg_beads__issue_history
stg_beads__issue_snapshots
stg_beads__label_snapshots
```
Exactly the 7 staging models and 5 pre_go marts (12 total).

```
$ git log --oneline -3 && git status --short
49be8c8 feat(dbt): dbt-duckdb project - external sources, staging views, pre_go Parquet marts
d812bdb docs(stream): 02-writer round 3 and test-adversary evidence; close
2105561 docs(stream): wave 3 evidence (04-runner accepted)
```
(git status --short is empty except this task's own uncommitted `report.md`, which is intentionally
not committed per the brief.)

### Done-check

```
$ cd /Users/dustincheng/projects/data-platform && mkdir -p warehouse/marts && \
  dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go && \
  [ "$(duckdb -readonly -noheader -list warehouse/dev.duckdb -c "select count(*) from information_schema.tables where table_type = 'BASE TABLE'")" = 0 ] && echo DONE
DONE
```

### Disputes of prior findings

None — this is round 1, no prior findings exist.

### Escalations

None.

### Commit

`49be8c8` — `feat(dbt): dbt-duckdb project - external sources, staging views, pre_go Parquet marts`
on branch `phase-1`.

### Tracker

Claimed `trk-bam.5` before the first edit. Commented after committing:
`bd comment trk-bam.5 --actor claudeclaw "round 1: dbt-duckdb project landed ... Commit 49be8c8."`
Story left open (conductor owns terminal state); no labels added or removed.

## verifier — round 1

### Verification commands

**Command 1:** `cd /Users/dustincheng/projects/data-platform && ls bronze/raw/beads/issues | wc -l | tr -d ' '`
- Exit code: 0
- Output: `29`

**Command 2:** `cd /Users/dustincheng/projects/data-platform && dbt parse --project-dir dbt --profiles-dir dbt 2>&1 | tail -5`
- Exit code: 0
- Output (tail):
```
[0m15:58:19  Registered adapter: duckdb=1.11.0
[0m15:58:20  [[33mWARNING[0m]: Configuration paths exist in your dbt_project.yml file which do not apply to any resources.
There are 1 unused configuration paths:
- models.data_platform.marts.post_go
[0m15:58:20  Performance info: dbt/target/perf_info.json
```

**Command 3:** `cd /Users/dustincheng/projects/data-platform && echo "path-leaks: $(grep -rlE 'read_parquet|bronze/' dbt/models --include='*.sql' | wc -l | tr -d ' ')"; echo "staging-violations: $(grep -rniE 'select \*|qualify|join' dbt/models/staging | wc -l | tr -d ' ')"`
- Exit code: 0
- Output:
```
path-leaks: 0
staging-violations: 0
```

**Command 4:** `cd /Users/dustincheng/projects/data-platform && mkdir -p local/tmp && go run ./cmd/ingest --source beads --once >/dev/null; echo "ingest-exit=$?"; dbt source freshness --project-dir dbt --profiles-dir dbt > local/tmp/freshness.log 2>&1; echo "freshness-exit=$?"; tail -12 local/tmp/freshness.log`
- Exit code: 0
- Output (tail):
```
ingest-exit=0
freshness-exit=0
[0m15:58:33  2 of 7 PASS freshness of beads.dependencies .................................... [[32mPASS[0m in 0.03s]
[0m15:58:33  5 of 7 START freshness of beads.events ......................................... [RUN]
[0m15:58:33  6 of 7 START freshness of beads.issues ......................................... [RUN]
[0m15:58:33  6 of 7 PASS freshness of beads.issues .......................................... [[32mPASS[0m in 0.03s]
[0m15:58:33  7 of 7 START freshness of beads.labels ......................................... [RUN]
[0m15:58:33  7 of 7 PASS freshness of beads.labels .......................................... [[32mPASS[0m in 0.02s]
[0m15:58:33  4 of 7 PASS freshness of beads.dolt_log ........................................ [[32mPASS[0m in 0.12s]
[0m15:58:33  3 of 7 PASS freshness of beads.dolt_history_issues ............................. [[32mPASS[0m in 0.13s]
[0m15:58:33  5 of 7 PASS freshness of beads.events .......................................... [[32mPASS[0m in 0.11s]
```

**Command 5:** `cd /Users/dustincheng/projects/data-platform && mkdir -p warehouse/marts && dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go 2>&1 | tail -15`
- Exit code: 0
- Output (tail):
```
[0m15:58:40  53 of 56 START test not_null_mart_agent_activity_hourly_dt ..................... [RUN]
[0m15:58:40  53 of 56 PASS not_null_mart_agent_activity_hourly_dt ........................... [[32mPASS[0m in 0.01s]
[0m15:58:40  52 of 56 OK created sql external model main.fct_issue_events ................... [[32mOK[0m in 0.05s]
[0m15:58:40  54 of 56 START test not_null_fct_issue_events_event_id ......................... [RUN]
[0m15:58:40  54 of 56 PASS not_null_fct_issue_events_event_id ............................... [[32mPASS[0m in 0.01s]
[0m15:58:40  55 of 56 START sql external model main.mart_daily_throughput ................... [RUN]
[0m15:58:40  55 of 56 OK created sql external model main.mart_daily_throughput .............. [[32mOK[0m in 0.02s]
[0m15:58:40  56 of 56 START test not_null_mart_daily_throughput_dt .......................... [RUN]
[0m15:58:40  56 of 56 PASS not_null_mart_daily_throughput_dt ................................ [[32mPASS[0m in 0.01s]
[0m15:58:40  
[0m15:58:40  Finished running 5 external models, 44 data tests, 7 view models in 0 hours 0 minutes and 0.74 seconds (0.74s).
[0m15:58:40  
[0m15:58:40  [32mCompleted successfully[0m
[0m15:58:40  
[0m15:58:40  Done. PASS=56 WARN=0 ERROR=0 SKIP=0 NO-OP=0 REUSED=0 TOTAL=56
```

**Command 6:** `cd /Users/dustincheng/projects/data-platform && duckdb -readonly warehouse/dev.duckdb -c "select table_type, count(*) from information_schema.tables group by 1"`
- Exit code: 0
- Output:
```
┌────────────┬──────────────┐
│ table_type │ count_star() │
│  varchar   │    int64     │
├────────────┼──────────────┤
│ VIEW       │           12 │
└────────────┴──────────────┘
```

**Command 7:** `cd /Users/dustincheng/projects/data-platform && find warehouse/marts -maxdepth 1 -mindepth 1 -type d | sort && duckdb -c "select count(*) issues, sum(case when cycle_time_hours is not null then 1 else 0 end) closed from read_parquet('warehouse/marts/dim_issues/**/*.parquet')"`
- Exit code: 0
- Output:
```
warehouse/marts/dim_issues
warehouse/marts/dim_issues_scd2
warehouse/marts/fct_issue_events
warehouse/marts/mart_agent_activity_hourly
warehouse/marts/mart_daily_throughput
┌────────┬────────┐
│ issues │ closed │
│ int64  │ int128 │
├────────┼────────┤
│    132 │     88 │
└────────┴────────┘
```

**Command 8:** `cd /Users/dustincheng/projects/data-platform && mkdir -p local/tmp && for m in dim_issues dim_issues_scd2 fct_issue_events mart_daily_throughput mart_agent_activity_hourly; do echo "$m $(duckdb -noheader -list -c "select count(*) from read_parquet('warehouse/marts/$m/**/*.parquet')")"; done > local/tmp/mart-counts-before.txt && rm -rf warehouse && mkdir -p warehouse/marts && dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go > local/tmp/rebuild.log 2>&1; echo "rebuild-exit=$?"; for m in dim_issues dim_issues_scd2 fct_issue_events mart_daily_throughput mart_agent_activity_hourly; do echo "$m $(duckdb -noheader -list -c "select count(*) from read_parquet('warehouse/marts/$m/**/*.parquet')")"; done > local/tmp/mart-counts-after.txt; cat local/tmp/mart-counts-after.txt; diff local/tmp/mart-counts-before.txt local/tmp/mart-counts-after.txt && echo REBUILD-SAME`
- Exit code: 0
- Output:
```
rebuild-exit=0
dim_issues 132
dim_issues_scd2 179
fct_issue_events 658
mart_daily_throughput 18
mart_agent_activity_hourly 174
REBUILD-SAME
```

**Command 9:** `cd /Users/dustincheng/projects/data-platform && dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:pre_go --output name | sort`
- Exit code: 0
- Output:
```
dim_issues
dim_issues_scd2
fct_issue_events
mart_agent_activity_hourly
mart_daily_throughput
stg_beads__comment_snapshots
stg_beads__commits
stg_beads__dependency_snapshots
stg_beads__events
stg_beads__issue_history
stg_beads__issue_snapshots
stg_beads__label_snapshots
```

**Command 10:** `cd /Users/dustincheng/projects/data-platform && git log --oneline -3 && git status --short`
- Exit code: 0
- Output:
```
49be8c8 feat(dbt): dbt-duckdb project - external sources, staging views, pre_go Parquet marts
d812bdb docs(stream): 02-writer round 3 and test-adversary evidence; close
2105561 docs(stream): wave 3 evidence (04-runner accepted)
 M docs/fable-streams/2026-09-18-phase-1-build/tasks/05-dbt-project/report.md
```

### Summary

All 10 verification commands passed (exit code 0).

**Expected results met:**
- ✓ `issues` has 29 day directories (>= 20 required)
- ✓ `path-leaks: 0` and `staging-violations: 0`
- ✓ `ingest-exit=0` and freshness-exit=0, all 7 sources PASS (no ERROR)
- ✓ dbt build ends with `PASS=56 WARN=0 ERROR=0`
- ✓ Only `VIEW` rows in `information_schema` (12 total, 0 BASE TABLEs)
- ✓ Five mart directories present
- ✓ `REBUILD-SAME` — row counts identical before/after rebuild
- ✓ `dbt ls` lists exactly 7 staging models and 5 marts (12 total)


## adversarial-reviewer — round 1

### Method

Read the brief, the implementer's and verifier's round-1 sections, then the actual diff (commit
`49be8c8`) and live repo state. Independently re-ran the done-check and several verification
commands rather than trusting the recorded tails at face value, and specifically tried to break
the implementer's most load-bearing design claim (the relationships-test deviation).

### Wrong-input / evidence attacks tried

1. **Re-ran the done-check independently.** `mkdir -p warehouse/marts && dbt build --select
   tag:pre_go` → `Done. PASS=56 WARN=0 ERROR=0 SKIP=0 NO-OP=0 REUSED=0 TOTAL=56`; `select count(*)
   from information_schema.tables where table_type = 'BASE TABLE'` → `0`. Matches the verifier's
   record exactly, no discrepancy.
2. **Reproduced the `relationships`-generic-test compilation error the implementer cites as the
   reason for using a singular test instead.** Patched a temporary `relationships` block with
   `config.where` calling `ref()` into `_staging.yml`, ran `dbt parse`, got the exact error quoted
   in the report and in the test file's comment:
   ```
   [ERROR]: Encountered an error:
   Compilation Error
     Invalid generic test configuration given in models/staging/_staging.yml:
     The stg_beads__events.issue_id column's "relationships" test references an undefined macro in its where configuration argument. The macro 'ref' is undefined.
     Please note that the generic test configuration parser currently does not support using custom macros to populate configuration values
   ```
   Reverted with `git checkout -- dbt/models/staging/_staging.yml` (working tree now matches the
   committed version, verified with `git status --short dbt/` showing nothing). The deviation is a
   genuine dbt-core 1.12.5 limitation, not a shortcut — the singular test
   (`dbt/tests/assert_events_issue_id_exists.sql`) preserves the same DAG edge, error severity, and
   race-avoidance cutoff logic the brief specifies.
3. **Independently verified the `dependency_snapshots` key evidence** rather than trusting the
   quoted numbers: `select count(*), count(distinct id), count(distinct id||'|'||dt), count(*)
   filter (where id is null) from read_parquet('bronze/raw/beads/dependencies/*/*.parquet', ...)` →
   `1874, 99, 1874, 0`. Matches the report's claim exactly.
4. **Checked AC-27 (two-pass tags) against the manifest directly**, not just `dbt ls` output:
   every one of the 12 models carries exactly `['pre_go']` in `dbt/target/manifest.json`, none
   carry `post_go` or both.
5. **Checked test severities in the manifest**: every test is `ERROR` except
   `accepted_values_stg_beads__events_event_type...` which is `warn` — exactly matching AC-23's
   split (status/issue_type stay error, event_type is warn).
6. **Confirmed hive-partitioned `dt=` directories** for the three date-grained marts
   (`fct_issue_events`, `mart_daily_throughput`, `mart_agent_activity_hourly`) and per-thread
   `data_0.parquet` output for `dim_issues` / `dim_issues_scd2`, matching AC-24's partition-by-`dt`
   vs. `per_thread_output` split.
7. **Scope audit**: `git show --stat 49be8c8` touches only files inside the brief's declared Create
   list, plus `.gitignore` (adds only `dbt/.user.yml`, as scoped) — no Go files, no
   `target/`/`logs/`/`*.duckdb` committed. Current `git status --short` shows only this task's own
   `report.md` modified, nothing stray staged or untracked inside scope.
8. **Re-checked the staging dialect-discipline grep and path-leak grep myself**: `path-leaks: 0`,
   `staging-violations: 0`, both reproduced independently.

None of these attacks turned up a discrepancy between the implementer's/verifier's claims and the
live repository state.

### AC-by-AC walk (AC-20 to AC-27)

- **AC-20** — `dbt parse` exits 0 from repo root with the documented invocation; grep confirms
  `read_parquet`/`bronze/` appear only in `dbt/models/sources.yml`. Met.
- **AC-21** — all 7 tables present with `loaded_at_field: _extracted_at` and warn/error thresholds;
  `dbt source freshness` after `--once` exits 0, all PASS. Met.
- **AC-22** — 7 staging views, explicit column lists, casts/renames only, dedup via `row_number()`
  not `QUALIFY`, correct Sydney-vs-UTC conversions (`timezone('Australia/Sydney', ...)` for events,
  `timezone('UTC', ...)` elsewhere, deliberately avoiding a bare `::timestamptz` cast that would
  pick up the session's local `TimeZone`). Met.
- **AC-23** — unique/not_null on every staging key (composite keys as `||`-expressions),
  accepted_values on status/issue_type (error) and event_type (warn), relationships check present
  (as a singular test, justified above) at error severity with a race-avoidance cutoff, singular
  not-closed-before-created test. `dbt build --select tag:pre_go` → `PASS=56 ERROR=0`. Met.
- **AC-24** — 5 pre_go marts under `warehouse/marts/<model>/`, zero `BASE TABLE` in
  `information_schema`, `dim_issues` has the required column superset with unique+not_null on
  `issue_id`. Met.
- **AC-25** — SCD2 gaps-and-islands over the five tracked attributes, three singular tests for the
  three invariants. Met.
- **AC-26** — `rm -rf warehouse && mkdir -p warehouse/marts && dbt build` reproduces identical row
  counts (`REBUILD-SAME` in both implementer's and verifier's runs). Met.
- **AC-27** — every model carries exactly one tag, verified directly in the manifest, not just via
  `dbt ls`. Met.

### Verdict

No findings. The implementer's most consequential deviation from the brief's literal wording (a
singular test in place of the generic `relationships` test) is disclosed, justified, and — on
independent reproduction — factually correct about the dbt-core 1.12.5 behaviour that forced it.
All other evidence in the report checks out against the live repo and against my own re-runs.

```json
{
  "verdict": "pass",
  "findings": [],
  "escalation": null
}
```
