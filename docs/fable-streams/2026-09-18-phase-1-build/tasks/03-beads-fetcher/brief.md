---
task: 03-beads-fetcher
parallel_safe: false
testable: true
tier: judgment
story: trk-bam.3
acs: [AC-09, AC-10, AC-11, AC-12, AC-13, AC-14]
---

# 03-beads-fetcher

## Goal

Build the source contract `internal/source` and the beads Fetcher `internal/source/beads`. For any declared
table and window, the Fetcher returns a `record.Batch` exactly as the Dolt server served it, read-only and in a
deterministic order.
- Hourly event-log windows cover `events`, `dolt_history_issues` and `dolt_log`.
- Daily snapshots of `issues`, `comments`, `dependencies` and `labels` are re-fetchable for any past day through
  Dolt time travel (`AS OF`).

The Fetcher "talks to the source… knows nothing else" (spec §2): it never writes files, never picks windows, and
never cleans data.

## Done-check

```
cd /Users/dustincheng/projects/data-platform && go test -count=1 ./internal/source/... && BEADS_LIVE=1 go test -count=1 -run Live ./internal/source/beads/ && echo DONE
```

## File scope

**Working-directory contract.** The target checkout is `/Users/dustincheng/projects/data-platform` (no
worktree), and the expected branch is `phase-1`. Before any write, run:

    cd /Users/dustincheng/projects/data-platform && [ "$(git rev-parse --show-toplevel)" = "/Users/dustincheng/projects/data-platform" ] && [ "$(git rev-parse --abbrev-ref HEAD)" = "phase-1" ] && echo CWD-OK

If it does not print `CWD-OK`, write nothing and escalate `broken_harness`.

Create:
- `internal/source/doc.go`, `internal/source/source.go`, `internal/source/source_test.go`
- `internal/source/beads/doc.go`, `config.go`, `declaration.go`, `query.go`, `schema.go`, `fetcher.go`
- `internal/source/beads/*_test.go`, including a live test file whose tests are named `TestLive…` and skip unless
  `BEADS_LIVE=1`

Modify:
- `go.mod`, `go.sum`: add the driver with `GOTOOLCHAIN=local go get github.com/go-sql-driver/mysql@v1.10.1`
  (spec F11). This task owns
  these files while it runs; task 02 ran before you in this wave.

**Git.** Commit when verification passes, and at the end of every fix round.
- Stage only paths inside this File scope, with `git add -- <paths>`. Then commit with
  `git commit -m "<message>" -- <paths>`, so the commit contains only your paths even if a sibling task has
  staged files in the shared index. Never use `git add -A`, `git add .` or `git commit -a`.
- Never commit this task's `report.md`, anything under `bronze/`, `warehouse/`, `local/` or `.beads/`, or any
  `*.duckdb` file.
- Commit message: `<type>(<scope>): <summary>`, a blank line, `Stream-task: 2026-09-18-phase-1-build/03-beads-fetcher round <N>`,
  a blank line, then `Co-Authored-By: Claude <your model name, e.g. Opus 5> <noreply@anthropic.com>`.
- If git reports an `index.lock`, wait 5 s and retry, at most 6 times. Do not push.

## Required content

**`internal/source`** (exact exported API):

```go
type Shape int
const (
    EventLog Shape = iota + 1 // rows selected by a time column inside the window
    Snapshot                  // whole table as of the window end (time travel)
)
type Table struct {
    Name     string
    Grain    window.Grain
    Lookback int
    Shape    Shape
}
type Source struct {
    Name    string
    Cadence time.Duration
    Tables  []Table
}
func (s Source) Table(name string) (Table, bool)
type Fetcher interface {
    Fetch(ctx context.Context, table string, w window.Window) (record.Batch, error)
}
```

**`internal/source/beads`** (exact exported API):

