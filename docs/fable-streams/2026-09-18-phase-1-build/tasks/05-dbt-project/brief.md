---
task: 05-dbt-project
parallel_safe: false
testable: false
tier: standard
story: trk-bam.5
acs: [AC-20, AC-21, AC-22, AC-23, AC-24, AC-25, AC-26, AC-27]
---

# 05-dbt-project

## Goal

Build the dbt-duckdb project in `dbt/`. It layers bronze Parquet into a tested warehouse:
- **sources** are external tables over bronze paths, with no load step and freshness on `_extracted_at`;
- **staging** views do casts, renames and timezone handling, with explicit columns and no joins;
- **pre_go marts** are materialised as Parquet through dbt-duckdb's `external` materialisation, so
  `warehouse/dev.duckdb` holds only views.

It must pass `unique` / `not_null` / `accepted_values` / `relationships` tests and a singular test, and rebuild
entirely from bronze. It also sets up the `post_go` folder that task 07's mart will live in.

## Done-check

```
cd /Users/dustincheng/projects/data-platform && mkdir -p warehouse/marts && dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go && [ "$(duckdb -readonly -noheader -list warehouse/dev.duckdb -c "select count(*) from information_schema.tables where table_type = 'BASE TABLE'")" = 0 ] && echo DONE
```

## File scope

**Working-directory contract.** The target checkout is `/Users/dustincheng/projects/data-platform` (no
worktree), and the expected branch is `phase-1`. Before any write, run:

    cd /Users/dustincheng/projects/data-platform && [ "$(git rev-parse --show-toplevel)" = "/Users/dustincheng/projects/data-platform" ] && [ "$(git rev-parse --abbrev-ref HEAD)" = "phase-1" ] && echo CWD-OK

If it does not print `CWD-OK`, write nothing and escalate `broken_harness`.

Create:
- `dbt/dbt_project.yml`, `dbt/profiles.yml`
- `dbt/models/sources.yml`
- `dbt/models/staging/*.sql`, `dbt/models/staging/_staging.yml`
- `dbt/models/marts/pre_go/*.sql`, `dbt/models/marts/pre_go/_pre_go.yml`
- `dbt/tests/*.sql` (singular tests)
- `dbt/macros/*.sql`, only if needed

Modify:
- `.gitignore`: only to add dbt artefacts that dbt creates in `dbt/` and that are not yet ignored, e.g.
  `dbt/.user.yml`.

No Go files. Local, uncommitted artefacts: `bronze/`, `warehouse/`, `dbt/target/`, `dbt/logs/`, all gitignored.

**Git.** Commit when verification passes, and at the end of every fix round.
- Stage only paths inside this File scope, with `git add -- <paths>`. Then commit with
  `git commit -m "<message>" -- <paths>`, so the commit contains only your paths even if a sibling task has
  staged files in the shared index. Never use `git add -A`, `git add .` or `git commit -a`.
- Never commit this task's `report.md`, anything under `bronze/`, `warehouse/`, `local/` or `.beads/`, any
  `*.duckdb` file, `dbt/target/` or `dbt/logs/`.
- Commit message: `<type>(<scope>): <summary>`, a blank line, `Stream-task: 2026-09-18-phase-1-build/05-dbt-project round <N>`,
  a blank line, then `Co-Authored-By: Claude <your model name, e.g. Sonnet 5> <noreply@anthropic.com>`.
- If git reports an `index.lock`, wait 5 s and retry, at most 6 times. Do not push.

## Required content

**Prerequisite data.** Populate bronze with the full beads history once, before you build. This is local and
gitignored:

    go run ./cmd/ingest --source beads --from 2026-08-21 --to $(date -u +%F)

This command is **(long)**: run it with the Bash tool timeout at 600000 ms, and record its wall time in your
report. The snapshot tables have history from 2026-08-21 (spec F12).

The Runner and Fetcher exist from tasks 03 and 04. Do not modify Go code; if ingestion fails, escalate
`plan_invalidating_discovery` with the output tail.

