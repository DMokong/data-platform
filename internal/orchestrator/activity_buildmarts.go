package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/telemetry"
	"github.com/DMokong/data-platform/internal/transform"
	"github.com/DMokong/data-platform/internal/transform/catalog"
	"github.com/DMokong/data-platform/internal/window"
)

// errTypeDbtBuildFailed is the application error type of a failed dbt build. It is
// non-retryable: a model that does not compile or a data test that fails gives the same result
// on a second run over the same bronze.
const errTypeDbtBuildFailed = "DbtBuildFailed"

// DbtBuildInput selects the dbt nodes one DbtBuild runs: "tag:pre_go" or "tag:post_go".
type DbtBuildInput struct {
	Selector string
}

// DbtBuildResult summarises one dbt build from its run_results.json.
type DbtBuildResult struct {
	Selector     string
	InvocationID string         // dbt's invocation_id, to find the run in dbt's own logs
	Nodes        int            // nodes dbt ran: models, tests, seeds, ...
	Statuses     map[string]int // node count per dbt status, e.g. success: 12, pass: 44
	Failed       []string       // unique ids of the nodes whose status is error, fail or runtime error
}

// RunTransformInput names one catalog transform and the window to run it for.
type RunTransformInput struct {
	Name   string
	Window window.Window
}

// DbtBuild runs `dbt build --select <in.Selector>` through DbtRunner and turns its
// run_results.json into telemetry: one span per dbt node under this activity's span, plus the
// platform.dbt.nodes counter. The telemetry is emitted for a failed build too, so the failing
// node shows up as an error span next to the ones that passed.
//
// A failed build is a non-retryable application error of type DbtBuildFailed whose message
// names the failed nodes and whose details are their unique ids. Only an attempt cut short by
// its own context (timeout, worker shutdown) stays retryable.
func (a *Activities) DbtBuild(ctx context.Context, in DbtBuildInput) (_ DbtBuildResult, err error) {
	defer recordActivity(ctx, ActivityDbtBuild, time.Now(), &err)
	if in.Selector == "" {
		return DbtBuildResult{}, badInput("orchestrator: DbtBuild: empty selector")
	}
	if a.DbtRunner.WorkDir == "" {
		return DbtBuildResult{}, badSetup("orchestrator: Activities.DbtRunner.WorkDir is empty")
	}

	rr, buildErr := a.DbtRunner.Build(ctx, in.Selector)
	telemetry.EmitDbtResults(ctx, in.Selector, rr)

	res := DbtBuildResult{
		Selector:     in.Selector,
		InvocationID: rr.InvocationID,
		Nodes:        len(rr.Results),
		Statuses:     make(map[string]int),
	}
	for _, n := range rr.Results {
		res.Statuses[n.Status]++
	}
	for _, n := range rr.Failed() {
		res.Failed = append(res.Failed, n.UniqueID)
	}
	if buildErr == nil {
		return res, nil
	}
	if ctx.Err() != nil {
		return DbtBuildResult{}, fmt.Errorf("orchestrator: dbt build --select %s: %w", in.Selector, buildErr)
	}
	msg := fmt.Sprintf("dbt build --select %s failed", in.Selector)
	if len(res.Failed) > 0 {
		msg += fmt.Sprintf(" for %d node(s): %s", len(res.Failed), strings.Join(res.Failed, ", "))
	} else {
		msg += ": " + buildErr.Error()
	}
	return DbtBuildResult{}, temporal.NewNonRetryableApplicationError(msg, errTypeDbtBuildFailed, buildErr, res.Failed)
}

// RunTransform runs the catalog transform named in.Name for in.Window and writes its output to
// the derived zone through the shared Writer (transform.Materialise), stamped with the extraction
// clock. A successful write adds its rows and bytes to platform.rows_written /
// platform.bytes_written under (source go_transform, table <output>, zone derived).
//
// An unknown name or a window of the wrong grain is a non-retryable error: the workflow only
// asks for catalog transforms at their own grain.
func (a *Activities) RunTransform(ctx context.Context, in RunTransformInput) (_ bronze.Result, err error) {
	defer recordActivity(ctx, ActivityRunTransform, time.Now(), &err)
	t, ok := catalog.Lookup(in.Name)
	if !ok {
		return bronze.Result{}, badInput("orchestrator: unknown transform %q", in.Name)
	}
	contract := t.Contract()
	if err := in.Window.Validate(); err != nil {
		return bronze.Result{}, badInput("orchestrator: transform %s: %v", in.Name, err)
	}
	if in.Window.Grain != contract.Grain {
		return bronze.Result{}, badInput("orchestrator: transform %s runs in %s windows, got a %s window",
			in.Name, contract.Grain, in.Window.Grain)
	}
	if a.Writer == nil {
		return bronze.Result{}, badSetup("orchestrator: Activities.Writer is nil")
	}
	if a.TransformEnv.WarehouseRoot == "" {
		return bronze.Result{}, badSetup("orchestrator: Activities.TransformEnv.WarehouseRoot is empty")
	}

	res, err := transform.Materialise(ctx, t, a.TransformEnv, a.Writer, in.Window, a.now().UTC())
	if err != nil {
		return bronze.Result{}, fmt.Errorf("orchestrator: %w", err)
	}
	telemetry.RecordWrite(ctx, "go_transform", contract.Output, string(bronze.Derived), res.Rows, res.Bytes)
	return res, nil
}
