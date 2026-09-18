package orchestrator_test

// Tests of the wiring between workflows and activities: Register's explicit names, a full
// MaterialiseWindow run over the real activities (claim-check end to end), failure handling, and
// the deterministic order and totals of MaterialiseSource (AC-33, AC-34).

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/orchestrator"
	"github.com/DMokong/data-platform/internal/window"
)

// recordingRegistry is a worker.Registry that records the names workflows and activities are
// registered under. The embedded Registry is nil, so any registration method other than the two
// *WithOptions ones panics: Register must name everything explicitly.
type recordingRegistry struct {
	worker.Registry
	workflows  []string
	activities []string
}

func (r *recordingRegistry) RegisterWorkflowWithOptions(w interface{}, o workflow.RegisterOptions) {
	r.workflows = append(r.workflows, o.Name)
}

func (r *recordingRegistry) RegisterActivityWithOptions(a interface{}, o activity.RegisterOptions) {
	r.activities = append(r.activities, o.Name)
}

// AC-37 added BuildMarts, DbtBuild and RunTransform to what task 06 already registered; this test
// checks each expected name is registered exactly once, under exactly that name, with nothing
// extra and nothing missing -- registration *order* is not part of any AC (Temporal's registry
// does not care what order names are added in), so the comparison sorts both sides instead of
// pinning task 09's particular append order inside Register's body.
func TestRegister_UsesExplicitNames(t *testing.T) {
	var r recordingRegistry
	orchestrator.Register(&r, &orchestrator.Activities{})
	wantWF := []string{"BuildMarts", "MaterialiseSource", "MaterialiseWindow"}
	wantAct := []string{"DbtBuild", "DeleteSpool", "FetchWindow", "RunTransform", "WriteWindow"}
	gotWF := append([]string(nil), r.workflows...)
	gotAct := append([]string(nil), r.activities...)
	sort.Strings(gotWF)
	sort.Strings(gotAct)
	if !reflect.DeepEqual(gotWF, wantWF) {
		t.Errorf("workflows registered as %v (sorted %v), want %v", r.workflows, gotWF, wantWF)
	}
	if !reflect.DeepEqual(gotAct, wantAct) {
		t.Errorf("activities registered as %v (sorted %v), want %v", r.activities, gotAct, wantAct)
	}
	if orchestrator.TaskQueue != "data-platform" {
		t.Errorf("TaskQueue = %q, want %q", orchestrator.TaskQueue, "data-platform")
	}
}

// listFiles returns every regular file under dir (none if dir does not exist).
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

// AC-33 end to end: MaterialiseWindow, driven by name over the activities Register wires up,
// writes one bronze file per hourly table with _extracted_at = FetchedAt, reports them in
// declaration order, and leaves no spool file behind.
func TestMaterialiseWindow_RealActivities_ClaimCheckEndToEnd_AC33(t *testing.T) {
	batch := sampleBatch()
	a, fetcher, bronzeRoot, spoolDir := newTestActivities(t, batch, nil, fixedNow)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	orchestrator.Register(env, a)

	w := mustWindow(t, 2026, 9, 15, 13, window.Hour)
	env.ExecuteWorkflow(orchestrator.WorkflowMaterialiseWindow, orchestrator.MaterialiseWindowInput{Source: "beads", Window: w})
	if !env.IsWorkflowCompleted() {
		t.Fatalf("MaterialiseWindow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("MaterialiseWindow failed: %v", err)
	}
	var res orchestrator.MaterialiseWindowResult
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatalf("decode result: %v", err)
	}

	wantTables := []string{"events", "dolt_history_issues", "dolt_log"}
	if len(res.Tables) != len(wantTables) {
		t.Fatalf("result has %d tables, want %d: %+v", len(res.Tables), len(wantTables), res.Tables)
	}
	for i, tbl := range wantTables {
		tr := res.Tables[i]
		wantPath := "raw/beads/" + tbl + "/dt=2026-09-15/hh=13.parquet"
		if tr.Table != tbl || tr.Path != wantPath || tr.Rows != len(batch.Rows) || tr.Bytes <= 0 {
			t.Errorf("result table %d = %+v, want %s at %s with %d rows and >0 bytes", i, tr, tbl, wantPath, len(batch.Rows))
			continue
		}
		rows := readParquetRows(t, filepath.Join(bronzeRoot, tr.Path))
		if len(rows) != len(batch.Rows) {
			t.Errorf("%s: bronze file has %d rows, want %d", tbl, len(rows), len(batch.Rows))
		}
		for _, row := range rows {
			if got := timestampMicros(row[bronze.ColExtractedAt]); !got.Equal(fixedNow) {
				t.Errorf("%s: _extracted_at = %v, want %v", tbl, got, fixedNow)
			}
		}
	}
	if got := fetcher.callCount(); got != len(wantTables) {
		t.Errorf("Fetcher called %d times, want %d", got, len(wantTables))
	}
	if left := listFiles(t, spoolDir); len(left) != 0 {
		t.Errorf("spool files left behind: %v", left)
	}
}

