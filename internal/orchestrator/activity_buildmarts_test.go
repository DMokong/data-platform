package orchestrator_test

// Behavioral tests for internal/orchestrator's BuildMarts-supporting activities:
//
//   - AC-37 ("A dbt failure is a non-retryable error naming the failing nodes from
//     run_results.json") for DbtBuild, run against a real *dbt.Runner shelling out to
//     internal/dbt/testdata/fake-dbt.sh (task 09's brief: "a DbtBuild activity test with a fake
//     dbt.Runner binary (reuse internal/dbt/testdata/fake-dbt.sh) returns a non-retryable
//     application error on failure").
//   - AC-39 ("Each activity records histogram platform.activity.duration ... run_results.json
//     becomes one span per dbt node ... plus counter platform.dbt.nodes by status ...
//     WriteWindow adds counters platform.rows_written and platform.bytes_written") for the
//     wiring DbtBuild and RunTransform add on top of internal/telemetry's already-tested
//     EmitDbtResults/RecordActivity/RecordWrite (see internal/telemetry/*_test.go for those
//     functions' own unit tests): these tests prove the *activities* actually call them, by
//     installing a real (in-memory-exporter) OTel TracerProvider/MeterProvider as the process's
//     global providers -- exactly what a live worker installs -- and reading back what a real
//     DbtBuild/RunTransform call produced.
//
// Every test drives the real Activities methods through
// go.temporal.io/sdk/testsuite.TestActivityEnvironment against real files on disk (a real dbt.Runner
// pointed at the fake binary, and a real dim_issues Parquet fixture for RunTransform's
// catalog-looked-up mentions.Transform to read), never a mock standing in for the thing under
// test.
//
// REQUIRED IMPLEMENTATION HOOK (does not exist yet; read before writing activity_buildmarts.go):
// per the brief, "The Activities struct gains the dependencies these need: a dbt.Runner, the
// transform Env, and the Writer it already has." The brief does not pin these two new field names
// verbatim (unlike Sources/Fetchers/Writer/SpoolDir/Now, pinned by task 06); this file assumes:
//
//	type Activities struct {
//	    ...                       // Sources, Fetchers, Writer, SpoolDir, Now (task 06, unchanged)
//	    DbtRunner    dbt.Runner
//	    TransformEnv transform.Env
//	}
//	func (a *Activities) DbtBuild(ctx context.Context, in DbtBuildInput) (DbtBuildResult, error)
//	func (a *Activities) RunTransform(ctx context.Context, in RunTransformInput) (bronze.Result, error)
//
// If the implementer picks different field names, this file needs a mechanical rename to
// compile; the DbtBuild/RunTransform *behavior* asserted below does not depend on which field
// name carries which dependency. Until these exist, this package fails to build -- the expected
// Mode-A result.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/dbt"
	"github.com/DMokong/data-platform/internal/orchestrator"
	"github.com/DMokong/data-platform/internal/transform"
	"github.com/DMokong/data-platform/internal/window"
)

// --- OTel test scaffolding ------------------------------------------------------------------
//
// internal/telemetry's exported functions (RecordWrite, RecordActivity, EmitDbtResults) read the
// process-global TracerProvider/MeterProvider at call time (see internal/telemetry/helpers_test.go's
// own doc comment), so installing an in-memory provider as the global provider -- then restoring
// the previous one afterwards -- is how a caller of telemetry observes exactly what it emitted.

func newBuildMartsTracerProvider(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})
	return rec
}

func newBuildMartsMeterProvider(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		otel.SetMeterProvider(prev)
	})
	return reader
}

func collectBuildMartsMetric(t *testing.T, reader *sdkmetric.ManualReader, name string) (metricdata.Metrics, bool) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

// sumInt64Point returns the Value of the one int64 Sum data point of instrument name whose
// attributes match want exactly on the given keys, failing the test if the instrument is absent
// or no point matches.
func sumInt64Point(t *testing.T, reader *sdkmetric.ManualReader, name string, want map[string]string) int64 {
	t.Helper()
	m, ok := collectBuildMartsMetric(t, reader, name)
	if !ok {
		t.Fatalf("metric %q not found among collected instruments", name)
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %s data is %T, want metricdata.Sum[int64]", name, m.Data)
	}
	for _, p := range sum.DataPoints {
		if buildMartsAttrsMatch(p.Attributes, want) {
			return p.Value
		}
	}
	t.Fatalf("metric %q has no data point matching %v (points: %+v)", name, want, sum.DataPoints)
	return 0
}

