---
task: 01-foundation
parallel_safe: true
testable: true
tier: standard
story: trk-bam.1
acs: [AC-01, AC-02, AC-45 (toolchain rows only)]
---

# 01-foundation

## Goal

Create the Go module and the two small, pure packages every later task imports:
- `internal/window`: time windows, lookback and backfill ranges. This is the idempotency backbone: files are
  derived from windows, never from the wall clock.
- `internal/record`: the in-memory batch contract shared by Fetchers, the Writer and Go transforms.

Also append the installed toolchain versions to the spec's decision log, as the kickoff requires. Later tasks
depend on the exact names below, so do not rename them.

## Done-check

```
cd /Users/dustincheng/projects/data-platform && go test -count=1 ./internal/window/... ./internal/record/... && head -1 go.mod | grep -qx 'module github.com/DMokong/data-platform' && grep -q 'dbt-duckdb 1.11.0' docs/data-platform-spec.md && echo DONE
```

## File scope

**Working-directory contract.** The target checkout is `/Users/dustincheng/projects/data-platform` (no
worktree), and the expected branch is `phase-1`. Before any write, run:

    cd /Users/dustincheng/projects/data-platform && [ "$(git rev-parse --show-toplevel)" = "/Users/dustincheng/projects/data-platform" ] && [ "$(git rev-parse --abbrev-ref HEAD)" = "phase-1" ] && echo CWD-OK

If it does not print `CWD-OK`, write nothing and escalate `broken_harness`.

Create:
- `go.mod`: `module github.com/DMokong/data-platform` with a `go 1.25` directive. Standard library only, so no
  `require` lines and no `go.sum` are expected.
- `internal/window/doc.go`, `internal/window/window.go`, `internal/window/window_test.go`
- `internal/record/doc.go`, `internal/record/record.go`, `internal/record/record_test.go`

Modify:
- `docs/data-platform-spec.md`: append rows to the **Decision log** table only; change nothing else in that
  file.

**Git.** Commit when verification passes, and at the end of every fix round.
- Stage only paths inside this File scope, with `git add -- <paths>`. Then commit with
  `git commit -m "<message>" -- <paths>`, so the commit contains only your paths even if a sibling task has
  staged files in the shared index. Never use `git add -A`, `git add .` or `git commit -a`.
- Never commit this task's `report.md`, anything under `bronze/`, `warehouse/`, `local/` or `.beads/`, or any
  `*.duckdb` file.
- Commit message: `<type>(<scope>): <summary>`, a blank line, `Stream-task: 2026-09-18-phase-1-build/01-foundation round <N>`,
  a blank line, then `Co-Authored-By: Claude <your model name, e.g. Sonnet 5> <noreply@anthropic.com>`.
- If git reports an `index.lock`, wait 5 s and retry, at most 6 times. Do not push.

## Required content

**`internal/window`** (exact exported API):

```go
type Grain int
const (
    Hour Grain = iota + 1
    Day
)
func (g Grain) Duration() time.Duration   // 1h, 24h
func (g Grain) String() string            // "hour", "day"

type Window struct {
    Start time.Time `json:"start"` // UTC, aligned to Grain
    End   time.Time `json:"end"`   // Start + Grain.Duration(); half-open [Start, End)
    Grain Grain     `json:"grain"`
}
func Containing(t time.Time, g Grain) Window                      // aligned window holding instant t (any location)
func Lookback(now time.Time, g Grain, n int) ([]Window, error)     // n windows, oldest first, last contains now; n < 1 → error
func Range(from, to time.Time, g Grain) ([]Window, error)          // every window with Start in [from's UTC date 00:00Z, (to's UTC date + 1 day) 00:00Z); from's date > to's date → error
func (w Window) Validate() error   // known grain, Start/End in UTC, Start aligned, End == Start + grain
func (w Window) String() string    // hour: "2026-09-15T13"   day: "2026-09-15"
```

- All returned `Start` / `End` values carry `time.UTC` as their location, so equality and JSON are
  deterministic.
- `Range` uses only the UTC calendar date of `from` and `to` (time of day is ignored); both dates are inclusive.

**`internal/record`** (exact exported API):

