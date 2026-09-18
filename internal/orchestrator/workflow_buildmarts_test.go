package orchestrator_test

// Behavioral tests for internal/orchestrator's BuildMarts workflow -- AC-37 in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md: "Workflow BuildMarts(window) runs activity
// DbtBuild(tag:pre_go), then one activity per registered Go transform, then
// DbtBuild(tag:post_go). A failed first pass stops it before any transform runs. ... Check:
// testsuite tests for ordering and failure."
//
// These tests drive go.temporal.io/sdk/testsuite.TestWorkflowEnvironment with DbtBuild and
// RunTransform mocked out (the same pattern workflow_materialise_test.go already uses for
// FetchWindow/WriteWindow/DeleteSpool): the only thing a workflow test can observe is which
// activities BuildMarts starts, with what arguments, in what order, and how it reacts to a
// failure -- never how BuildMarts is internally structured. catalog.All() (task 07) is read
// directly rather than hard-coded, so this file stays correct if a transform is ever added or
// removed.
//
// REQUIRED IMPLEMENTATION HOOK (does not exist yet; read before writing workflow_buildmarts.go):
// this file references orchestrator.BuildMarts, orchestrator.BuildMartsInput,
// orchestrator.BuildMartsResult, orchestrator.DbtBuildInput, orchestrator.DbtBuildResult,
// orchestrator.RunTransformInput, orchestrator.ActivityDbtBuild and orchestrator.ActivityRunTransform,
// all pinned by the brief's "Required content", plus assumes the two activity methods have the
// signatures:
//
//	func (a *Activities) DbtBuild(ctx context.Context, in DbtBuildInput) (DbtBuildResult, error)
//	func (a *Activities) RunTransform(ctx context.Context, in RunTransformInput) (bronze.Result, error)
//
// (RunTransform's return type is not spelled out verbatim in the brief, but the brief says it
// "calls transform.Materialise(...)", whose own return type is (bronze.Result, error) -- see
// internal/transform/transform.go.) Until BuildMarts exists, this package fails to build; that is
// the expected Mode-A result.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/orchestrator"
	"github.com/DMokong/data-platform/internal/transform/catalog"
	"github.com/DMokong/data-platform/internal/window"
)

func dbtBuildStub(ctx context.Context, in orchestrator.DbtBuildInput) (orchestrator.DbtBuildResult, error) {
	return orchestrator.DbtBuildResult{}, nil
}

func runTransformStub(ctx context.Context, in orchestrator.RunTransformInput) (bronze.Result, error) {
	return bronze.Result{}, nil
}

// buildMartsWorkflowEnv builds a TestWorkflowEnvironment with DbtBuild and RunTransform stub-
// registered under their exported names, ready for a test to attach its own OnActivity behaviors
// (see stubActivity's doc comment in workflow_materialise_test.go for why the stub registration
// step is needed at all).
func buildMartsWorkflowEnv(t *testing.T) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	stubActivity(env, orchestrator.ActivityDbtBuild, dbtBuildStub)
	stubActivity(env, orchestrator.ActivityRunTransform, runTransformStub)
	return env
}

// buildMartsCallLog is a concurrency-safe ordered record of which mocked activity ran and with
// what argument, e.g. "dbtbuild:tag:pre_go" or "transform:issue_mentions" -- the only externally
// observable trace of BuildMarts's actual execution order a workflow test can see.
type buildMartsCallLog struct {
	entries []string
}

// wantBuildMartsOrder is pre_go, then every catalog.All() transform's Contract().Name in catalog
// order, then post_go -- the exact AC-37 sequence, derived from the real catalog so this test
// keeps working if a transform is ever added.
func wantBuildMartsOrder() []string {
	order := []string{"dbtbuild:tag:pre_go"}
	for _, tr := range catalog.All() {
		order = append(order, "transform:"+tr.Contract().Name)
	}
	return append(order, "dbtbuild:tag:post_go")
}

// AC-37: BuildMarts runs DbtBuild(tag:pre_go), then RunTransform for every catalog.All()
// transform in catalog order, then DbtBuild(tag:post_go) -- in exactly that order, no step
// skipped, none repeated, none out of order.
func TestBuildMarts_RunsPreGoThenTransformsThenPostGo_InOrder_AC37(t *testing.T) {
	env := buildMartsWorkflowEnv(t)

	var log buildMartsCallLog
	env.OnActivity(orchestrator.ActivityDbtBuild, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, in orchestrator.DbtBuildInput) (orchestrator.DbtBuildResult, error) {
			log.entries = append(log.entries, "dbtbuild:"+in.Selector)
			return orchestrator.DbtBuildResult{Selector: in.Selector, Nodes: 1}, nil
		})
	env.OnActivity(orchestrator.ActivityRunTransform, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, in orchestrator.RunTransformInput) (bronze.Result, error) {
			log.entries = append(log.entries, "transform:"+in.Name)
			return bronze.Result{Rows: 1, Bytes: 10}, nil
		})

	w := mustWindow(t, 2026, 9, 15, 0, window.Day)
	env.ExecuteWorkflow(orchestrator.BuildMarts, orchestrator.BuildMartsInput{Window: w})

	if !env.IsWorkflowCompleted() {
		t.Fatalf("BuildMarts did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("BuildMarts failed: %v", err)
	}

	want := wantBuildMartsOrder()
	got := log.entries
	if len(got) != len(want) {
		t.Fatalf("call order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q, want %q (full order got=%v want=%v)", i, got[i], want[i], got, want)
		}
	}
}