// AC-33: a table whose WriteWindow keeps failing fails MaterialiseWindow with an error naming the
// table; the other tables still complete, and every spool (the failed one too) is deleted.
func TestMaterialiseWindow_WriteFailureNamesTableAndCleansSpool_AC33(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	stubActivity(env, orchestrator.ActivityFetchWindow, fetchWindowStub)
	stubActivity(env, orchestrator.ActivityWriteWindow, writeWindowStub)
	stubActivity(env, orchestrator.ActivityDeleteSpool, deleteSpoolStub)

	var mu sync.Mutex
	writes := map[string]int{}
	deleted := map[string]bool{}
	env.OnActivity(orchestrator.ActivityFetchWindow, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, in orchestrator.FetchWindowInput) (orchestrator.SpoolRef, error) {
			return orchestrator.SpoolRef{Path: "/spool/" + in.Table, Source: in.Source, Table: in.Table, Window: in.Window}, nil
		})
	env.OnActivity(orchestrator.ActivityWriteWindow, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, ref orchestrator.SpoolRef) (bronze.Result, error) {
			mu.Lock()
			writes[ref.Table]++
			mu.Unlock()
			if ref.Table == "dolt_log" {
				return bronze.Result{}, errors.New("disk full")
			}
			return bronze.Result{Path: "raw/beads/" + ref.Table, Rows: 1}, nil
		})
	env.OnActivity(orchestrator.ActivityDeleteSpool, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, ref orchestrator.SpoolRef) error {
			mu.Lock()
			deleted[ref.Table] = true
			mu.Unlock()
			return nil
		})

	w := mustWindow(t, 2026, 9, 15, 13, window.Hour)
	env.ExecuteWorkflow(orchestrator.MaterialiseWindow, orchestrator.MaterialiseWindowInput{Source: "beads", Window: w})
	if !env.IsWorkflowCompleted() {
		t.Fatalf("MaterialiseWindow did not complete")
	}
	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(err.Error(), "dolt_log") {
		t.Fatalf("error = %v, want one naming table dolt_log", err)
	}
	for _, other := range []string{"events", "dolt_history_issues"} {
		if strings.Contains(err.Error(), other+":") {
			t.Errorf("error %q names %s, which succeeded", err, other)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if writes["dolt_log"] != 5 {
		t.Errorf("failing WriteWindow ran %d times, want 5 (the retry policy's maximum attempts)", writes["dolt_log"])
	}
	for _, tbl := range []string{"events", "dolt_history_issues", "dolt_log"} {
		if !deleted[tbl] {
			t.Errorf("spool of %s was not deleted", tbl)
		}
	}
}

// Activities reject requests retrying cannot fix with a non-retryable error, before touching the
// source.
func TestFetchWindow_RejectsUndeclaredRequestsNonRetryably(t *testing.T) {
	a, fetcher, _, _ := newTestActivities(t, sampleBatch(), nil, fixedNow)
	env := newTestActivityEnv(t, a)
	hour := mustWindow(t, 2026, 9, 15, 13, window.Hour)
	day := mustWindow(t, 2026, 9, 15, 0, window.Day)
	for name, in := range map[string]orchestrator.FetchWindowInput{
		"unknown source":   {Source: "nope", Table: "events", Window: hour},
		"unknown table":    {Source: "beads", Table: "nope", Window: hour},
		"wrong grain":      {Source: "beads", Table: "events", Window: day},
		"malformed window": {Source: "beads", Table: "events", Window: window.Window{Start: hour.Start, End: hour.Start, Grain: window.Hour}},
	} {
		_, err := env.ExecuteActivity(a.FetchWindow, in)
		var appErr *temporal.ApplicationError
		if err == nil || !errors.As(err, &appErr) || !appErr.NonRetryable() {
			t.Errorf("%s: FetchWindow error = %v, want a non-retryable application error", name, err)
		}
	}
	if n := fetcher.callCount(); n != 0 {
		t.Errorf("Fetcher called %d times for rejected requests, want 0", n)
	}
}

// A failed fetch surfaces the cause and writes no spool file.
func TestFetchWindow_FetchErrorLeavesNoSpool(t *testing.T) {
	a, _, _, spoolDir := newTestActivities(t, sampleBatch(), errors.New("dolt: connection refused"), fixedNow)
	env := newTestActivityEnv(t, a)
	in := orchestrator.FetchWindowInput{Source: "beads", Table: "events", Window: mustWindow(t, 2026, 9, 15, 13, window.Hour)}
	_, err := env.ExecuteActivity(a.FetchWindow, in)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("FetchWindow error = %v, want the fetch error", err)
	}
	if left := listFiles(t, spoolDir); len(left) != 0 {
		t.Errorf("spool files after a failed fetch: %v", left)
	}
}