```go
type Kind int
const (
    String Kind = iota + 1 // UTF-8 text (varchar, char, text, json)
    Int64                  // any integer
    Float64
    Bool
    Timestamp              // naive wall-clock timestamp: a time.Time in time.UTC whose wall fields are the source's value
)
func (k Kind) String() string
type Column struct { Name string; Kind Kind }
type Batch struct { Columns []Column; Rows [][]any } // each value: nil | string | int64 | float64 | bool | time.Time
func (b Batch) Validate() error
```

`Validate` rejects:
- an empty column name or a duplicate column name;
- an unknown kind;
- a row whose length differs from `len(Columns)`;
- a non-nil value whose Go type does not match its column's kind;
- a `Timestamp` value whose location is not `time.UTC`.

**Tests** are table-driven. They must at least cover:
- the AC-01 examples:
  - `Lookback(2026-09-15T00:30Z, Hour, 24)` runs from 2026-09-14T01:00Z to 2026-09-15T00:00Z;
  - `Lookback(same, Day, 2)` returns [2026-09-14, 2026-09-15];
  - `Range(2026-09-14, 2026-09-15, Hour)` has 48 windows and `Day` has 2;
- a non-UTC input (e.g. an instant in `Australia/Sydney`) giving the same window as its UTC instant;
- `Lookback` with n = 0, and `Range` with from > to (both errors);
- `Validate` on a misaligned window;
- `String()` for both grains;
- every `Batch.Validate` rejection case, plus a valid batch.

**doc.go**: each package's doc comment explains, in two to five lines for a learner, what the package does. It
must include a line that starts exactly `// Data-engineering concept: `:
- `window`: event-time windowing and replayable, window-derived partitions;
- `record`: the schema-carrying batch that forms the extract/load contract.

**Decision log**: append one row dated `2026-09-18` recording the pinned toolchain. Run `go version`,
`dbt --version`, `duckdb --version` and `temporal --version` and record what you observe; expected (F9 in the
spec): Go 1.25.5; dbt-core 1.12.5 with dbt-duckdb 1.11.0 installed via
`uv tool install --python 3.12 dbt-core --with dbt-duckdb`, bundling duckdb 1.5.5; DuckDB CLI 1.5.5 and Temporal
CLI 1.9.1 (server 1.32.0, UI 2.54.1) via Homebrew. Reasoning column: free, no accounts (principle 5), and exact
versions make the dbt-duckdb and Temporal behaviour reproducible. If an observed version differs from F9,
record the observed one and say so in your report.

## Inputs

- `/Users/dustincheng/projects/data-platform/CLAUDE.md`
- `/Users/dustincheng/projects/data-platform/docs/data-platform-spec.md` (the idempotency section and the
  decision log)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/spec.md` (§3 and
  AC-01, AC-02, AC-45)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/plan.md`
  (Global constraints, Interfaces)

## Verification commands

```
cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/window internal/record)" && echo GOFMT-OK
cd /Users/dustincheng/projects/data-platform && go vet ./internal/window/... ./internal/record/...
cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/window/... ./internal/record/... 2>&1 | tail -40
cd /Users/dustincheng/projects/data-platform && head -3 go.mod
cd /Users/dustincheng/projects/data-platform && grep -n '2026-09-18' docs/data-platform-spec.md
cd /Users/dustincheng/projects/data-platform && grep -H '^// Data-engineering concept: ' internal/window/doc.go internal/record/doc.go
cd /Users/dustincheng/projects/data-platform && git log --oneline -3 && git status --short
```

## Report obligation

Append `## implementer — round <N>` to `tasks/01-foundation/report.md`. Include:
- every file created or modified;
- each verification command with its exit code and output tail (≤30 lines);
- the commit SHA;
- the versions you observed for the decision-log row.

## Tracker

Story **trk-bam.1**. Run `bd` from `/Users/dustincheng/projects/data-platform`; its `.beads/redirect`
resolves the shared tracker.
- Before your first edit: `bd update trk-bam.1 --claim --actor claudeclaw`
- Once per round, after committing and before your final response:
  `bd comment trk-bam.1 --actor claudeclaw "<round N: what landed + commit SHA, or what stopped you>"`
- Never close the story, and never add or remove labels (never `agent`, never `human`).

## Out of scope

- Bronze paths and Parquet (task 02).
- Any SQL or Dolt access (task 03).
- Adding any third-party module.
- Any edit to `docs/data-platform-spec.md` outside the decision-log table.
- Editing `CLAUDE.md` or `.gitignore`.
