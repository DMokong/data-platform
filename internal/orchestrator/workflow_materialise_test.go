package orchestrator_test

// Behavioral tests for internal/orchestrator's workflows -- AC-33 and AC-34 in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md -- driven through
// go.temporal.io/sdk/testsuite.TestWorkflowEnvironment with every activity and child workflow
// mocked out. A workflow's only externally observable behavior is which activities/children it
// starts, with which arguments, and what it itself returns or errors; that is exactly what these
// tests assert, never anything about how MaterialiseWindow/MaterialiseSource are internally
// structured.
//
// Activity/child-workflow name convergence: Temporal's SDK derives an activity or workflow "type
// name" from whatever is passed to workflow.ExecuteActivity / workflow.ExecuteChildWorkflow --
// either a literal string, or (via reflection on the function's own name, stripping any bound-
// method "-fm" suffix) a function/method value. Since the brief pins the real methods'  names
// exactly to the Activity*/Workflow* name constants (func (a *Activities) FetchWindow(...),
// func MaterialiseWindow(...), etc.), both invocation styles resolve to the same name. These
// tests rely only on that name, mocking activities by the exported string constants and children
// by the exported MaterialiseWindow function value -- never on which invocation style the
// implementation happens to choose.
//
// All types/names referenced below (Activities, SpoolRef, FetchWindowInput,
// MaterialiseWindowInput, MaterialiseWindowResult, MaterialiseSourceInput,
// MaterialiseSourceResult, MaterialiseWindow, MaterialiseSource, TaskQueue and the Activity*/
// Workflow* constants) are pinned by the brief's "Required content" section; this file never
// references an unexported orchestrator symbol.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/orchestrator"
	"github.com/DMokong/data-platform/internal/window"
)

// stubActivity registers a placeholder function under name with the workflow test environment, so
// env.OnActivity(name, ...) has something to attach to: TestWorkflowEnvironment.OnActivity panics
// if the string name it is given was never registered. The placeholder is never actually invoked
// once a matching OnActivity mock is set up below; only its signature (for argument decoding)
// matters.
func stubActivity(env *testsuite.TestWorkflowEnvironment, name string, fn interface{}) {
	env.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name, DisableAlreadyRegisteredCheck: true})
}

func fetchWindowStub(ctx context.Context, in orchestrator.FetchWindowInput) (orchestrator.SpoolRef, error) {
	return orchestrator.SpoolRef{}, nil
}

func writeWindowStub(ctx context.Context, ref orchestrator.SpoolRef) (bronze.Result, error) {
	return bronze.Result{}, nil
}

func deleteSpoolStub(ctx context.Context, ref orchestrator.SpoolRef) error {
	return nil
}

// sameRef reports whether two SpoolRefs describe the same fetch: every field must match. Used to
// confirm WriteWindow receives exactly its own FetchWindow's SpoolRef, not e.g. an empty struct or
// another table's ref.
func sameRef(a, b orchestrator.SpoolRef) bool {
	return a.Path == b.Path && a.Source == b.Source && a.Table == b.Table &&
		a.Window == b.Window && a.Rows == b.Rows && a.FetchedAt.Equal(b.FetchedAt)
}