**Invocation contract.** Every dbt command runs **from the repo root** as
`dbt <cmd> --project-dir dbt --profiles-dir dbt`, and all paths inside the project are repo-root-relative.
`warehouse/marts/` must exist before a build: DuckDB does not create parent directories (spec F5), so run
`mkdir -p warehouse/marts` first. Tasks 08, 09 and 10 do the same in Go and in the Makefile.

**`dbt_project.yml`.**
- Project `data_platform`, profile `data_platform`.
- `models/staging` → `+materialized: view`, `+tags: ["pre_go"]`.
- `models/marts/pre_go` → `+materialized: external`, `+tags: ["pre_go"]`.
- `models/marts/post_go` → `+materialized: external`, `+tags: ["post_go"]`. The folder may be empty until task
  07; if dbt warns about an unused config path, that is acceptable.
- Do **not** put tags on the parent `models/marts`: dbt tags accumulate, and every model must carry exactly one
  of the two tags (AC-27).

**`profiles.yml`.** Target `dev`, `type: duckdb`, `path: warehouse/dev.duckdb`, threads 4. No secrets.

**`models/sources.yml`.**
- Source `beads` with `meta.external_location` set to
  `"read_parquet('bronze/raw/beads/{name}/*/*.parquet', hive_partitioning = true, union_by_name = true)"`.
- Tables: `events`, `dolt_history_issues`, `dolt_log`, `issues`, `comments`, `dependencies`, `labels`.
- `loaded_at_field: _extracted_at` on every table.
- Freshness: snapshot tables warn after 6 hours and error after 48 hours. Event-log tables warn after 48 hours
  and error after 30 days; event freshness also reflects quiet periods in the source, since only windows
  holding rows carry an `_extracted_at`.
- This is the only file that contains paths or `read_parquet` (AC-20).

**Staging.** One view per source table, named exactly:
- `stg_beads__events`
- `stg_beads__issue_snapshots` (from `issues`)
- `stg_beads__issue_history` (from `dolt_history_issues`)
- `stg_beads__commits` (from `dolt_log`)
- `stg_beads__comment_snapshots`
- `stg_beads__dependency_snapshots`
- `stg_beads__label_snapshots`

Rules for every staging model:
- An explicit column list: select the columns the marts need and rename freely (e.g. `id` → `issue_id`,
  `text` → `comment_text`, `date` → `committed_at`).
- Casts only: no joins and no business logic.
- No DuckDB-only types or `QUALIFY` (dialect discipline, spec §4). For de-duplication use a `row_number()`
  subquery.
- Keep `_extracted_at` where useful.
- Snapshot models expose `snapshot_date`, the `dt` partition (the UTC day of the window).
- Timestamps become `timestamptz`:
  - `events.created_at` is Sydney wall time (spec F1), so convert it as Australia/Sydney, e.g.
    `timezone('Australia/Sydney', created_at)`;
  - every other source timestamp is UTC wall time, so convert it as UTC.
- `stg_beads__events` also exposes `created_date_local` (the Australia/Sydney date), and is de-duplicated on
  `id` (keep the row with the greatest `_extracted_at`). The DST fall-back hour maps two UTC windows onto one
  local range.
- The history and snapshot models keep the attributes the marts need: status, priority, issue_type, assignee,
  title, created_at, updated_at, closed_at, close_reason, description, design, acceptance_criteria, notes.

**Data tests** (`_staging.yml`, `_pre_go.yml`, `dbt/tests/`):
- `unique` + `not_null` on each staging key. Composite keys are tested as expressions (e.g.
  `issue_id || '|' || snapshot_date`):
  - events: `id`;
  - issue_snapshots: `(issue_id, snapshot_date)`;
  - issue_history: `(issue_id, commit_hash)`;
  - commits: `commit_hash`;
  - comment_snapshots: `(comment_id, snapshot_date)`;
  - dependency_snapshots: a key you verify is unique. Older snapshots may predate a column (schema evolution),
    so check the data and document your choice;
  - label_snapshots: `(issue_id, label, snapshot_date)`.