// DeleteSpool and WriteWindow refuse a ref pointing outside SpoolDir.
func TestSpoolActivities_RefuseForeignPaths(t *testing.T) {
	a, _, _, _ := newTestActivities(t, sampleBatch(), nil, fixedNow)
	env := newTestActivityEnv(t, a)
	outside := filepath.Join(t.TempDir(), "keep.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := orchestrator.SpoolRef{Path: outside, Source: "beads", Table: "events", Window: mustWindow(t, 2026, 9, 15, 13, window.Hour)}
	if _, err := env.ExecuteActivity(a.DeleteSpool, ref); err == nil {
		t.Errorf("DeleteSpool accepted a path outside SpoolDir")
	}
	if _, err := env.ExecuteActivity(a.WriteWindow, ref); err == nil {
		t.Errorf("WriteWindow accepted a path outside SpoolDir")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("file outside SpoolDir was touched: %v", err)
	}
}

// MaterialiseSource sums its children's per-table rows and bytes, in declaration order.
func TestMaterialiseSource_ReturnsPerTableTotals(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetStartTime(time.Date(2026, 9, 15, 0, 30, 0, 0, time.UTC))
	env.OnWorkflow(orchestrator.MaterialiseWindow, mock.Anything, mock.Anything).Return(
		func(ctx workflow.Context, in orchestrator.MaterialiseWindowInput) (orchestrator.MaterialiseWindowResult, error) {
			res := orchestrator.MaterialiseWindowResult{Source: in.Source, Window: in.Window}
			if in.Window.Grain == window.Hour {
				res.Tables = []orchestrator.TableResult{{Table: "events", Rows: 2, Bytes: 100}, {Table: "dolt_log", Rows: 1, Bytes: 10}}
			} else {
				res.Tables = []orchestrator.TableResult{{Table: "issues", Rows: 5, Bytes: 1000}}
			}
			return res, nil
		})
	// AC-37: every ingestion child succeeds here, so MaterialiseSource also starts a BuildMarts
	// child; this test's own concern is the per-table totals below, so BuildMarts is mocked to a
	// bare success rather than asserted on (see workflow_materialise_test.go's dedicated AC-37
	// tests for BuildMarts's id/input/ordering).
	env.OnWorkflow(orchestrator.BuildMarts, mock.Anything, mock.Anything).
		Return(orchestrator.BuildMartsResult{}, nil)
	env.ExecuteWorkflow(orchestrator.MaterialiseSource, orchestrator.MaterialiseSourceInput{Source: "beads"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("MaterialiseSource failed: %v", err)
	}
	var res orchestrator.MaterialiseSourceResult
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if res.Windows != 26 || res.Rows != 24*3+2*5 || res.Bytes != 24*110+2*1000 {
		t.Errorf("totals = %d windows / %d rows / %d bytes, want 26 / %d / %d", res.Windows, res.Rows, res.Bytes, 24*3+2*5, 24*110+2*1000)
	}
	want := []orchestrator.TableTotals{
		{Table: "events", Windows: 24, Rows: 48, Bytes: 2400},
		{Table: "dolt_history_issues"},
		{Table: "dolt_log", Windows: 24, Rows: 24, Bytes: 240},
		{Table: "issues", Windows: 2, Rows: 10, Bytes: 2000},
		{Table: "comments"},
		{Table: "dependencies"},
		{Table: "labels"},
	}
	if !reflect.DeepEqual(res.Tables, want) {
		t.Errorf("per-table totals = %+v, want %+v", res.Tables, want)
	}
}

// An unknown source fails both workflows instead of silently doing nothing.
func TestWorkflows_RejectUnknownSource(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(orchestrator.MaterialiseSource, orchestrator.MaterialiseSourceInput{Source: "nope"})
	if err := env.GetWorkflowError(); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("MaterialiseSource(nope) error = %v, want one naming the source", err)
	}
	env = suite.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(orchestrator.MaterialiseWindow, orchestrator.MaterialiseWindowInput{Source: "nope", Window: mustWindow(t, 2026, 9, 15, 13, window.Hour)})
	if err := env.GetWorkflowError(); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("MaterialiseWindow(nope) error = %v, want one naming the source", err)
	}
}