// materialiseWindowCase drives one MaterialiseWindow run and asserts (AC-33): each of
// FetchWindow/WriteWindow/DeleteSpool ran exactly len(wantTables) times; exactly wantTables was
// processed (no table skipped, no extra table, no duplicate); and every WriteWindow call received
// precisely the SpoolRef its own FetchWindow call returned. Tables may run in parallel or
// sequentially (the brief leaves that to the implementer), so this only checks the resulting sets
// and per-table pairing, never a call order.
func materialiseWindowCase(t *testing.T, w window.Window, wantTables []string) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	stubActivity(env, orchestrator.ActivityFetchWindow, fetchWindowStub)
	stubActivity(env, orchestrator.ActivityWriteWindow, writeWindowStub)
	stubActivity(env, orchestrator.ActivityDeleteSpool, deleteSpoolStub)

	var mu sync.Mutex
	fetched := map[string]orchestrator.SpoolRef{}
	written := map[string]orchestrator.SpoolRef{}
	deleted := map[string]orchestrator.SpoolRef{}

	env.OnActivity(orchestrator.ActivityFetchWindow, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, in orchestrator.FetchWindowInput) (orchestrator.SpoolRef, error) {
			ref := orchestrator.SpoolRef{
				Path:      "/spool/" + in.Source + "/" + in.Table + "/" + in.Window.String(),
				Source:    in.Source,
				Table:     in.Table,
				Window:    in.Window,
				Rows:      3,
				FetchedAt: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC),
			}
			mu.Lock()
			fetched[in.Table] = ref
			mu.Unlock()
			return ref, nil
		},
	)
	env.OnActivity(orchestrator.ActivityWriteWindow, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, ref orchestrator.SpoolRef) (bronze.Result, error) {
			mu.Lock()
			written[ref.Table] = ref
			mu.Unlock()
			return bronze.Result{Rows: ref.Rows}, nil
		},
	)
	env.OnActivity(orchestrator.ActivityDeleteSpool, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, ref orchestrator.SpoolRef) error {
			mu.Lock()
			deleted[ref.Table] = ref
			mu.Unlock()
			return nil
		},
	)

	env.ExecuteWorkflow(orchestrator.MaterialiseWindow, orchestrator.MaterialiseWindowInput{Source: "beads", Window: w})

	if !env.IsWorkflowCompleted() {
		t.Fatalf("MaterialiseWindow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("MaterialiseWindow failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	checkSet := func(label string, got map[string]orchestrator.SpoolRef) {
		if len(got) != len(wantTables) {
			t.Errorf("%s: processed %d tables, want exactly %d (%v)", label, len(got), len(wantTables), wantTables)
		}
		for _, tbl := range wantTables {
			if _, ok := got[tbl]; !ok {
				t.Errorf("%s: table %q was never processed", label, tbl)
			}
		}
		for tbl := range got {
			want := false
			for _, w := range wantTables {
				if w == tbl {
					want = true
					break
				}
			}
			if !want {
				t.Errorf("%s: unexpected table %q processed", label, tbl)
			}
		}
	}
	checkSet(orchestrator.ActivityFetchWindow, fetched)
	checkSet(orchestrator.ActivityWriteWindow, written)
	checkSet(orchestrator.ActivityDeleteSpool, deleted)

	for _, tbl := range wantTables {
		f, ok1 := fetched[tbl]
		w2, ok2 := written[tbl]
		if ok1 && ok2 && !sameRef(f, w2) {
			t.Errorf("table %q: WriteWindow's SpoolRef (%+v) != its own FetchWindow's SpoolRef (%+v)", tbl, w2, f)
		}
	}
}

// AC-33: MaterialiseWindow on an hourly window runs FetchWindow, WriteWindow and DeleteSpool for
// exactly beads' 3 hourly (event-log) tables -- events, dolt_history_issues, dolt_log -- not the
// 4 daily snapshot tables.
func TestMaterialiseWindow_HourlyWindow_ThreeTables_AC33(t *testing.T) {
	w := mustWindow(t, 2026, 9, 15, 13, window.Hour)
	materialiseWindowCase(t, w, []string{"events", "dolt_history_issues", "dolt_log"})
}

// AC-33: MaterialiseWindow on a daily window runs beads' 4 snapshot tables -- issues, comments,
// dependencies, labels -- not the 3 hourly ones.
func TestMaterialiseWindow_DailyWindow_FourTables_AC33(t *testing.T) {
	w := mustWindow(t, 2026, 9, 15, 0, window.Day)
	materialiseWindowCase(t, w, []string{"issues", "comments", "dependencies", "labels"})
}

// AC-34: MaterialiseSource, as of a fixed workflow.Now (env.SetStartTime), starts exactly one
// MaterialiseWindow child per lookback window: 24 hourly + 2 daily for beads, each with the
// deterministic workflow id "materialise/<source>/<grain>/<window>" -- no window missing, none
// duplicated, none extra.
func TestMaterialiseSource_StartsExpectedChildren_AC34(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	start := time.Date(2026, 9, 15, 0, 30, 0, 0, time.UTC)
	env.SetStartTime(start)

	hourly, err := window.Lookback(start, window.Hour, 24)
	if err != nil {
		t.Fatalf("window.Lookback(hour): %v", err)
	}
	daily, err := window.Lookback(start, window.Day, 2)
	if err != nil {
		t.Fatalf("window.Lookback(day): %v", err)
	}
	wantIDs := map[string]bool{}
	for _, w := range hourly {
		wantIDs["materialise/beads/hour/"+w.String()] = true
	}
	for _, w := range daily {
		wantIDs["materialise/beads/day/"+w.String()] = true
	}

	env.OnWorkflow(orchestrator.MaterialiseWindow, mock.Anything, mock.Anything).
		Return(orchestrator.MaterialiseWindowResult{}, nil)

	var mu sync.Mutex
	gotIDs := map[string]int{}
	env.SetOnChildWorkflowStartedListener(func(info *workflow.Info, ctx workflow.Context, args converter.EncodedValues) {
		mu.Lock()
		gotIDs[info.WorkflowExecution.ID]++
		mu.Unlock()
	})

	env.ExecuteWorkflow(orchestrator.MaterialiseSource, orchestrator.MaterialiseSourceInput{Source: "beads"})

	if !env.IsWorkflowCompleted() {
		t.Fatalf("MaterialiseSource did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("MaterialiseSource failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotIDs) != len(wantIDs) {
		t.Errorf("started %d distinct children, want %d", len(gotIDs), len(wantIDs))
	}
	for id := range wantIDs {
		if gotIDs[id] != 1 {
			t.Errorf("child %q started %d times, want exactly 1", id, gotIDs[id])
		}
	}
	for id, n := range gotIDs {
		if !wantIDs[id] {
			t.Errorf("unexpected child id %q (started %d times)", id, n)
		}
	}
}

// AC-34: if any MaterialiseWindow child fails, MaterialiseSource itself fails, and the returned
// error names the failed child.
func TestMaterialiseSource_FailsWhenChildFails_AC34(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	start := time.Date(2026, 9, 15, 0, 30, 0, 0, time.UTC)
	env.SetStartTime(start)
	failWindow := window.Containing(start, window.Hour)
	failID := "materialise/beads/hour/" + failWindow.String()

	env.OnWorkflow(orchestrator.MaterialiseWindow, mock.Anything, mock.MatchedBy(func(in orchestrator.MaterialiseWindowInput) bool {
		return in.Window == failWindow
	})).Return(orchestrator.MaterialiseWindowResult{}, errors.New("dolt: connection refused"))
	env.OnWorkflow(orchestrator.MaterialiseWindow, mock.Anything, mock.Anything).
		Return(orchestrator.MaterialiseWindowResult{}, nil)

	env.ExecuteWorkflow(orchestrator.MaterialiseSource, orchestrator.MaterialiseSourceInput{Source: "beads"})

	if !env.IsWorkflowCompleted() {
		t.Fatalf("MaterialiseSource did not complete")
	}
	err := env.GetWorkflowError()
	if err == nil {
		t.Fatalf("MaterialiseSource succeeded, want an error naming the failed child %q", failID)
	}
	if !strings.Contains(err.Error(), failID) {
		t.Errorf("error %q does not name the failed child %q", err.Error(), failID)
	}
}
