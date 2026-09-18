---
task: 04-runner
parallel_safe: true
testable: true
tier: standard
story: trk-bam.4
acs: [AC-15, AC-16, AC-17, AC-18, AC-19]
---

# 04-runner

## Goal

Build the **Runner** (`internal/runner`) and the `ingest` CLI (`cmd/ingest`). They wire a source's Fetcher to the
Writer over windows:
- a **lookback run** (`--once`): the trailing windows per table, as declared;
- a **backfill** (`--from … --to …`): every window in a date range, overwriting each file.

Retries are bounded and a failed window never leaves a partial file. The Runner has no loop and no ticker: the
Temporal Schedule (task 06) provides cadence. This is where "backfill is free because of the rules above"
(spec, idempotency section) becomes a command you can run.

## Done-check

```
cd /Users/dustincheng/projects/data-platform && go test -count=1 ./internal/runner/... ./cmd/ingest/... && rm -rf bronze/raw/beads && go run ./cmd/ingest --source beads --from 2026-09-14 --to 2026-09-15 && [ "$(find bronze/raw/beads/events -name '*.parquet' | wc -l | tr -d ' ')" = 48 ] && [ "$(find bronze/raw/beads/issues -name '*.parquet' | wc -l | tr -d ' ')" = 2 ] && echo DONE
```

## File scope

**Working-directory contract.** The target checkout is `/Users/dustincheng/projects/data-platform` (no
worktree), and the expected branch is `phase-1`. Before any write, run:

    cd /Users/dustincheng/projects/data-platform && [ "$(git rev-parse --show-toplevel)" = "/Users/dustincheng/projects/data-platform" ] && [ "$(git rev-parse --abbrev-ref HEAD)" = "phase-1" ] && echo CWD-OK

If it does not print `CWD-OK`, write nothing and escalate `broken_harness`.

Create:
- `internal/runner/doc.go`, `internal/runner/runner.go`, `internal/runner/runner_test.go`
- `cmd/ingest/doc.go`, `cmd/ingest/main.go`, and `cmd/ingest/main_test.go` if you test flag parsing

Modify:
- `go.mod` / `go.sum`: only if a standard-library-only implementation is impossible, which it should not be.

Local, uncommitted artefacts you will create while verifying: `bronze/` and `local/tmp/`, both gitignored.

**Git.** Commit when verification passes, and at the end of every fix round.
- Stage only paths inside this File scope, with `git add -- <paths>`. Then commit with
  `git commit -m "<message>" -- <paths>`, so the commit contains only your paths even if a sibling task has
  staged files in the shared index. Never use `git add -A`, `git add .` or `git commit -a`.
- Never commit this task's `report.md`, anything under `bronze/`, `warehouse/`, `local/` or `.beads/`, or any
  `*.duckdb` file.
- Commit message: `<type>(<scope>): <summary>`, a blank line, `Stream-task: 2026-09-18-phase-1-build/04-runner round <N>`,
  a blank line, then `Co-Authored-By: Claude <your model name, e.g. Sonnet 5> <noreply@anthropic.com>`.
- If git reports an `index.lock`, wait 5 s and retry, at most 6 times. Do not push.

## Required content

**`internal/runner`** (exact exported API):

```go
type Runner struct {
    Source      source.Source
    Fetcher     source.Fetcher
    Writer      *bronze.Writer
    Now         func() time.Time // extraction clock for _extracted_at; nil → time.Now
    Attempts    int              // tries per window; 0 → 3
    Backoff     time.Duration    // base delay between tries, doubled each retry; tests set it tiny
    Concurrency int              // windows in flight; 0 → 4
    Log         *slog.Logger     // nil → slog.Default()
}
type TableSummary struct {
    Table   string
    Windows int
    Rows    int
    Bytes   int64
    Failed  []string // "table window: error"
}
type Summary struct { Tables []TableSummary } // in declaration order
func (r *Runner) RunOnce(ctx context.Context, now time.Time) (Summary, error)          // per table: window.Lookback(now, table.Grain, table.Lookback)
func (r *Runner) Backfill(ctx context.Context, from, to time.Time) (Summary, error)    // per table: window.Range(from, to, table.Grain)
```