```go
type Config struct { Host string; Port int; Database string; User string; Password string; EventsTZ string }
func ConfigFromEnv() (Config, error)   // env names and defaults per AC-09; invalid port or unknown tz → error
func (c Config) DSN() string            // tcp, no TLS, parseTime=true, loc=UTC, interpolateParams=true
func Declaration() source.Source        // AC-10: "beads", 15*time.Minute, 7 tables in this order: events, dolt_history_issues, dolt_log, issues, comments, dependencies, labels
type Query struct { SQL string; Args []any }
func BuildQuery(table string, w window.Window, eventsLoc *time.Location) (Query, error) // pure; unknown table → error
type Fetcher struct { /* *sql.DB, config, loc */ }
func NewFetcher(cfg Config) (*Fetcher, error)
func (f *Fetcher) Fetch(ctx context.Context, table string, w window.Window) (record.Batch, error)
func (f *Fetcher) Close() error
```

**Queries.** SQL keywords are uppercase. Table names come only from the declaration's fixed list, never from
input.
- `events`: `SELECT * FROM events WHERE created_at >= ? AND created_at < ? ORDER BY created_at, id`, with bounds
  equal to `w.Start` / `w.End` converted to `EventsTZ` wall time, formatted `2006-01-02 15:04:05`. The source
  stores Sydney local time (spec F1).
- `dolt_history_issues`: `… WHERE commit_date >= ? AND commit_date < ? ORDER BY commit_date, commit_hash, id`,
  UTC bounds.
- `dolt_log`: `… WHERE date >= ? AND date < ? ORDER BY date, commit_hash`, UTC bounds.
- Snapshots: `SELECT * FROM <table> AS OF TIMESTAMP(?) ORDER BY <key>`, where the arg is `w.End` in UTC,
  formatted as above. Keys: `issues`, `comments`, `dependencies` use `id`; `labels` uses `issue_id, label`.
  Spec F2: a bare string literal after `AS OF` is rejected, but `TIMESTAMP(...)` works. Verify your statement
  live.

**Fetch rules.**
- *Read-only.* Every fetch runs inside `db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})` and ends with a
  rollback or commit (AC-14).
- *Columns.* Columns come from `rows.ColumnTypes()` in result order, which is table order for `SELECT *`. Map
  `DatabaseTypeName()` to a kind:
  - CHAR / VARCHAR / TEXT variants / JSON / ENUM / SET / DECIMAL → `String`;
  - TINYINT / SMALLINT / MEDIUMINT / INT / BIGINT (signed or unsigned) → `Int64`;
  - FLOAT / DOUBLE → `Float64`;
  - DATETIME / TIMESTAMP → `Timestamp`;
  - anything else → an error naming the column and type.
  `tinyint(1)` stays `Int64` and JSON stays text: ingestion never transforms.
- *Observed driver behaviour* (conductor probe, 2026-09-18, go-sql-driver/mysql v1.10.1 against this server).
  `DatabaseTypeName()` reports unsigned integers with a prefix, e.g. `UNSIGNED BIGINT` for
  `dolt_log.commit_order`, and that value scans as `uint64`. `TEXT` scans as `[]uint8`; `DATETIME` scans as
  `time.Time`. Convert `uint64` to `int64` with an overflow check that errors rather than wraps.
- *Values.* Pitfall: with `interpolateParams=true` the driver uses the text protocol, so numeric columns may
  arrive as `[]byte`. Decode each value to its kind's Go type (`string`, `int64`, `float64`, `time.Time` in UTC
  carrying the source wall-clock fields); NULL becomes `nil`. This decodes the wire format and does not change
  any value. A value that will not decode is an error, not a guess.
- *Before history.* A snapshot window that ends before the table's first commit (Dolt reports
  `table not found`) returns an error naming table and window. Never return an empty batch for it.
- *Output.* The returned batch passes `record.Batch.Validate()`.

**Unit tests (no DB).**
- `ConfigFromEnv` with defaults and with overrides.
- `Declaration()`: the full expected table list, grains, lookbacks, shapes and cadence.
- `BuildQuery` for every table, including the AC-11 examples:
  - window 2026-09-15T03:00Z gives events bounds `2026-09-15 13:00:00` / `2026-09-15 14:00:00` and history
    bounds `2026-09-15 03:00:00` / `2026-09-15 04:00:00`;
  - window 2026-10-03T16:00Z gives events bounds `2026-10-04 03:00:00` / `2026-10-04 04:00:00`;
  - a snapshot for day 2026-09-10 gets the AS OF arg `2026-09-11 00:00:00`.
