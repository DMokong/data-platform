---
task: 07-go-transform
parallel_safe: true
testable: true
tier: standard
story: trk-bam.7
acs: [AC-28, AC-29, AC-30, AC-31, AC-32]
---

# 07-go-transform

## Goal

Build the first **Go transform** and close the dbt ↔ Go loop (spec §6, decision "Go transforms write
`bronze/derived/`, declared as dbt sources; two-pass dbt build").

`issue_mentions` reads the `dim_issues` **mart Parquet** with parquet-go. It uses no DuckDB and never reads
staging. It extracts issue-id mentions from free text, which SQL handles badly, and writes
`bronze/derived/issue_mentions/` through the shared Writer.

dbt then declares that output as source `derived.issue_mentions`, with freshness. A `post_go` mart,
`mart_issue_mention_drift`, flags mentions with no declared dependency edge. The two-pass build
(`tag:pre_go` → transform → `tag:post_go`) runs end to end.

## Done-check

```
cd /Users/dustincheng/projects/data-platform && go test -count=1 ./internal/transform/... ./cmd/transform/... && rm -rf warehouse && mkdir -p warehouse/marts && dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go >/dev/null && go run ./cmd/transform issue_mentions --window 2026-09-18 && dbt build --project-dir dbt --profiles-dir dbt --select tag:post_go && ls warehouse/marts/mart_issue_mention_drift/*.parquet && echo DONE
```

## File scope

**Working-directory contract.** The target checkout is `/Users/dustincheng/projects/data-platform` (no
worktree), and the expected branch is `phase-1`. Before any write, run:

    cd /Users/dustincheng/projects/data-platform && [ "$(git rev-parse --show-toplevel)" = "/Users/dustincheng/projects/data-platform" ] && [ "$(git rev-parse --abbrev-ref HEAD)" = "phase-1" ] && echo CWD-OK

If it does not print `CWD-OK`, write nothing and escalate `broken_harness`.

Create:
- `internal/transform/doc.go`, `internal/transform/transform.go`, `internal/transform/transform_test.go`
- `internal/transform/catalog/doc.go`, `internal/transform/catalog/catalog.go`,
  `internal/transform/catalog/catalog_test.go`
- `internal/transform/mentions/doc.go`, `mentions.go`, `extract.go`, `mentions_test.go`, `extract_test.go`
- `cmd/transform/doc.go`, `cmd/transform/main.go`
- `dbt/models/marts/post_go/mart_issue_mention_drift.sql`, `dbt/models/marts/post_go/_post_go.yml`

Modify:
- `dbt/models/sources.yml`: **add** the `derived` source only; do not change the `beads` source.

**Not in scope: `go.mod` / `go.sum`.** Task 08 runs in parallel and owns them. parquet-go is already required
(task 02). Use only the standard library and existing requirements, including in tests. If you truly need a new
module, stop and escalate `scope_breach`.

**Parallel-wave hygiene.** Task 08 is editing `internal/telemetry`, `internal/dbt` and `go.mod` at the same time.
Build and test only your own packages (`./internal/transform/...`, `./cmd/transform/...`); never
`go build ./...` or `go test ./...`.

**Git.** Commit when verification passes, and at the end of every fix round.
- Stage only paths inside this File scope, with `git add -- <paths>`. Then commit with
  `git commit -m "<message>" -- <paths>`, so the commit contains only your paths even if a sibling task has
  staged files in the shared index. Never use `git add -A`, `git add .` or `git commit -a`.
- Never commit this task's `report.md`, anything under `bronze/`, `warehouse/`, `local/` or `.beads/`, or any
  `*.duckdb` file.
- Commit message: `<type>(<scope>): <summary>`, a blank line, `Stream-task: 2026-09-18-phase-1-build/07-go-transform round <N>`,
  a blank line, then `Co-Authored-By: Claude <your model name, e.g. Sonnet 5> <noreply@anthropic.com>`.
- If git reports an `index.lock`, a sibling task is committing: wait 5 s and retry, at most 6 times. Do not
  push.

## Required content

**`internal/transform`** (exact exported API):

```go
type Contract struct {
    Name   string       // "issue_mentions"
    Inputs []string     // table names read, e.g. ["dim_issues"]
    Output string       // derived dataset/table name, e.g. "issue_mentions"
    Grain  window.Grain // window.Day
    Tests  []string     // dbt tests guarding the output, e.g. ["source_freshness:derived.issue_mentions", "mart_issue_mention_drift"]
}
type Env struct { WarehouseRoot string } // "<repo>/warehouse"
type Transform interface {
    Contract() Contract
    Run(ctx context.Context, env Env, w window.Window) (record.Batch, error)
}
// Materialise runs t for window w and writes its output to the derived zone through the shared Writer:
// Writer.Write(ctx, bronze.Derived, t.Contract().Output, w, batch, extractedAt).
func Materialise(ctx context.Context, t Transform, env Env, wr *bronze.Writer, w window.Window, extractedAt time.Time) (bronze.Result, error)
```

**`internal/transform/catalog`**: `func All() []transform.Transform` (sorted by name) and
`func Lookup(name string) (transform.Transform, bool)`. This is the registry the orchestrator lists (task 09). It
is kept explicit: no `init()` side-effect registration.

**`internal/transform/mentions`**: the `issue_mentions` transform.
- *Input.* Read every `*.parquet` under `<WarehouseRoot>/marts/dim_issues/` with parquet-go, using columns
  `issue_id, title, description, design, acceptance_criteria, notes, close_reason`. DuckDB writes them as
  optional UTF-8 byte arrays, so nulls are possible.
- *Prefixes.* The set of issue-id prefixes is everything before the first `-` across `dim_issues.issue_id` (today
  `trk`).
- *Pattern.* A mention matches `\b(<prefix alternatives>)-[0-9a-z]+(\.[0-9]+)*\b`, case-sensitive. It includes
  child ids like `trk-bam.1`; a trailing sentence period is not part of the id.
- *Output batch.* Columns `issue_id` (String), `mentioned_issue_id` (String), `field` (String, one of the six
  field names) and `mention_count` (Int64). There is one row per (issue_id, mentioned_issue_id, field).
  Self-mentions are dropped. Rows are sorted by `issue_id, mentioned_issue_id, field`, so output is
  deterministic.
- *Unknown prefixes* (e.g. `claw-` when no `claw-*` issue exists) are ignored.

**`cmd/transform`.**
- Invocation: `transform <name> --window YYYY-MM-DD [--root .]`. The positional name comes first, so parse it,
  then parse the flags from the remaining args.
- It builds a Day window for the date, runs `transform.Materialise` with `extractedAt = time.Now()`, prints
  `transform=<name> window=<date> rows=<n> path=<bronze-relative path>`, and exits 1 on error. An unknown name
  lists the catalog and exits 2.

**dbt.**
- `sources.yml` gains source `derived`:
  - `meta.external_location` set to
    `"read_parquet('bronze/derived/{name}/*/*.parquet', hive_partitioning = true, union_by_name = true)"`;
  - table `issue_mentions` with `loaded_at_field: _extracted_at`;
  - freshness warn after 24 hours, error after 72 hours.
- `mart_issue_mention_drift` lives in `dbt/models/marts/post_go/`, so the folder config tags it `post_go` and
  materialises it `external`. Give it `location` `warehouse/marts/mart_issue_mention_drift` and
  `options: {per_thread_output: true, overwrite: true}`.
  - It takes mentions from the **latest `dt`** of `source('derived','issue_mentions')` that have no dependency
    edge, in either direction, between the two issues in the latest `stg_beads__dependency_snapshots` snapshot.
  - Grain: (issue_id, mentioned_issue_id), with total `mention_count`, a count of distinct fields, and
    `mentioned_issue_exists` (whether the id is in `dim_issues`).
  - Tests: `unique` on `issue_id || '|' || mentioned_issue_id`, and `not_null` on both ids.
- No paths or `read_parquet` in any `.sql` file.

**Tests (Go).**
- Table-driven extraction tests:
  - punctuation and word boundaries (`(trk-abc)`, `trk-abc,`, `trk-abc.` at sentence end);
  - child id `trk-bam.1`;
  - a hyphenated suffix (`trk-7gx-health` → `trk-7gx`);
  - duplicates within one field (count 2);
  - the same id in two fields (two rows);
  - a self-mention dropped;
  - an unknown prefix ignored;
  - null fields.
- A test over a small Parquet fixture that you generate in the test with parquet-go into `t.TempDir()`, shaped
  like dim_issues, which checks the batch schema and row order.
- A catalog test: `issue_mentions` is listed with its exact Contract (AC-28).

`doc.go` for each package includes a line starting `// Data-engineering concept: `:
- `transform`: the transform contract (inputs, one output, window, tests) shared by dbt and Go;
- `catalog`: an explicit transform registry for orchestration;
- `mentions`: free-text parsing where SQL is the wrong tool;
- `cmd/transform`: running one Go transform between the two dbt passes.

## Inputs

- `/Users/dustincheng/projects/data-platform/docs/data-platform-spec.md` (§5 tags; §6 Go transforms; §7)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/spec.md` (AC-28 to
  AC-32, AC-42)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/plan.md`
- `/Users/dustincheng/projects/data-platform/internal/bronze/writer.go`,
  `/Users/dustincheng/projects/data-platform/internal/window/window.go`,
  `/Users/dustincheng/projects/data-platform/internal/record/record.go`
- `/Users/dustincheng/projects/data-platform/dbt/dbt_project.yml`,
  `/Users/dustincheng/projects/data-platform/dbt/models/sources.yml`,
  `/Users/dustincheng/projects/data-platform/dbt/models/marts/pre_go/_pre_go.yml`,
  `/Users/dustincheng/projects/data-platform/dbt/models/staging/_staging.yml`
- The real mart schema: `duckdb -c "describe select * from read_parquet('warehouse/marts/dim_issues/**/*.parquet')"`

## Verification commands

```
cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/transform cmd/transform)" && echo GOFMT-OK
cd /Users/dustincheng/projects/data-platform && go vet ./internal/transform/... ./cmd/transform/...
cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/transform/... ./cmd/transform/... 2>&1 | tail -40
cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./cmd/transform/ && ! go list -deps ./cmd/transform/ | grep -qi duckdb && echo NO-CGO-NO-DUCKDB
cd /Users/dustincheng/projects/data-platform && echo "path-leaks: $(grep -rlE 'read_parquet|bronze/' dbt/models --include='*.sql' | wc -l | tr -d ' ')"
cd /Users/dustincheng/projects/data-platform && rm -rf warehouse && mkdir -p warehouse/marts && dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go > /dev/null 2>&1; echo "pass1-exit=$?"; go run ./cmd/transform issue_mentions --window 2026-09-18; echo "transform-exit=$?"; dbt build --project-dir dbt --profiles-dir dbt --select tag:post_go 2>&1 | tail -8
cd /Users/dustincheng/projects/data-platform && go run ./cmd/transform issue_mentions --window 2026-09-18 >/dev/null && ls -la bronze/derived/issue_mentions/dt=2026-09-18/ && duckdb -c "select count(*) as n_rows, count(distinct issue_id) as n_issues, max(_extracted_at) as extracted from read_parquet('bronze/derived/issue_mentions/dt=2026-09-18/part-0.parquet')"
cd /Users/dustincheng/projects/data-platform && duckdb -c "select * from read_parquet('warehouse/marts/mart_issue_mention_drift/*.parquet') order by mention_count desc limit 5"
cd /Users/dustincheng/projects/data-platform && dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:post_go --output name
cd /Users/dustincheng/projects/data-platform && mkdir -p local/tmp && dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --output name | sort > local/tmp/all-models.txt && ( dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:pre_go --output name; dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:post_go --output name ) | sort > local/tmp/tagged-models.txt && echo "models=$(wc -l < local/tmp/all-models.txt | tr -d ' ') tagged=$(wc -l < local/tmp/tagged-models.txt | tr -d ' ') dup=$(uniq -d local/tmp/tagged-models.txt | wc -l | tr -d ' ')" && diff local/tmp/all-models.txt local/tmp/tagged-models.txt && echo TAGS-EXACT
cd /Users/dustincheng/projects/data-platform && mkdir -p local/tmp && dbt source freshness --project-dir dbt --profiles-dir dbt --select source:derived > local/tmp/freshness-derived.log 2>&1; echo "freshness-exit=$?"; tail -5 local/tmp/freshness-derived.log
cd /Users/dustincheng/projects/data-platform && git log --oneline -3 && git status --short
```

Expected results:
- `pass1-exit=0` and `transform-exit=0`.
- The post_go build ends with `ERROR=0`.
- The derived directory holds only `part-0.parquet`.
- The post_go listing is exactly `mart_issue_mention_drift`.
- The tag check prints `models=13 tagged=13 dup=0` and `TAGS-EXACT` (AC-27).
- `freshness-exit=0`.

The two-pass command is **(long)**: run it with the Bash tool timeout at 600000 ms.

## Report obligation

Append `## implementer — round <N>` to `tasks/07-go-transform/report.md`. Include:
- the files changed;
- the mention and drift counts you observed;
- each verification command with its exit code and output tail (≤30 lines);
- the commit SHA.

## Tracker

Story **trk-bam.7**. Run `bd` from `/Users/dustincheng/projects/data-platform`.
- Before your first edit: `bd update trk-bam.7 --claim --actor claudeclaw`
- Once per round, after committing and before your final response:
  `bd comment trk-bam.7 --actor claudeclaw "<round N: what landed + commit SHA, or what stopped you>"`
- Never close the story, and never add or remove labels (never `agent`, never `human`).

## Out of scope

- Temporal activities for transforms (task 09).
- Telemetry (task 08).
- Changing any pre_go model, staging model or the `beads` source.
- `go.mod` / `go.sum`.
- Reading staging views or `warehouse/dev.duckdb` from Go.