**Runner behaviour.**
- For every (table, window), stamp `extractedAt := r.Now()` immediately before `Fetch`, then call
  `Writer.Write(ctx, bronze.Raw, <source>/<table>, w, batch, extractedAt)`.
- A failed fetch or write is retried up to `Attempts` times with `Backoff` doubling. Other windows keep going.
- After all windows finish, return the summary and, if anything failed, a non-nil error (`errors.Join`) naming
  every failed table and window.
- The Writer's atomic rename already guarantees that a failed window leaves no partial file; do not delete or
  pre-create files.
- Log one structured line per window (table, window, rows, bytes, attempt).

**`cmd/ingest`.**
- Flags:
  - `--source` (required; only `beads` is registered);
  - backfill mode: `--from` / `--to` (`YYYY-MM-DD`);
  - lookback mode: `--once`, with optional `--now` (RFC 3339; default: current time);
  - `--root` (repo root, default `.`; bronze root = `<root>/bronze`).
- Exactly one mode must be given. Invalid combinations print usage and exit 2.
- Build the beads Fetcher from `beads.ConfigFromEnv()` and close it on exit.
- Print one summary line per table in the form `table=<name> windows=<n> rows=<n> bytes=<n>`. Exit 1 when any
  window failed, after listing every failure.

**Tests.**
- Unit tests with a fake `source.Fetcher` and a temp bronze root:
  - lookback window selection for a fixed `now` (24 hourly and 2 daily windows, correct first and last);
  - backfill counts;
  - a fetcher that fails twice then succeeds is retried and succeeds;
  - a fetcher that always fails for one window makes the run return an error naming that window, while every
    other window's file is written and the failing window's previous file stays untouched;
  - summary totals.
- Use `Backoff` ≈ 1 ms in tests.

`doc.go` for each package includes a line starting `// Data-engineering concept: `:
- `runner`: replayable backfill and lookback re-extraction;
- `cmd/ingest`: backfill as a first-class command.

## Inputs

- `/Users/dustincheng/projects/data-platform/docs/data-platform-spec.md` (Requirement: idempotent re-runs; §2
  Runner)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/spec.md` (§3, AC-15 to
  AC-19)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/plan.md`
- `/Users/dustincheng/projects/data-platform/internal/window/window.go`,
  `/Users/dustincheng/projects/data-platform/internal/bronze/writer.go`,
  `/Users/dustincheng/projects/data-platform/internal/source/source.go`,
  `/Users/dustincheng/projects/data-platform/internal/source/beads/fetcher.go`,
  `/Users/dustincheng/projects/data-platform/internal/source/beads/config.go`

## Verification commands

