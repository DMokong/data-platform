---
task: 02-writer
parallel_safe: false
testable: true
tier: judgment
story: trk-bam.2
acs: [AC-03, AC-04, AC-05, AC-06, AC-07, AC-08]
---

# 02-writer

## Goal

Build the bronze **Writer** in `internal/bronze`. It turns a `record.Batch` for one window into exactly one
Parquet file at a path derived only from the window. It stamps provenance columns, overwrites atomically on
re-run, and is byte-for-byte deterministic. This is the spec's idempotency requirement made executable (spec
§3 "Bronze", §7 "Go side": two runs of one window must produce byte-identical files). Fetchers and Go
transforms all write through it.

Work test-first: a test-author may already have written failing tests in this scope. Make them pass, and extend
them where the ACs demand more.

## Done-check

```
cd /Users/dustincheng/projects/data-platform && go test -count=1 ./internal/bronze/... && CGO_ENABLED=0 go build ./internal/bronze/... && ! go list -deps ./internal/bronze/... | grep -qi duckdb && echo DONE
```

## File scope

**Working-directory contract.** The target checkout is `/Users/dustincheng/projects/data-platform` (no
worktree), and the expected branch is `phase-1`. Before any write, run:

    cd /Users/dustincheng/projects/data-platform && [ "$(git rev-parse --show-toplevel)" = "/Users/dustincheng/projects/data-platform" ] && [ "$(git rev-parse --abbrev-ref HEAD)" = "phase-1" ] && echo CWD-OK

If it does not print `CWD-OK`, write nothing and escalate `broken_harness`.

Create:
- `internal/bronze/doc.go`, `internal/bronze/path.go`, `internal/bronze/writer.go`
- `internal/bronze/path_test.go`, `internal/bronze/writer_test.go`
- `internal/bronze/testdata/`, if you need fixtures

Modify:
- `go.mod`, `go.sum`: add parquet-go with `GOTOOLCHAIN=local go get github.com/parquet-go/parquet-go@v0.32.0`
  (spec F11). This task
  owns these two files in its wave; task 03 runs after you.

**Git.** Commit when verification passes, and at the end of every fix round.
- Stage only paths inside this File scope, with `git add -- <paths>`. Then commit with
  `git commit -m "<message>" -- <paths>`, so the commit contains only your paths even if a sibling task has
  staged files in the shared index. Never use `git add -A`, `git add .` or `git commit -a`.
- Never commit this task's `report.md`, anything under `bronze/`, `warehouse/`, `local/` or `.beads/`, or any
  `*.duckdb` file.
- Commit message: `<type>(<scope>): <summary>`, a blank line, `Stream-task: 2026-09-18-phase-1-build/02-writer round <N>`,
  a blank line, then `Co-Authored-By: Claude <your model name, e.g. Opus 5> <noreply@anthropic.com>`.
- If git reports an `index.lock`, wait 5 s and retry, at most 6 times. Do not push.

## Required content

**Exact exported API** (later tasks call these names):

```go
type Zone string
const (
    Raw     Zone = "raw"
    Derived Zone = "derived"
)
const (
    ColExtractedAt = "_extracted_at"
    ColWindowStart = "_window_start"
    ColWindowEnd   = "_window_end"
    ColSourceFile  = "_source_file"
)
// Path returns the bronze-relative path for a window, e.g. "raw/beads/events/dt=2026-09-15/hh=13.parquet".
func Path(zone Zone, dataset string, w window.Window) (string, error)

type Writer struct { Root string } // the bronze root directory, e.g. "<repo>/bronze"
type Result struct {
    Path  string // bronze-relative, identical to Path(...)
    Rows  int
    Bytes int64
}
func (wr *Writer) Write(ctx context.Context, zone Zone, dataset string, w window.Window, b record.Batch, extractedAt time.Time) (Result, error)
```

**Path rules (AC-03).**
- Hourly: `<zone>/<dataset>/dt=YYYY-MM-DD/hh=HH.parquet`. Daily:
  `<zone>/<dataset>/dt=YYYY-MM-DD/part-0.parquet`.
- `dt` and `hh` come from `w.Start` in UTC.
- Error on: a window failing `w.Validate()`; a zone other than `raw` / `derived`; an empty dataset; any
  `/`-separated dataset segment not matching `^[a-z0-9_]+$` (rejecting `..`, absolute paths and empty segments).
- The path must be a pure function: no clock, no environment, no filesystem.

**Write rules.**
- Call `b.Validate()` first. Reject a batch that already has a column named like a provenance column.
- *Schema (AC-06, AC-07).* Batch columns come first, in batch order, all OPTIONAL. Then `_extracted_at`,
  `_window_start`, `_window_end` as REQUIRED INT64 `TIMESTAMP(MICROS, isAdjustedToUTC=true)`, then
  `_source_file` as a REQUIRED string. Kind mapping:
  - `String` → BYTE_ARRAY with String logical type;
  - `Int64` → INT64;
  - `Float64` → DOUBLE;
  - `Bool` → BOOLEAN;
  - `Timestamp` → INT64 `TIMESTAMP(MICROS, isAdjustedToUTC=false)`, written from the value's wall-clock fields,
    unchanged.
- *Values.* NULL stays NULL. `_extracted_at` is `extractedAt` truncated to µs, in UTC. `_source_file` equals the
  bronze-relative path returned by `Path`.