- The type mapping, including the unknown-type error.
- A test that `Fetch` requests `ReadOnly: true`: factor the tx options into a function or variable you can
  assert on.

**Live tests (`BEADS_LIVE=1`, against 127.0.0.1:49209 db trk).**
- A closed hourly window fetched twice gives equal batches. Use a past window with rows, e.g. `events` in
  2026-09-15T03 or `dolt_history_issues` in 2026-09-02T16.
- `issues` snapshot for day 2026-09-10 fetched twice gives equal batches, with row count > 0.
- The events row count summed over the 24 hourly windows of UTC day 2026-09-15 equals an independent
  `SELECT COUNT(*) FROM events WHERE created_at >= '2026-09-15 10:00:00' AND created_at < '2026-09-16 10:00:00'`.
  Those are the Sydney bounds for that UTC day, since no DST applies in September.
- An `issues` snapshot for day 2026-08-01 returns an error.
- `events.created_at` of a known row equals the raw source value; do not shift it to UTC.

`doc.go` for each package includes a line starting `// Data-engineering concept: `:
- `source`: declared extraction contracts (grain, cadence, lookback);
- `beads`: extraction from an event log plus time-travel snapshots, without transformation.

## Inputs

- `/Users/dustincheng/projects/data-platform/docs/data-platform-spec.md` (Requirement: idempotent re-runs; §1
  Sources; §2; §11 Configuration)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/spec.md` (F1, F2, F7,
  §3, AC-09 to AC-14)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/plan.md`
- `/Users/dustincheng/projects/data-platform/internal/window/window.go`
- `/Users/dustincheng/projects/data-platform/internal/record/record.go`
- `/Users/dustincheng/projects/data-platform/CLAUDE.md` (Source one: beads; treat it as read-only)

## Verification commands

```
cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/source)" && echo GOFMT-OK
cd /Users/dustincheng/projects/data-platform && go vet ./internal/source/...
cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/source/... 2>&1 | tail -30
cd /Users/dustincheng/projects/data-platform && BEADS_LIVE=1 go test -count=1 -v -run Live ./internal/source/beads/ 2>&1 | tail -30
cd /Users/dustincheng/projects/data-platform && grep -rnE '(INSERT|UPDATE|DELETE|REPLACE|CREATE|DROP|ALTER|CALL)[[:space:]]' --include='*.go' internal/source | grep -v '_test.go' ; echo "write-statement-lines: $(grep -rnE '(INSERT|UPDATE|DELETE|REPLACE|CREATE|DROP|ALTER|CALL)[[:space:]]' --include='*.go' internal/source | grep -vc '_test.go')"
cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./internal/source/... && echo BUILD-OK
cd /Users/dustincheng/projects/data-platform && grep -E '^(go|toolchain) ' go.mod
cd /Users/dustincheng/projects/data-platform && grep -n 'go-sql-driver/mysql' go.mod && git log --oneline -3 && git status --short
```

The go directive must be `go 1.25` or `go 1.25.x` (x ≤ 5), with no `toolchain` line.

The write-statement grep must report `write-statement-lines: 0`.

## Report obligation

Append `## implementer — round <N>` to `tasks/03-beads-fetcher/report.md`. Include:
- the files changed;
- the observed `DatabaseTypeName()` values per table (a short table) and how each maps;
- anything about the live data that surprised you;
- each verification command with its exit code and output tail (≤30 lines);
- the commit SHA.

## Tracker

Story **trk-bam.3**. Run `bd` from `/Users/dustincheng/projects/data-platform`.
- Before your first edit: `bd update trk-bam.3 --claim --actor claudeclaw`
- Once per round, after committing and before your final response:
  `bd comment trk-bam.3 --actor claudeclaw "<round N: what landed + commit SHA, or what stopped you>"`
- Never close the story, and never add or remove labels (never `agent`, never `human`).

## Out of scope

- Writing files (task 02 / 04).
- Window selection, retries and the CLI (task 04).
- Any write to Dolt, including temp tables, `CALL dolt_*` procedures and session variables that modify state.
- Any timezone conversion of values. Staging does that (task 05).
- Editing `internal/window`, `internal/record` or `internal/bronze`.