- `accepted_values`:
  - `status` ∈ {open, in_progress, blocked, deferred, closed};
  - `issue_type` ∈ {bug, feature, task, epic, chore, decision, event};
  - `event_type` ∈ {created, updated, status_changed, commented, closed, reopened, dependency_added,
    dependency_removed, label_added, label_removed, compacted, claimed}, with `config: {severity: warn}`. Beads
    adds event kinds more often than statuses, and a new kind must not halt BuildMarts. status and issue_type
    stay at error severity.
- `relationships`: `stg_beads__events.issue_id` → `stg_beads__issue_snapshots.issue_id`, at error severity, and
  not flaky under the extraction race (an event whose issue was created after that day's snapshot was fetched).
  E.g. restrict it with `config: where:` to events created before the latest snapshot's `_extracted_at`, less a
  margin; explain your choice in the yml description.
- A singular test `dbt/tests/assert_issue_not_closed_before_created.sql` (zero rows = pass).
- A singular test `dbt/tests/assert_event_created_matches_issue_created.sql`. Each `created` event's UTC
  `created_at` from `stg_beads__events` must lie within 60 seconds of its issue's UTC `created_at` in the latest
  issue snapshot. This proves the F1 Sydney→UTC conversion (AC-22); zero rows = pass.
- SCD2 tests (AC-25): one `is_current` row per issue; `valid_to` null iff `is_current`; a singular test for no
  overlapping intervals per issue.

**pre_go marts** (`dbt/models/marts/pre_go/`). Each is `external` with `location` =
`warehouse/marts/<model_name>`. Date-grained marts use `options: {partition_by: 'dt', overwrite: true}`, with
`dt` = a local (Australia/Sydney) date. The others use `options: {per_thread_output: true, overwrite: true}`,
which writes `warehouse/marts/<model>/data_N.parquet` and replaces stale files; the conductor verified this in
DuckDB 1.5.5.
- `dim_issues`: one row per issue from the latest `snapshot_date`. Columns include at least `issue_id, title,
  status, issue_type, priority, assignee, created_at, closed_at, cycle_time_hours` (hours from created to
  closed, null if open) `, description, design, acceptance_criteria, notes, close_reason`, with `unique` +
  `not_null` on `issue_id`. **Task 07's Go transform reads these text columns by name.**
- `dim_issues_scd2`: SCD Type 2 over daily snapshots. One row per issue per contiguous run of unchanged
  (status, priority, issue_type, assignee, title), with `valid_from` (first snapshot_date of the run),
  `valid_to` (the next run's `valid_from`, exclusive, or null) and `is_current`.
- `fct_issue_events`: de-duplicated events with UTC `created_at`, `created_date_local`, `event_type`, `actor`,
  `issue_id`, and the issue's `issue_type` from `dim_issues`. Partitioned by `dt` = `created_date_local`.
- `mart_daily_throughput`: per local date, `issues_created`, `issues_closed` and `events`. Partitioned by `dt`.
- `mart_agent_activity_hourly`: per UTC hour and actor, `events` (from events.actor) and `commits` (from
  dolt_log.committer). Partitioned by `dt` = the local date of the hour.

## Inputs

- `/Users/dustincheng/projects/data-platform/docs/data-platform-spec.md` (§4 Warehouse; §5 dbt Core; §7 Tests;
  the decision log)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/spec.md` (F1, F4, F5,
  F7, AC-20 to AC-27)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/plan.md`
- `/Users/dustincheng/projects/data-platform/.gitignore`
- A sample of real bronze files: inspect one per table with `duckdb -c "describe select * from read_parquet('bronze/raw/beads/<t>/*/*.parquet', union_by_name=true)"`

## Verification commands

```
cd /Users/dustincheng/projects/data-platform && ls bronze/raw/beads/issues | wc -l | tr -d ' '
cd /Users/dustincheng/projects/data-platform && dbt parse --project-dir dbt --profiles-dir dbt 2>&1 | tail -5
cd /Users/dustincheng/projects/data-platform && echo "path-leaks: $(grep -rlE 'read_parquet|bronze/' dbt/models --include='*.sql' | wc -l | tr -d ' ')"; echo "staging-violations: $(grep -rniE 'select \*|qualify|join' dbt/models/staging | wc -l | tr -d ' ')"
cd /Users/dustincheng/projects/data-platform && mkdir -p local/tmp && go run ./cmd/ingest --source beads --once >/dev/null; echo "ingest-exit=$?"; dbt source freshness --project-dir dbt --profiles-dir dbt > local/tmp/freshness.log 2>&1; echo "freshness-exit=$?"; tail -12 local/tmp/freshness.log
cd /Users/dustincheng/projects/data-platform && mkdir -p warehouse/marts && dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go 2>&1 | tail -15
cd /Users/dustincheng/projects/data-platform && duckdb -readonly warehouse/dev.duckdb -c "select table_type, count(*) from information_schema.tables group by 1"
cd /Users/dustincheng/projects/data-platform && find warehouse/marts -maxdepth 1 -mindepth 1 -type d | sort && duckdb -c "select count(*) issues, sum(case when cycle_time_hours is not null then 1 else 0 end) closed from read_parquet('warehouse/marts/dim_issues/**/*.parquet')"
cd /Users/dustincheng/projects/data-platform && mkdir -p local/tmp && for m in dim_issues dim_issues_scd2 fct_issue_events mart_daily_throughput mart_agent_activity_hourly; do echo "$m $(duckdb -noheader -list -c "select count(*) from read_parquet('warehouse/marts/$m/**/*.parquet')")"; done > local/tmp/mart-counts-before.txt && rm -rf warehouse && mkdir -p warehouse/marts && dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go > local/tmp/rebuild.log 2>&1; echo "rebuild-exit=$?"; for m in dim_issues dim_issues_scd2 fct_issue_events mart_daily_throughput mart_agent_activity_hourly; do echo "$m $(duckdb -noheader -list -c "select count(*) from read_parquet('warehouse/marts/$m/**/*.parquet')")"; done > local/tmp/mart-counts-after.txt; cat local/tmp/mart-counts-after.txt; diff local/tmp/mart-counts-before.txt local/tmp/mart-counts-after.txt && echo REBUILD-SAME
cd /Users/dustincheng/projects/data-platform && dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:pre_go --output name | sort
cd /Users/dustincheng/projects/data-platform && git log --oneline -3 && git status --short
```

Commands 4, 5 and 8 are **(long)**: run them with the Bash tool timeout at 600000 ms.

Expected results:
- `issues` has at least 20 day directories.
- `path-leaks: 0` and `staging-violations: 0`.
- `ingest-exit=0` and freshness exits 0, with no source at ERROR.
- The build ends `PASS=… ERROR=0`.
- Only `VIEW` rows in `information_schema`.
- Five mart directories.
- `REBUILD-SAME`.
- `dbt ls` lists exactly the 7 staging models and 5 marts.

## Report obligation

Append `## implementer — round <N>` to `tasks/05-dbt-project/report.md`. Include:
- the files created;
- the model list with row counts;
- the dependency_snapshots key choice, with its evidence;
- how the relationships test avoids the extraction race;
- each verification command with its exit code and output tail (≤30 lines);
- the commit SHA.

## Tracker

Story **trk-bam.5**. Run `bd` from `/Users/dustincheng/projects/data-platform`.
- Before your first edit: `bd update trk-bam.5 --claim --actor claudeclaw`
- Once per round, after committing and before your final response:
  `bd comment trk-bam.5 --actor claudeclaw "<round N: what landed + commit SHA, or what stopped you>"`
- Never close the story, and never add or remove labels (never `agent`, never `human`).

## Out of scope

- The `derived` source and any `post_go` model (task 07).
- Any Go code.
- dbt packages from the hub (none: stay dependency-free).
- Incremental models.
- Metabase or any BI tool.
- Deleting `bronze/`: it is only overwritten, by `ingest`.
