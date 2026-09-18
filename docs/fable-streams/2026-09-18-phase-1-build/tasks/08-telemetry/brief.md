---
task: 08-telemetry
parallel_safe: true
testable: true
tier: standard
story: trk-bam.8
acs: [AC-38, AC-39 (package-level parts)]
---

# 08-telemetry

## Goal

Build two standalone, well-tested packages that task 09 wires into the Temporal worker.

- **`internal/telemetry`** sets up OpenTelemetry tracing and metrics (spec §10). The OTLP exporter defaults to
  the workspace's local Alloy collector on `127.0.0.1:4317` (spec F8, F10), with stdout and none as alternatives. It
  exposes the platform's instruments: rows and bytes written, activity duration and outcome, and dbt node
  results. It also turns dbt's `run_results.json` into spans and metrics, "into the same stream" as the
  orchestrator.
- **`internal/dbt`** is the adapter the orchestrator uses to shell out to `dbt build --select <selector>` and
  parse `run_results.json`.

## Done-check

```
cd /Users/dustincheng/projects/data-platform && go test -count=1 ./internal/telemetry/... ./internal/dbt/... && CGO_ENABLED=0 go build ./internal/telemetry/... ./internal/dbt/... && echo DONE
```

## File scope

**Working-directory contract.** The target checkout is `/Users/dustincheng/projects/data-platform` (no
worktree), and the expected branch is `phase-1`. Before any write, run:

    cd /Users/dustincheng/projects/data-platform && [ "$(git rev-parse --show-toplevel)" = "/Users/dustincheng/projects/data-platform" ] && [ "$(git rev-parse --abbrev-ref HEAD)" = "phase-1" ] && echo CWD-OK

If it does not print `CWD-OK`, write nothing and escalate `broken_harness`.

Create:
- `internal/telemetry/doc.go`, `setup.go`, `metrics.go`, `dbtspans.go`, `*_test.go`
- `internal/dbt/doc.go`, `runresults.go`, `build.go`, `*_test.go`
- `internal/dbt/testdata/run_results_pass.json`, `internal/dbt/testdata/run_results_fail.json`
- `internal/dbt/testdata/fake-dbt.sh`: exactly this path, because task 09's tests reuse it

Modify:
- `go.mod`, `go.sum`: add the OpenTelemetry modules with `GOTOOLCHAIN=local go get <module>@v1.46.0`, all pinned
  at **v1.46.0**, a set the conductor verified builds on Go 1.25.5 (spec F11). You need `go.opentelemetry.io/otel`, `…/otel/sdk`, `…/otel/sdk/metric`,
  `…/otel/exporters/otlp/otlptrace/otlptracegrpc`, `…/otel/exporters/otlp/otlpmetric/otlpmetricgrpc`,
  `…/otel/exporters/stdout/stdouttrace` and `…/otel/exporters/stdout/stdoutmetric`. This task owns these files
  in its wave.

**Parallel-wave hygiene.** Task 07 is running at the same time. It edits `internal/transform/**`,
`cmd/transform/**` and `dbt/**`, and it deletes and rebuilds `warehouse/`.
- **Never run the real `dbt` binary**, and never touch `warehouse/` or `dbt/`.
- Build and test only your own packages; never run `go build ./...` or `go test ./...`.
- For the passing fixture, copy `local/fixtures/run_results_pre_go.json`. The conductor snapshotted it from a
  full `tag:pre_go` build between waves 4 and 5. Never read `dbt/target/`: task 07 rewrites it concurrently. Derive the failing fixture from it by
  setting one model to `error` and one test to `fail`, with realistic `message` / `failures` fields.

**Git.** Commit when verification passes, and at the end of every fix round.
- Stage only paths inside this File scope, with `git add -- <paths>`. Then commit with
  `git commit -m "<message>" -- <paths>`, so the commit contains only your paths even if a sibling task has
  staged files in the shared index. Never use `git add -A`, `git add .` or `git commit -a`.
- Never commit this task's `report.md`, anything under `bronze/`, `warehouse/`, `local/` or `.beads/`, or any
  `*.duckdb` file.
- Commit message: `<type>(<scope>): <summary>`, a blank line, `Stream-task: 2026-09-18-phase-1-build/08-telemetry round <N>`,
  a blank line, then `Co-Authored-By: Claude <your model name, e.g. Sonnet 5> <noreply@anthropic.com>`.