// histogramCount returns the Count of the one Histogram[float64] data point of instrument name
// whose attributes match want exactly on the given keys, failing the test if the instrument is
// absent or no point matches.
func histogramCount(t *testing.T, reader *sdkmetric.ManualReader, name string, want map[string]string) uint64 {
	t.Helper()
	m, ok := collectBuildMartsMetric(t, reader, name)
	if !ok {
		t.Fatalf("metric %q not found among collected instruments", name)
	}
	hist, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("metric %s data is %T, want metricdata.Histogram[float64]", name, m.Data)
	}
	for _, p := range hist.DataPoints {
		if buildMartsAttrsMatch(p.Attributes, want) {
			return p.Count
		}
	}
	t.Fatalf("metric %q has no data point matching %v", name, want)
	return 0
}

func buildMartsAttrsMatch(set attribute.Set, want map[string]string) bool {
	for k, v := range want {
		got, ok := set.Value(attribute.Key(k))
		if !ok || got.AsString() != v {
			return false
		}
	}
	return true
}

// --- dbt.Runner fixture helpers (reused from internal/dbt/build_test.go's own technique) -----

func fakeDBTPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "dbt", "testdata", "fake-dbt.sh"))
	if err != nil {
		t.Fatalf("abs path to fake-dbt.sh: %v", err)
	}
	if info, err := os.Stat(abs); err != nil {
		t.Fatalf("fake-dbt.sh not found at %s: %v", abs, err)
	} else if info.Mode()&0o111 == 0 {
		t.Fatalf("fake-dbt.sh at %s is not executable", abs)
	}
	return abs
}

func dbtFixtureAbsPath(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "dbt", "testdata", name))
	if err != nil {
		t.Fatalf("abs path to %s: %v", name, err)
	}
	return abs
}

// newFakeDbtRunner points a dbt.Runner at fake-dbt.sh, configured (via env vars fake-dbt.sh
// reads) to copy fixture onto <WorkDir>/dbt/target/run_results.json and exit with exitCode.
func newFakeDbtRunner(t *testing.T, fixture string, exitCode string) dbt.Runner {
	t.Helper()
	work := t.TempDir()
	t.Setenv("FAKE_DBT_ARGV_FILE", filepath.Join(work, "argv.txt"))
	t.Setenv("FAKE_DBT_FIXTURE", dbtFixtureAbsPath(t, fixture))
	t.Setenv("FAKE_DBT_EXIT_CODE", exitCode)
	os.Unsetenv("FAKE_DBT_NO_WRITE")
	return dbt.Runner{Bin: fakeDBTPath(t), WorkDir: work, ProjectDir: "dbt", ProfilesDir: "dbt"}
}

// --- DbtBuild ----------------------------------------------------------------------------------

// AC-37 + AC-39: a passing DbtBuild parses run_results.json into DbtBuildResult, emits one span
// per node (dbt.<resource_type>.<name>, AC-39's span-naming rule) and a platform.dbt.nodes count,
// and records platform.activity.duration under activity=DbtBuild, outcome=success.
func TestDbtBuild_Success_EmitsResultSpansAndMetrics_AC37_AC39(t *testing.T) {
	runner := newFakeDbtRunner(t, "run_results_pass.json", "0")
	a := &orchestrator.Activities{DbtRunner: runner}

	tracerRec := newBuildMartsTracerProvider(t)
	meterReader := newBuildMartsMeterProvider(t)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(a.DbtBuild)

	val, err := env.ExecuteActivity(a.DbtBuild, orchestrator.DbtBuildInput{Selector: "tag:pre_go"})
	if err != nil {
		t.Fatalf("DbtBuild: %v", err)
	}
	var res orchestrator.DbtBuildResult
	if err := val.Get(&res); err != nil {
		t.Fatalf("decode DbtBuildResult: %v", err)
	}
	if res.Selector != "tag:pre_go" {
		t.Errorf("DbtBuildResult.Selector = %q, want %q", res.Selector, "tag:pre_go")
	}
	if res.Nodes != 56 {
		t.Errorf("DbtBuildResult.Nodes = %d, want 56 (run_results_pass.json's node count)", res.Nodes)
	}
	if len(res.Failed) != 0 {
		t.Errorf("DbtBuildResult.Failed = %v, want none on a passing build", res.Failed)
	}

	// AC-39: "run_results.json becomes one span per dbt node, named dbt.<resource_type>.<name>".
	ended := tracerRec.Ended()
	if len(ended) != 56 {
		t.Errorf("ended spans = %d, want 56 (one per run_results_pass.json node)", len(ended))
	}
	var sawModelSpan bool
	for _, s := range ended {
		if s.Name() == "dbt.model.dim_issues" {
			sawModelSpan = true
		}
	}
	if !sawModelSpan {
		names := make([]string, len(ended))
		for i, s := range ended {
			names[i] = s.Name()
		}
		t.Errorf("no span named %q among ended spans: %v", "dbt.model.dim_issues", names)
	}

	// AC-39: "plus counter platform.dbt.nodes by status".
	if got := sumInt64Point(t, meterReader, "platform.dbt.nodes", map[string]string{"status": "success", "resource_type": "model"}); got == 0 {
		t.Errorf("platform.dbt.nodes(status=success,resource_type=model) = 0, want > 0")
	}

	// AC-39: "Each activity records histogram platform.activity.duration (attributes activity,
	// outcome = success|failure)."
	if got := histogramCount(t, meterReader, "platform.activity.duration", map[string]string{"activity": "DbtBuild", "outcome": "success"}); got != 1 {
		t.Errorf("platform.activity.duration(DbtBuild,success) Count = %d, want 1", got)
	}
}

