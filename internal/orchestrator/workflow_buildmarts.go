package orchestrator

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/transform/catalog"
	"github.com/DMokong/data-platform/internal/window"
)

// Workflow code in this file must stay deterministic, under the same rules as
// workflow_materialise.go: no wall clock, native goroutines, randomness or I/O. The transform
// catalog it reads is static data compiled into the binary.

// dbt selectors of the two passes around the Go transforms. Every model is tagged with exactly
// one of them (dbt/dbt_project.yml).
const (
	selectorPreGo  = "tag:pre_go"
	selectorPostGo = "tag:post_go"
)

// BuildMartsInput names the window the Go transforms run for: a Day window.
type BuildMartsInput struct {
	Window window.Window
}

// TransformResult is what one Go transform wrote to the derived zone.
type TransformResult struct {
	Name  string
	Path  string // bronze-relative
	Rows  int
	Bytes int64
}

// BuildMartsResult reports both dbt passes and every transform, in the order they ran.
type BuildMartsResult struct {
	Window     window.Window
	PreGo      DbtBuildResult
	Transforms []TransformResult
	PostGo     DbtBuildResult
}

var (
	// dbtBuildOptions: a full build of the phase-1 project takes seconds, but dbt starts slowly
	// and marts grow; 15 min bounds a hung build. A failed build is non-retryable, so the second
	// attempt only ever follows a timeout or a lost worker.
	dbtBuildOptions = workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    10 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    2,
		},
	}
	// transformOptions: a transform reads mart Parquet and writes one derived file; local work,
	// retried like the ingestion activities.
	transformOptions = workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         retryPolicy,
	}
)

// BuildMarts builds the warehouse in two passes around the Go transforms: DbtBuild(tag:pre_go),
// then RunTransform for every catalog transform in catalog order, one at a time, then
// DbtBuild(tag:post_go). dbt's own DAG orders the models inside each pass. A failed step fails
// the workflow, and nothing after it runs: a failed first pass leaves the transforms unrun, and a
// failed transform leaves the post_go marts as they were.
func BuildMarts(ctx workflow.Context, in BuildMartsInput) (BuildMartsResult, error) {
	res := BuildMartsResult{Window: in.Window}
	if err := in.Window.Validate(); err != nil {
		return res, temporal.NewNonRetryableApplicationError(err.Error(), errTypeBadInput, nil)
	}
	if in.Window.Grain != window.Day {
		return res, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("BuildMarts runs for a %s window, got a %s window", window.Day, in.Window.Grain), errTypeBadInput, nil)
	}

	dbtCtx := workflow.WithActivityOptions(ctx, dbtBuildOptions)
	if err := workflow.ExecuteActivity(dbtCtx, ActivityDbtBuild, DbtBuildInput{Selector: selectorPreGo}).Get(ctx, &res.PreGo); err != nil {
		return res, fmt.Errorf("build marts %s: %s %s: %w", in.Window, ActivityDbtBuild, selectorPreGo, err)
	}

	transformCtx := workflow.WithActivityOptions(ctx, transformOptions)
	for _, t := range catalog.All() {
		name := t.Contract().Name
		var out bronze.Result
		tin := RunTransformInput{Name: name, Window: in.Window}
		if err := workflow.ExecuteActivity(transformCtx, ActivityRunTransform, tin).Get(ctx, &out); err != nil {
			return res, fmt.Errorf("build marts %s: %s %s: %w", in.Window, ActivityRunTransform, name, err)
		}
		res.Transforms = append(res.Transforms, TransformResult{Name: name, Path: out.Path, Rows: out.Rows, Bytes: out.Bytes})
	}

	if err := workflow.ExecuteActivity(dbtCtx, ActivityDbtBuild, DbtBuildInput{Selector: selectorPostGo}).Get(ctx, &res.PostGo); err != nil {
		return res, fmt.Errorf("build marts %s: %s %s: %w", in.Window, ActivityDbtBuild, selectorPostGo, err)
	}
	return res, nil
}