- If git reports an `index.lock`, a sibling task is committing: wait 5 s and retry, at most 6 times. Do not
  push.

## Required content

**`internal/dbt`** (exact exported API):

```go
type Timing struct { Name string; StartedAt, CompletedAt time.Time }
type NodeResult struct {
    UniqueID      string   // e.g. "model.data_platform.dim_issues"
    ResourceType  string   // first dot-segment of UniqueID: model | test | seed | snapshot | …
    Name          string   // last dot-segment of UniqueID
    Status        string   // success | error | fail | pass | warn | skipped | runtime error
    ExecutionTime float64
    Timing        []Timing
    Message       string
    Failures      *int
}
type RunResults struct { InvocationID string; GeneratedAt time.Time; ElapsedTime float64; Results []NodeResult }
func ParseRunResults(r io.Reader) (RunResults, error)
func (rr RunResults) Failed() []NodeResult              // status error | fail | runtime error
type Runner struct {
    Bin         string // default "dbt"
    WorkDir     string // repo root; dbt runs with this as its working directory
    ProjectDir  string // default "dbt"
    ProfilesDir string // default "dbt"
}
// Build ensures <WorkDir>/warehouse/marts exists, runs
// `<Bin> build --project-dir <ProjectDir> --profiles-dir <ProfilesDir> --select <selector>`,
// then parses <WorkDir>/<ProjectDir>/target/run_results.json.
// If dbt exits non-zero, it returns the parsed results together with an error naming the failed nodes.
// It records the start time before exec. If run_results.json is missing, or its metadata.generated_at is
// before that start time, it returns an error such as "dbt produced no run results", never a stale file's nodes.
func (r Runner) Build(ctx context.Context, selector string) (RunResults, error)
```

**`internal/telemetry`** (exact exported API):

```go
type Config struct {
    ServiceName string    // "data-platform-worker"
    Exporter    string    // "otlp" | "stdout" | "none"
    Stdout      io.Writer // used when Exporter == "stdout"; nil → os.Stdout
}
func ConfigFromEnv(serviceName string) Config // PLATFORM_OTEL_EXPORTER, default "otlp"; OTLP endpoint via standard OTEL_EXPORTER_OTLP_* env, else 127.0.0.1:4317 insecure (see OTLP below)
func Setup(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) // installs global TracerProvider + MeterProvider with resource service.name; unknown exporter → error; shutdown flushes both
func RecordWrite(ctx context.Context, source, table, zone string, rows int, bytes int64)       // platform.rows_written, platform.bytes_written (Int64Counter; attrs source, table, zone)
func RecordActivity(ctx context.Context, activity string, d time.Duration, err error)          // platform.activity.duration (Float64Histogram, seconds; attrs activity, outcome=success|failure)
func EmitDbtResults(ctx context.Context, selector string, rr dbt.RunResults)                  // one span per node + platform.dbt.nodes counter
```

- *Instruments.* Obtain them from the **global** providers at call time (e.g. `otel.Meter(...)` inside a
  `sync.Once` keyed on the provider, or simply per call), so tests can install an in-memory provider.
- *OTLP.* Use the gRPC exporters so the standard `OTEL_EXPORTER_OTLP_*` env vars apply when set. When **no**
  endpoint env var is set (`OTEL_EXPORTER_OTLP_ENDPOINT`, `…_TRACES_ENDPOINT`, `…_METRICS_ENDPOINT`), pass an
  explicit endpoint `127.0.0.1:4317` with insecure (plaintext) transport. The SDK's own default is `localhost`,
  which resolves to `::1` here and hangs; Alloy listens on 127.0.0.1 only (spec F8, F10). Test this default
  selection.
- *dbt spans.* `EmitDbtResults` creates one span per node, named `dbt.<resource_type>.<name>` (e.g.
  `dbt.model.dim_issues`, `dbt.test.unique_…`), as a child of the span in `ctx`.
  - Take start and end times from the node's `execute` timing, else `compile`, else fall back to
    `ExecutionTime`, via `trace.WithTimestamp`.
  - Attributes: `dbt.unique_id`, `dbt.status`, `dbt.selector`, `dbt.execution_time`, and `dbt.failures` when
    set.
  - Status is Error for error / fail / runtime error.
  - It increments `platform.dbt.nodes` with attributes `status` and `resource_type`.