// AC-37: a failing dbt build (a node in run_results.json failed, and dbt exits non-zero) makes
// DbtBuild return a non-retryable application error whose message names the failing node --
// "A dbt failure is a non-retryable error naming the failing nodes from run_results.json." AC-39:
// telemetry is still emitted for the failed run (EmitDbtResults runs on the parsed results
// regardless of dbt's exit code), including RecordActivity's failure outcome.
func TestDbtBuild_Failure_NonRetryableNamesFailedNodesAndStillEmitsTelemetry_AC37_AC39(t *testing.T) {
	runner := newFakeDbtRunner(t, "run_results_fail.json", "1")
	a := &orchestrator.Activities{DbtRunner: runner}

	tracerRec := newBuildMartsTracerProvider(t)
	meterReader := newBuildMartsMeterProvider(t)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(a.DbtBuild)

	_, err := env.ExecuteActivity(a.DbtBuild, orchestrator.DbtBuildInput{Selector: "tag:post_go"})
	if err == nil {
		t.Fatalf("DbtBuild succeeded, want an error naming the failed dbt node(s)")
	}
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) {
		t.Fatalf("DbtBuild error = %v (%T), want a *temporal.ApplicationError", err, err)
	}
	if !appErr.NonRetryable() {
		t.Errorf("DbtBuild error is retryable, want non-retryable (a dbt failure cannot be fixed by retrying)")
	}
	if want := "DbtBuildFailed"; appErr.Type() != want {
		t.Errorf("DbtBuild error type = %q, want %q", appErr.Type(), want)
	}
	if !strings.Contains(err.Error(), "stg_beads__dependency_snapshots") {
		t.Errorf("error %q does not name the failing node stg_beads__dependency_snapshots", err.Error())
	}

	// AC-39: telemetry still runs on a failed build.
	if got := len(tracerRec.Ended()); got != 56 {
		t.Errorf("ended spans = %d, want 56 even on a failed build (EmitDbtResults runs on whatever run_results.json parsed to)", got)
	}
	if got := sumInt64Point(t, meterReader, "platform.dbt.nodes", map[string]string{"status": "error", "resource_type": "model"}); got != 1 {
		t.Errorf("platform.dbt.nodes(status=error,resource_type=model) = %d, want 1", got)
	}
	if got := sumInt64Point(t, meterReader, "platform.dbt.nodes", map[string]string{"status": "fail", "resource_type": "test"}); got != 1 {
		t.Errorf("platform.dbt.nodes(status=fail,resource_type=test) = %d, want 1", got)
	}
	if got := histogramCount(t, meterReader, "platform.activity.duration", map[string]string{"activity": "DbtBuild", "outcome": "failure"}); got != 1 {
		t.Errorf("platform.activity.duration(DbtBuild,failure) Count = %d, want 1", got)
	}
}

// --- RunTransform --------------------------------------------------------------------------

// buildMartsDimIssuesRow is the same narrow projection of dim_issues mentions.Transform.Run reads
// (internal/transform/mentions/mentions.go): issue_id required, title optional.
type buildMartsDimIssuesRow struct {
	IssueID string  `parquet:"issue_id"`
	Title   *string `parquet:"title,optional"`
}

func strp(s string) *string { return &s }

