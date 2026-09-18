---
task: 10-integration
parallel_safe: true
testable: false
tier: standard
story: trk-bam.10
acs: [AC-41, AC-42, AC-43, AC-44, AC-45, AC-46]
---

# 10-integration

## Goal

Make phase 1 usable and provable end to end:
- a `Makefile` exposing every step;
- a `README.md` runbook a newcomer can follow;
- a helper SQL file that lets the DuckDB UI browse mart Parquet in its own process;
- the Go module versions pinned in the decision log;
- `go mod tidy` clean, and `CLAUDE.md`'s toolchain note brought up to date.

Then **execute every runbook command** and record the evidence. This task only integrates and proves. If the
end-to-end run exposes a bug in another task's code, do not fix it here: escalate with the evidence.

## Done-check

```
cd /Users/dustincheng/projects/data-platform && make check && bash scripts/live-materialise.sh && echo DONE
```

## File scope

**Working-directory contract.** The target checkout is `/Users/dustincheng/projects/data-platform` (no
worktree), and the expected branch is `phase-1`. Before any write, run:

    cd /Users/dustincheng/projects/data-platform && [ "$(git rev-parse --show-toplevel)" = "/Users/dustincheng/projects/data-platform" ] && [ "$(git rev-parse --abbrev-ref HEAD)" = "phase-1" ] && echo CWD-OK

If it does not print `CWD-OK`, write nothing and escalate `broken_harness`.

Create:
- `Makefile`
- `README.md`
- `scripts/marts-views.sql`

Modify:
- `docs/data-platform-spec.md`: append one row to the **Decision log** table only.
- `go.mod`, `go.sum`: only if `go mod tidy -diff` reports changes, apply them with `GOTOOLCHAIN=local go mod
  tidy`; add or upgrade nothing.
- `CLAUDE.md`: the `## Toolchain` paragraph only. Replace "not yet installed" with "installed; pinned versions
  are in the spec's decision log", and name the install commands.

**Git.** Commit when verification passes, and at the end of every fix round.
- Stage only paths inside this File scope, with `git add -- <paths>`. Then commit with
  `git commit -m "<message>" -- <paths>`, so the commit contains only your paths even if a sibling task has
  staged files in the shared index. Never use `git add -A`, `git add .` or `git commit -a`.
- Never commit this task's `report.md`, anything under `bronze/`, `warehouse/`, `local/` or `.beads/`, or any
  `*.duckdb` file.
- Commit message: `<type>(<scope>): <summary>`, a blank line, `Stream-task: 2026-09-18-phase-1-build/10-integration round <N>`,
  a blank line, then `Co-Authored-By: Claude <your model name, e.g. Sonnet 5> <noreply@anthropic.com>`.
- If git reports an `index.lock`, wait 5 s and retry, at most 6 times. Do not push.

## Required content

**Makefile.** POSIX-friendly, with a `help` default target listing everything. Export
`TEMPORAL_ADDRESS ?= 127.0.0.1:7233` for every target: `localhost` resolves to `::1` and gRPC hangs (spec F10).
Targets:
- `fmt-check`: fail if `gofmt -l .` prints anything.
- `vet`, `test` (`go test ./...`), `test-live` (`BEADS_LIVE=1 go test ./...`).
- `build`: `CGO_ENABLED=0 go build -o bin/ ./cmd/...`.
- `check`: `fmt-check vet test-live build`, plus the doc.go (AC-41), file-writer (AC-42) and hygiene (AC-44)
  checks below, each printing `OK`. Hygiene includes `go mod tidy -diff` and a go directive of 1.25.x (x ≤ 5)
  with no `toolchain` line.
- `temporal-dev`: `temporal server start-dev --db-filename local/temporal.db`, foreground.
- `worker`: `go run ./cmd/worker -root .`, foreground.
- `schedule`: `go run ./cmd/worker -apply-schedule`.
- `trigger`: `temporal schedule trigger --schedule-id materialise-beads`.
- `ingest-once`: `go run ./cmd/ingest --source beads --once`.
- `backfill`: `go run ./cmd/ingest --source beads --from $(FROM) --to $(TO)`. `FROM` defaults to `2026-08-21`
  and `TO` to today in UTC.
- `dbt-pre` / `dbt-post`: `mkdir -p warehouse/marts`, then `dbt build --project-dir dbt --profiles-dir dbt
  --select tag:pre_go` (or `tag:post_go`).
- `transform`: `go run ./cmd/transform issue_mentions --window $(WINDOW)`. `WINDOW` defaults to today in UTC.
- `build-marts`: `dbt-pre transform dbt-post`.
- `freshness`: `dbt source freshness --project-dir dbt --profiles-dir dbt`.
- `live`: `bash scripts/live-materialise.sh`.
- `ui`: `duckdb -ui -init scripts/marts-views.sql`. This is interactive and opens a browser, so it is not run in
  verification.

Nothing in the Makefile deletes `bronze/`.

**`scripts/marts-views.sql`.** `CREATE OR REPLACE VIEW` statements, one per mart, in an **in-memory** DuckDB
session. Each reads `read_parquet('warehouse/marts/<model>/**/*.parquet')`. It is meant to be run from the repo
root. It never opens `warehouse/dev.duckdb`: the reader runs in its own process and never touches the writer's
file (spec §9, decision log).

**`README.md`** sections:
- what this is;
- the layout, with each Go package and its `Data-engineering concept:` line, pulled from the `doc.go` files;
- prerequisites and the exact install commands (spec F9 / decision log);
- configuration (the env vars: `BEADS_*`, `TEMPORAL_ADDRESS`, `TEMPORAL_NAMESPACE`, `PLATFORM_OTEL_EXPORTER`,
  standard `OTEL_EXPORTER_OTLP_*`, `PLATFORM_DBT_BIN`);