```
cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/runner cmd/ingest)" && echo GOFMT-OK
cd /Users/dustincheng/projects/data-platform && go vet ./internal/runner/... ./cmd/ingest/...
cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/runner/... ./cmd/ingest/... 2>&1 | tail -30
cd /Users/dustincheng/projects/data-platform && rm -rf bronze/raw/beads && go run ./cmd/ingest --source beads --from 2026-09-14 --to 2026-09-15; echo "exit=$?"; for t in events dolt_history_issues dolt_log issues comments dependencies labels; do echo "$t $(find bronze/raw/beads/$t -name '*.parquet' | wc -l | tr -d ' ')"; done
cd /Users/dustincheng/projects/data-platform && rm -rf local/tmp/before && mkdir -p local/tmp && cp -R bronze/raw/beads local/tmp/before && (cd bronze/raw/beads && find . -name '*.parquet' | sort) > local/tmp/files-before.txt && go run ./cmd/ingest --source beads --from 2026-09-14 --to 2026-09-15 >/dev/null && (cd bronze/raw/beads && find . -name '*.parquet' | sort) > local/tmp/files-after.txt && diff local/tmp/files-before.txt local/tmp/files-after.txt && echo SAME-FILE-SET
cd /Users/dustincheng/projects/data-platform && for t in events dolt_history_issues dolt_log issues comments dependencies labels; do echo "$t $(duckdb -noheader -list -c "select (select count(*) from (select * exclude (_extracted_at) from read_parquet('local/tmp/before/$t/*/*.parquet', union_by_name=true) except all select * exclude (_extracted_at) from read_parquet('bronze/raw/beads/$t/*/*.parquet', union_by_name=true))) || '/' || (select count(*) from (select * exclude (_extracted_at) from read_parquet('bronze/raw/beads/$t/*/*.parquet', union_by_name=true) except all select * exclude (_extracted_at) from read_parquet('local/tmp/before/$t/*/*.parquet', union_by_name=true)))")"; done
cd /Users/dustincheng/projects/data-platform && q="select count(*) || '/' || coalesce(sum(hash(e)),0) from (select * exclude (_extracted_at) from read_parquet('bronze/raw/beads/events/dt=2026-09-15/*.parquet')) e" && before=$(duckdb -noheader -list -c "$q") && rm -rf bronze/raw/beads/events/dt=2026-09-15 && go run ./cmd/ingest --source beads --from 2026-09-15 --to 2026-09-15 >/dev/null && after=$(duckdb -noheader -list -c "$q") && echo "before=$before after=$after files=$(ls bronze/raw/beads/events/dt=2026-09-15 | wc -l | tr -d ' ')" && [ "$before" = "$after" ] && echo REPLAY-OK
cd /Users/dustincheng/projects/data-platform && rm -rf bronze/raw/beads && go run ./cmd/ingest --source beads --once --now 2026-09-15T00:30:00Z; echo "exit=$?"; for t in events issues; do echo "$t $(find bronze/raw/beads/$t -name '*.parquet' | sort | sed -n '1p;$p' | tr '\n' ' ') count=$(find bronze/raw/beads/$t -name '*.parquet' | wc -l | tr -d ' ')"; done
cd /Users/dustincheng/projects/data-platform && go run ./cmd/ingest --source beads --once >/dev/null; echo "exit=$?"; echo "newest-events: $(find bronze/raw/beads/events -name '*.parquet' | sort | tail -1)"; echo "expected-suffix: dt=$(date -u +%F)/hh=$(date -u +%H).parquet"
cd /Users/dustincheng/projects/data-platform && git log --oneline -3 && git status --short
```

Commands 4 to 9 query the live Dolt server and are **(long)**: run each with the Bash tool timeout at 600000 ms.

Expected results:
- Command 4: counts 48 / 48 / 48 / 2 / 2 / 2 / 2 and `exit=0`.
- Command 6: every table prints `0/0`.
- The replay command prints `files=24` and `REPLAY-OK`.
- The plain `--once` run prints `exit=0`, and `newest-events` ends with `expected-suffix`. If the command ran
  across an hour boundary, the previous hour is acceptable.
- The `--once` run: events runs from `dt=2026-09-14/hh=01.parquet` to `dt=2026-09-15/hh=00.parquet` with
  count 24; issues has `dt=2026-09-14` and `dt=2026-09-15` with count 2.

## Report obligation

Append `## implementer — round <N>` to `tasks/04-runner/report.md`. Include:
- the files changed;
- each verification command with its exit code and output tail (≤30 lines);
- how long the two-day backfill took;
- the commit SHA.

## Tracker

Story **trk-bam.4**. Run `bd` from `/Users/dustincheng/projects/data-platform`.
- Before your first edit: `bd update trk-bam.4 --claim --actor claudeclaw`
- Once per round, after committing and before your final response:
  `bd comment trk-bam.4 --actor claudeclaw "<round N: what landed + commit SHA, or what stopped you>"`
- Never close the story, and never add or remove labels (never `agent`, never `human`).

## Out of scope

- Any scheduling loop or ticker (the Temporal Schedule is task 06).
- Temporal code.
- dbt.
- Changes to `internal/window`, `internal/record`, `internal/bronze` or `internal/source/**`. If one is wrong,
  escalate `plan_invalidating_discovery` with evidence.