// writeDimIssuesFixture writes rows as dim_issues's one Parquet file under warehouseRoot, so a
// real catalog-looked-up mentions.Transform has something to read.
func writeDimIssuesFixture(t *testing.T, warehouseRoot string, rows []buildMartsDimIssuesRow) {
	t.Helper()
	dir := filepath.Join(warehouseRoot, "marts", "dim_issues")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	f, err := os.Create(filepath.Join(dir, "part-0.parquet"))
	if err != nil {
		t.Fatalf("create dim_issues fixture: %v", err)
	}
	defer f.Close()
	w := parquet.NewGenericWriter[buildMartsDimIssuesRow](f)
	if _, err := w.Write(rows); err != nil {
		t.Fatalf("write dim_issues fixture rows: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close dim_issues fixture writer: %v", err)
	}
}

// AC-37 + AC-39: RunTransform looks "issue_mentions" up in the real catalog, runs it against a
// real dim_issues fixture, writes the real derived-zone Parquet file through the real Writer, and
// records platform.rows_written/platform.bytes_written under source="go_transform",
// table=<the transform's Output>, zone="derived" -- the brief's literal example
// (`telemetry.RecordWrite(ctx, "go_transform", <output>, "derived", rows, bytes)`), plus
// platform.activity.duration for activity=RunTransform.
func TestRunTransform_WritesDerivedAndRecordsWriteMetrics_AC37_AC39(t *testing.T) {
	root := t.TempDir()
	warehouseRoot := filepath.Join(root, "warehouse")
	bronzeRoot := filepath.Join(root, "bronze")
	writeDimIssuesFixture(t, warehouseRoot, []buildMartsDimIssuesRow{
		{IssueID: "bam-1", Title: strp("see bam-2 for context")},
		{IssueID: "bam-2", Title: strp("no mentions here")},
	})

	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	a := &orchestrator.Activities{
		TransformEnv: transform.Env{WarehouseRoot: warehouseRoot},
		Writer:       &bronze.Writer{Root: bronzeRoot},
		Now:          func() time.Time { return now },
	}

	meterReader := newBuildMartsMeterProvider(t)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(a.RunTransform)

	w := window.Containing(now, window.Day)
	val, err := env.ExecuteActivity(a.RunTransform, orchestrator.RunTransformInput{Name: "issue_mentions", Window: w})
	if err != nil {
		t.Fatalf("RunTransform: %v", err)
	}
	var res bronze.Result
	if err := val.Get(&res); err != nil {
		t.Fatalf("decode bronze.Result: %v", err)
	}
	if res.Rows != 1 {
		t.Fatalf("bronze.Result.Rows = %d, want 1 (one mention: bam-1's title mentions bam-2)", res.Rows)
	}
	if res.Bytes <= 0 {
		t.Errorf("bronze.Result.Bytes = %d, want > 0", res.Bytes)
	}

	wantPath := filepath.Join(bronzeRoot, "derived", "issue_mentions", "dt=2026-09-18", "part-0.parquet")
	if got := filepath.Join(bronzeRoot, res.Path); got != wantPath {
		t.Errorf("bronze.Result.Path resolves to %s, want %s", got, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("derived output file missing at %s: %v", wantPath, err)
	}

	attrs := map[string]string{"source": "go_transform", "table": "issue_mentions", "zone": "derived"}
	if got := sumInt64Point(t, meterReader, "platform.rows_written", attrs); got != int64(res.Rows) {
		t.Errorf("platform.rows_written(go_transform,issue_mentions,derived) = %d, want %d", got, res.Rows)
	}
	if got := sumInt64Point(t, meterReader, "platform.bytes_written", attrs); got != res.Bytes {
		t.Errorf("platform.bytes_written(go_transform,issue_mentions,derived) = %d, want %d", got, res.Bytes)
	}
	if got := histogramCount(t, meterReader, "platform.activity.duration", map[string]string{"activity": "RunTransform", "outcome": "success"}); got != 1 {
		t.Errorf("platform.activity.duration(RunTransform,success) Count = %d, want 1", got)
	}
}

// AC-37: RunTransform on a name the catalog does not know rejects the request instead of running
// nothing silently; retrying it cannot help (the workflow never asks for a name outside
// catalog.All(), so this is a bad-input case, not a source outage).
func TestRunTransform_RejectsUnknownTransformName_AC37(t *testing.T) {
	a := &orchestrator.Activities{
		TransformEnv: transform.Env{WarehouseRoot: t.TempDir()},
		Writer:       &bronze.Writer{Root: t.TempDir()},
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(a.RunTransform)

	w := window.Containing(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), window.Day)
	_, err := env.ExecuteActivity(a.RunTransform, orchestrator.RunTransformInput{Name: "no_such_transform", Window: w})
	if err == nil {
		t.Fatalf("RunTransform(no_such_transform) succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "no_such_transform") {
		t.Errorf("error %q does not name the unknown transform", err.Error())
	}
}