- a runbook with numbered steps: dev server → worker → schedule → trigger → backfill → ingest-once →
  build-marts → freshness → UI. Say explicitly that `make live` performs the dev-server, worker, schedule and
  trigger steps end to end (AC-46);
- how idempotency and replay are demonstrated (the task 04 drills);
- phase-1 source facts a reader must know (spec F1–F3);
- troubleshooting: the DuckDB single-writer rule, `warehouse/marts` needing to exist, and Dolt being read-only.

Every command in the runbook is one you execute below.

**Decision log row** (date `2026-09-18`): the Go module pins exactly as they appear in `go.mod` after tidy
(parquet-go, go-sql-driver/mysql, go.temporal.io/sdk, go.temporal.io/sdk/contrib/opentelemetry, and the
go.opentelemetry.io/otel family with its version). Reasoning: pure Go, no CGO (principle 6), and reproducible
builds.

**End-to-end execution**, recorded in your report with tails. Run, in this order:
1. `make check`
2. `make backfill` (the full history)
3. `make ingest-once`
4. `rm -rf warehouse && make build-marts` (dbt-pre, transform, dbt-post)
5. `make freshness`
6. The UI view file, non-interactively: `duckdb -c ".read scripts/marts-views.sql" -c "select count(*) from dim_issues"`
7. `make live`
8. Every hygiene and scan command in the Verification section

Do not delete `bronze/`.

## Inputs

- `/Users/dustincheng/projects/data-platform/docs/data-platform-spec.md`
- `/Users/dustincheng/projects/data-platform/CLAUDE.md`
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/spec.md` (all of it;
  AC-41 to AC-46 in particular)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/plan.md`
- every `doc.go` (`find cmd internal -name doc.go`)
- `/Users/dustincheng/projects/data-platform/scripts/live-materialise.sh`
- `/Users/dustincheng/projects/data-platform/go.mod`
- `/Users/dustincheng/projects/data-platform/dbt/dbt_project.yml`

## Verification commands

```
cd /Users/dustincheng/projects/data-platform && make check 2>&1 | tail -25
cd /Users/dustincheng/projects/data-platform && for d in $(go list -f '{{.Dir}}' ./...); do grep -q '^// Data-engineering concept: ' "$d/doc.go" 2>/dev/null || echo "MISSING $d/doc.go"; done; echo "packages: $(go list ./... | wc -l | tr -d ' ')"
cd /Users/dustincheng/projects/data-platform && grep -rnE 'os\.(Create|CreateTemp|WriteFile|OpenFile|Rename)\(' --include='*.go' cmd internal | grep -v '_test.go'
cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./... && echo "duckdb-deps: $(go list -deps ./... | grep -ci duckdb)"
cd /Users/dustincheng/projects/data-platform && GOTOOLCHAIN=local go mod tidy -diff && echo TIDY-CLEAN; grep -E '^(module|go|toolchain) ' go.mod
cd /Users/dustincheng/projects/data-platform && echo "tracked-local-state: $(git ls-files | grep -cE '^(bronze|warehouse|local|\.beads)/|\.duckdb$')"; git rev-parse --abbrev-ref HEAD
cd /Users/dustincheng/projects/data-platform && make backfill 2>&1 | tail -10
cd /Users/dustincheng/projects/data-platform && make ingest-once 2>&1 | tail -10
cd /Users/dustincheng/projects/data-platform && rm -rf warehouse && make build-marts 2>&1 | tail -12
cd /Users/dustincheng/projects/data-platform && make freshness 2>&1 | tail -12
cd /Users/dustincheng/projects/data-platform && duckdb -c ".read scripts/marts-views.sql" -c "select count(*) from dim_issues" -c "select count(*) from mart_issue_mention_drift"
cd /Users/dustincheng/projects/data-platform && make live 2>&1 | tail -25
cd /Users/dustincheng/projects/data-platform && grep -n '2026-09-18' docs/data-platform-spec.md && sed -n '/^## Toolchain/,/^## /p' CLAUDE.md
cd /Users/dustincheng/projects/data-platform && git log --oneline -12 && git status --short
```

Expected results:
- No `MISSING` lines.
- The writer grep lists only files under `internal/bronze/` and `internal/orchestrator/spool.go` (or the spool
  file's actual name).
- `duckdb-deps: 0`, `TIDY-CLEAN`, `tracked-local-state: 0`, and branch `phase-1`.
- The go directive is 1.25.x (x ≤ 5), with no `toolchain` line.
- The last line of the live output is `LIVE-MATERIALISE: PASS`.

`make check`, `make backfill`, `make build-marts` and `make live` are **(long)**: run them with the Bash tool
timeout at 600000 ms.

## Report obligation

Append `## implementer — round <N>` to `tasks/10-integration/report.md`. Include:
- the files changed;
- each end-to-end step and verification command with its exit code and output tail (≤30 lines), in the order
  run;
- wall times for the backfill and the live run;
- the commit SHA.

## Tracker

Story **trk-bam.10**. Run `bd` from `/Users/dustincheng/projects/data-platform`.
- Before your first edit: `bd update trk-bam.10 --claim --actor claudeclaw`
- Once per round, after committing and before your final response:
  `bd comment trk-bam.10 --actor claudeclaw "<round N: what landed + commit SHA, or what stopped you>"`
- Never close the story, and never add or remove labels (never `agent`, never `human`).

## Out of scope

- Fixing code in any other task's files: escalate instead.
- Adding dependencies.
- CI.
- Any BI tool beyond the DuckDB UI view file.
- Deleting `bronze/`.
- Pushing.
- Editing any part of `docs/data-platform-spec.md` except the decision-log table.