**Tests.**
- `ParseRunResults` on both fixtures: counts, timings and `Failed()`.
- `Runner.Build` against `testdata/fake-dbt.sh`, set as `Bin`. The script records its argv to a file, copies a
  chosen fixture to `<WorkDir>/<ProjectDir>/target/run_results.json` (use `t.TempDir()` as `WorkDir`), and exits
  0 or 1 by env var. Also cover a run where the script writes no file while a stale one from before the start
  time exists: `Build` must error. Assert:
  - the exact argv;
  - that `warehouse/marts` was created under `WorkDir`;
  - the pass result;
  - the fail error lists the failed node names.
- Telemetry, using `sdktrace` with `tracetest.NewSpanRecorder()` and `sdkmetric.NewManualReader()`:
  - `RecordWrite` sums;
  - `RecordActivity` outcome attributes;
  - `EmitDbtResults` produces one span per node with the expected names, statuses and timestamps, plus the
    `platform.dbt.nodes` totals by status;
  - `ConfigFromEnv` defaults and overrides;
  - `Setup` with `stdout` writes span JSON to the given writer after shutdown;
  - `Setup` with `none` works;
  - `Setup` with an unknown exporter errors.

`doc.go` for each package includes a line starting `// Data-engineering concept: `:
- `telemetry`: pipeline observability (traces and metrics for data runs);
- `dbt`: an orchestration boundary around a SQL transformation tool, with run artefacts as data.

## Inputs

- `/Users/dustincheng/projects/data-platform/docs/data-platform-spec.md` (§10 Observability; §8)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/spec.md` (F8, AC-38,
  AC-39, AC-42)
- `/Users/dustincheng/projects/data-platform/docs/fable-streams/2026-09-18-phase-1-build/plan.md`
- `/Users/dustincheng/projects/data-platform/local/fixtures/run_results_pre_go.json`: the real artefact from a
  full pre_go build, snapshotted by the conductor. Copy it; do not regenerate it.

## Verification commands

```
cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/telemetry internal/dbt)" && echo GOFMT-OK
cd /Users/dustincheng/projects/data-platform && go vet ./internal/telemetry/... ./internal/dbt/...
cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/telemetry/... ./internal/dbt/... 2>&1 | tail -40
cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./internal/telemetry/... ./internal/dbt/... && ! go list -deps ./internal/telemetry/... ./internal/dbt/... | grep -qi duckdb && echo NO-CGO-NO-DUCKDB
cd /Users/dustincheng/projects/data-platform && grep -o '"dbt_schema_version": *"[^"]*"' internal/dbt/testdata/run_results_pass.json && grep -c '"unique_id"' internal/dbt/testdata/run_results_pass.json
cd /Users/dustincheng/projects/data-platform && grep -nE 'go.opentelemetry.io/otel' go.mod
cd /Users/dustincheng/projects/data-platform && grep -E '^(go|toolchain) ' go.mod
cd /Users/dustincheng/projects/data-platform && git log --oneline -3 && git status --short
```

Expected results:
- Every `go.opentelemetry.io/otel*` module listed as a direct requirement is at `v1.46.0`, except any the SDK
  versions separately (e.g. `otel/sdk/metric` may carry its own version line; record what you pinned).
- The go directive is `go 1.25` or `go 1.25.x` (x ≤ 5), with no `toolchain` line.
- The pass fixture has a real `dbt_schema_version` and more than 10 `unique_id` entries.

## Report obligation

Append `## implementer — round <N>` to `tasks/08-telemetry/report.md`. Include:
- the files changed;
- the OTel module versions you pinned;
- how the fixtures were produced;
- each verification command with its exit code and output tail (≤30 lines);
- the commit SHA.

## Tracker

Story **trk-bam.8**. Run `bd` from `/Users/dustincheng/projects/data-platform`.
- Before your first edit: `bd update trk-bam.8 --claim --actor claudeclaw`
- Once per round, after committing and before your final response:
  `bd comment trk-bam.8 --actor claudeclaw "<round N: what landed + commit SHA, or what stopped you>"`
- Never close the story, and never add or remove labels (never `agent`, never `human`).

## Out of scope

- Temporal interceptors and worker wiring (task 09).
- Any change to `internal/orchestrator`, `cmd/*` or `dbt/*`.
- Running real dbt.
- Exporting to anything except OTLP, stdout or none.
- Grafana dashboards.