- *Determinism (AC-04).* Use a fixed compression codec and fixed writer options, embed no wall-clock or random
  metadata, and keep the column and row order exactly as given. Same batch, window and `extractedAt` must give
  identical bytes.
- *Atomic overwrite (AC-05).*
  - `MkdirAll` the target directory.
  - Write to `os.CreateTemp(<target dir>, ".tmp-*")`, sync, close, and check `ctx.Err()` immediately before
    `os.Rename` over the target.
  - On any error, remove the temp file and leave any existing target untouched.
  - Never append to or read the old file.
- *Zero rows.* A zero-row batch writes a valid file with the full schema.
- *Root confinement.* Joining `Root` and the path must stay under `Root`; AC-03's segment rule already
  guarantees this.

**Tests** (`go test ./internal/bronze/...`) cover at least:
- the path table: hh=00, hh=23, a daily window, the day boundary (2026-09-15T23 vs 2026-09-16T00), and every
  error case;
- SHA-256 equality over two writes of the same inputs (into two different roots, same relative path), and
  inequality when only `extractedAt` changes;
- overwrite: write 3 rows then 1 row to the same window; the file has 1 row and the directory has no `.tmp-*`
  leftovers;
- failure, both ways, each leaving the prior file byte-identical and no temp file:
  - an encode failure injected through an unexported test seam (e.g. a package-level variable or option that
    swaps the encoder);
  - a context cancelled before the rename;
- provenance values read back from the written file;
- a type round-trip for every kind, including a NULL in each OPTIONAL column, asserting the Parquet logical
  types (isAdjustedToUTC false for `Timestamp`, true for provenance);
- a zero-row file readable with its schema;
- rejection of a provenance-named input column.

`TestWriteSampleForInspection`: when env `BRONZE_SAMPLE_OUT` is set, write one sample hourly-window file under
that directory as bronze root, then log its absolute path. Use zone `raw` and dataset `sample/kinds`, with one
column of each kind and one row that is all NULLs. Otherwise `t.Skip`.

`doc.go` explains, for a learner, immutable raw data, window-derived paths and overwrite-on-rerun, and includes a
line starting `// Data-engineering concept: ` (idempotent, replayable raw layer).

## Inputs

- `/Users/dustincheng/projects/data-platform/docs/data-platform-spec.md` (Principles; Requirement: idempotent
  re-runs; §2 Writer; §3 Bronze; §7 Go side)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/spec.md` (F4, F6, §3,
  AC-03 to AC-08)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/plan.md`
- `/Users/dustincheng/projects/data-platform/internal/window/window.go`
- `/Users/dustincheng/projects/data-platform/internal/record/record.go`
- The parquet-go v0.32.0 source in the module cache (`go env GOMODCACHE`), in particular `type_time.go` for
  `Timestamp` / `TimestampAdjusted`.

## Verification commands

```
cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/bronze)" && echo GOFMT-OK
cd /Users/dustincheng/projects/data-platform && go vet ./internal/bronze/...
cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/bronze/... 2>&1 | tail -40
cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./internal/bronze/... && ! go list -deps ./internal/bronze/... | grep -qi duckdb && echo NO-CGO-NO-DUCKDB
cd /Users/dustincheng/projects/data-platform && rm -rf local/tmp/bronze-sample && BRONZE_SAMPLE_OUT=$PWD/local/tmp/bronze-sample go test -count=1 -run TestWriteSampleForInspection ./internal/bronze/ && f=$(find local/tmp/bronze-sample -name '*.parquet') && echo "$f" && duckdb -c "describe select * from read_parquet('$f')" && duckdb -c "select * from read_parquet('$f')"
cd /Users/dustincheng/projects/data-platform && grep -E '^(go|toolchain) ' go.mod
cd /Users/dustincheng/projects/data-platform && grep -n 'parquet-go' go.mod && git log --oneline -3 && git status --short
```

Expected results:
- The sample `DESCRIBE` shows:
  - the `Timestamp` column as `TIMESTAMP`;
  - `_extracted_at`, `_window_start` and `_window_end` as `TIMESTAMP WITH TIME ZONE`;
  - `_source_file` as `VARCHAR`;
  - the String / Int64 / Float64 / Bool columns as `VARCHAR` / `BIGINT` / `DOUBLE` / `BOOLEAN`.
- The sample select shows one all-NULL row.
- The go directive is `go 1.25` or `go 1.25.x` (x ≤ 5), with no `toolchain` line.

## Report obligation

Append `## implementer — round <N>` to `tasks/02-writer/report.md`. Include:
- the files changed;
- your compression codec choice and why;
- how you made the timestamp logical types exact;
- each verification command with its exit code and output tail (≤30 lines);
- the commit SHA.

## Tracker

Story **trk-bam.2**. Run `bd` from `/Users/dustincheng/projects/data-platform`.
- Before your first edit: `bd update trk-bam.2 --claim --actor claudeclaw`
- Once per round, after committing and before your final response:
  `bd comment trk-bam.2 --actor claudeclaw "<round N: what landed + commit SHA, or what stopped you>"`
- Never close the story, and never add or remove labels (never `agent`, never `human`).

## Out of scope

- Fetching from any source (task 03).
- Deciding which windows to write (task 04).
- Reading Parquet back for production use (task 07).
- Compaction.
- Any DuckDB or CGO dependency.
- Editing `internal/window` or `internal/record`. If they are wrong, escalate `plan_invalidating_discovery`.