// AC-37: a failing pre_go DbtBuild stops BuildMarts before any transform runs, and post_go never
// runs either -- "A failed first pass stops it before any transform runs."
func TestBuildMarts_FailingPreGo_NoTransformNoPostGo_AC37(t *testing.T) {
	env := buildMartsWorkflowEnv(t)

	var log buildMartsCallLog
	preGoErr := temporal.NewNonRetryableApplicationError(
		"dbt build --select tag:pre_go failed for 1 node(s): model.data_platform.stg_beads__dependency_snapshots",
		"DbtBuildFailed", errors.New("dbt: exit status 1"))

	// The pre_go call itself fails, non-retryably.
	env.OnActivity(orchestrator.ActivityDbtBuild, mock.Anything, mock.MatchedBy(func(in orchestrator.DbtBuildInput) bool {
		return in.Selector == "tag:pre_go"
	})).Run(func(args mock.Arguments) {
		log.entries = append(log.entries, "dbtbuild:tag:pre_go")
	}).Return(orchestrator.DbtBuildResult{}, preGoErr)
	// Any other DbtBuild call (most importantly a post_go call) is logged rather than left
	// unmocked, so a bug that runs post_go anyway produces a clear assertion failure below instead
	// of an opaque mock panic that could be mistaken for a broken test harness.
	env.OnActivity(orchestrator.ActivityDbtBuild, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		in := args.Get(1).(orchestrator.DbtBuildInput)
		log.entries = append(log.entries, "dbtbuild:"+in.Selector)
	}).Return(orchestrator.DbtBuildResult{}, nil)
	env.OnActivity(orchestrator.ActivityRunTransform, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		in := args.Get(1).(orchestrator.RunTransformInput)
		log.entries = append(log.entries, "transform:"+in.Name)
	}).Return(bronze.Result{}, nil)

	w := mustWindow(t, 2026, 9, 15, 0, window.Day)
	env.ExecuteWorkflow(orchestrator.BuildMarts, orchestrator.BuildMartsInput{Window: w})

	if !env.IsWorkflowCompleted() {
		t.Fatalf("BuildMarts did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("BuildMarts succeeded, want it to fail when pre_go fails")
	}

	got := log.entries
	if len(got) != 1 || got[0] != "dbtbuild:tag:pre_go" {
		t.Errorf(`call log = %v, want exactly ["dbtbuild:tag:pre_go"] (no transform, no post_go after a failed pre_go)`, got)
	}
}

// AC-37: a failing RunTransform stops BuildMarts before post_go runs (pre_go still ran, exactly
// once).
func TestBuildMarts_FailingRunTransform_NoPostGo_AC37(t *testing.T) {
	transforms := catalog.All()
	if len(transforms) == 0 {
		t.Fatalf("catalog.All() is empty; this test needs at least one registered transform to fail")
	}
	firstName := transforms[0].Contract().Name

	env := buildMartsWorkflowEnv(t)

	var log buildMartsCallLog
	env.OnActivity(orchestrator.ActivityDbtBuild, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		in := args.Get(1).(orchestrator.DbtBuildInput)
		log.entries = append(log.entries, "dbtbuild:"+in.Selector)
	}).Return(orchestrator.DbtBuildResult{Selector: "tag:pre_go", Nodes: 1}, nil)
	env.OnActivity(orchestrator.ActivityRunTransform, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		in := args.Get(1).(orchestrator.RunTransformInput)
		log.entries = append(log.entries, "transform:"+in.Name)
	}).Return(bronze.Result{}, errors.New("transform: "+firstName+": run: boom"))

	w := mustWindow(t, 2026, 9, 15, 0, window.Day)
	env.ExecuteWorkflow(orchestrator.BuildMarts, orchestrator.BuildMartsInput{Window: w})

	if !env.IsWorkflowCompleted() {
		t.Fatalf("BuildMarts did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("BuildMarts succeeded, want it to fail when a transform fails")
	}

	preGoCount, postGoCount, unexpected := 0, 0, 0
	for _, c := range log.entries {
		switch {
		case c == "dbtbuild:tag:pre_go":
			preGoCount++
		case c == "dbtbuild:tag:post_go":
			postGoCount++
		case c == "transform:"+firstName:
			// expected -- RunTransform's own retry policy (5 attempts, per the brief) may call
			// this mock more than once before the workflow gives up; every attempt is for the
			// same, first, failing transform.
		default:
			unexpected++
		}
	}
	if preGoCount != 1 {
		t.Errorf("pre_go DbtBuild ran %d times, want exactly 1: %v", preGoCount, log.entries)
	}
	if postGoCount != 0 {
		t.Errorf("post_go DbtBuild ran %d times, want 0 (a failing transform must stop BuildMarts before post_go): %v", postGoCount, log.entries)
	}
	if unexpected != 0 {
		t.Errorf("call log contains %d unexpected entries: %v", unexpected, log.entries)
	}
}
