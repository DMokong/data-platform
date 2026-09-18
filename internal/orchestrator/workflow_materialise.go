package orchestrator

import (
	"fmt"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/source"
	"github.com/DMokong/data-platform/internal/window"
)

// Workflow code in this file must stay deterministic: Temporal replays it from the event history.
// Time comes only from workflow.Now, concurrency only from workflow.Go and futures, and all I/O
// happens in activities. The source declarations it reads are static data.

// MaterialiseWindowInput names the source and the window to materialise.
type MaterialiseWindowInput struct {
	Source string
	Window window.Window
}

// MaterialiseSourceInput names the source whose lookback windows to materialise.
type MaterialiseSourceInput struct {
	Source string
}

// TableResult is what one table's WriteWindow put in bronze for one window.
type TableResult struct {
	Table string
	Path  string // bronze-relative
	Rows  int
	Bytes int64
}

// MaterialiseWindowResult reports every table MaterialiseWindow wrote, in declaration order.
type MaterialiseWindowResult struct {
	Source string
	Window window.Window
	Tables []TableResult
}

// TableTotals sums one table over every window a MaterialiseSource run wrote.
type TableTotals struct {
	Table   string
	Windows int
	Rows    int
	Bytes   int64
}

// MaterialiseSourceResult totals a MaterialiseSource run: windows materialised, then rows and
// bytes per table in declaration order and overall, then what the BuildMarts child built.
type MaterialiseSourceResult struct {
	Source     string
	Windows    int
	Rows       int
	Bytes      int64
	Tables     []TableTotals
	BuildMarts BuildMartsResult
}

// retryPolicy is shared by every ingestion activity: first retry after 2 s, doubling, at most 5
// attempts. Five attempts ride out a Dolt restart or a brief network blip (2+4+8+16 = 30 s of
// backoff) without hammering a source that is down; the next scheduled run tries again anyway.
var retryPolicy = &temporal.RetryPolicy{
	InitialInterval:    2 * time.Second,
	BackoffCoefficient: 2.0,
	MaximumAttempts:    5,
}

// Activity options. StartToClose bounds one attempt; the heartbeat timeout detects a worker that
// died mid-attempt within a minute instead of waiting out StartToClose.
var (
	// fetchOptions: a fetch of the largest hourly window (~6 MB) takes seconds; 5 min leaves room
	// for a slow Dolt query without letting a hung connection hold a slot for long.
	fetchOptions = workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy:         retryPolicy,
	}
	// writeOptions: decoding a spool and encoding one Parquet file is local CPU and disk work.
	writeOptions = workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy:         retryPolicy,
	}
	// deleteOptions: one unlink.
	deleteOptions = workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         retryPolicy,
	}
)

// MaterialiseWindow materialises one window of one source: for every table whose grain matches
// the window, in declaration order, it runs FetchWindow, then WriteWindow on the returned
// SpoolRef, then DeleteSpool. Tables run in parallel. If a table fails, the others still finish,
// and the workflow fails with an error naming every failed table.
func MaterialiseWindow(ctx workflow.Context, in MaterialiseWindowInput) (MaterialiseWindowResult, error) {
	res := MaterialiseWindowResult{Source: in.Source, Window: in.Window}
	src, ok := Declarations()[in.Source]
	if !ok {
		return res, temporal.NewNonRetryableApplicationError(fmt.Sprintf("unknown source %q", in.Source), errTypeBadInput, nil)
	}
	if err := in.Window.Validate(); err != nil {
		return res, temporal.NewNonRetryableApplicationError(err.Error(), errTypeBadInput, nil)
	}
	var tables []source.Table
	for _, t := range src.Tables {
		if t.Grain == in.Window.Grain {
			tables = append(tables, t)
		}
	}
	if len(tables) == 0 {
		return res, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("source %q declares no %s tables", in.Source, in.Window.Grain), errTypeBadInput, nil)
	}

	results := make([]TableResult, len(tables))
	errs := make([]error, len(tables))
	wg := workflow.NewWaitGroup(ctx)
	for i, t := range tables {
		wg.Add(1)
		workflow.Go(ctx, func(gctx workflow.Context) {
			defer wg.Done()
			results[i], errs[i] = materialiseTable(gctx, src.Name, t.Name, in.Window)
		})
	}
	wg.Wait(ctx)

	var failed []string
	for i, t := range tables {
		if errs[i] != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", t.Name, errs[i]))
			continue
		}
		res.Tables = append(res.Tables, results[i])
	}
	if len(failed) > 0 {
		return res, fmt.Errorf("materialise %s %s: %d of %d tables failed: %s",
			src.Name, in.Window, len(failed), len(tables), strings.Join(failed, "; "))
	}
	return res, nil
}

// materialiseTable runs FetchWindow → WriteWindow → DeleteSpool for one table. The spool is
// deleted only after WriteWindow succeeds, so every WriteWindow retry finds it. If WriteWindow
// fails for good, the spool is deleted anyway: no later activity can redeem it.
func materialiseTable(ctx workflow.Context, sourceName, table string, w window.Window) (TableResult, error) {
	var ref SpoolRef
	fetchCtx := workflow.WithActivityOptions(ctx, fetchOptions)
	in := FetchWindowInput{Source: sourceName, Table: table, Window: w}
	if err := workflow.ExecuteActivity(fetchCtx, ActivityFetchWindow, in).Get(ctx, &ref); err != nil {
		return TableResult{}, fmt.Errorf("%s: %w", ActivityFetchWindow, err)
	}

	var out bronze.Result
	writeCtx := workflow.WithActivityOptions(ctx, writeOptions)
	writeErr := workflow.ExecuteActivity(writeCtx, ActivityWriteWindow, ref).Get(ctx, &out)

	deleteCtx := workflow.WithActivityOptions(ctx, deleteOptions)
	deleteErr := workflow.ExecuteActivity(deleteCtx, ActivityDeleteSpool, ref).Get(ctx, nil)

	if writeErr != nil {
		return TableResult{}, fmt.Errorf("%s: %w", ActivityWriteWindow, writeErr)
	}
	if deleteErr != nil {
		return TableResult{}, fmt.Errorf("%s: %w", ActivityDeleteSpool, deleteErr)
	}
	return TableResult{Table: table, Path: out.Path, Rows: out.Rows, Bytes: out.Bytes}, nil
}

// grainLookback is one grain of a source and the number of trailing windows to re-extract.
type grainLookback struct {
	grain    window.Grain
	lookback int
}

// grainLookbacks returns each grain of src once, in the order its first table appears in
// src.Tables. The beads tables of one grain share a lookback; if tables of one grain ever
// disagree, the largest lookback wins, so no table re-extracts fewer windows than it declares.
func grainLookbacks(src source.Source) []grainLookback {
	var out []grainLookback
	for _, t := range src.Tables {
		found := false
		for i := range out {
			if out[i].grain == t.Grain {
				out[i].lookback = max(out[i].lookback, t.Lookback)
				found = true
				break
			}
		}
		if !found {
			out = append(out, grainLookback{grain: t.Grain, lookback: t.Lookback})
		}
	}
	return out
}

// childWorkflowID is the deterministic id of the MaterialiseWindow child for one window, e.g.
// "materialise/beads/hour/2026-09-15T13".
func childWorkflowID(sourceName string, w window.Window) string {
	return fmt.Sprintf("materialise/%s/%s/%s", sourceName, w.Grain, w)
}

// buildMartsWorkflowID is the deterministic id of the BuildMarts child a source's run starts for
// one day, e.g. "build-marts/beads/2026-09-15".
func buildMartsWorkflowID(sourceName string, day window.Window) string {
	return fmt.Sprintf("build-marts/%s/%s", sourceName, day)
}

// MaterialiseSource re-extracts a source's lookback: for each grain, taken in declaration order,
// it computes window.Lookback(workflow.Now, grain, lookback) and starts one MaterialiseWindow
// child per window, all at once, with workflow id materialise/<source>/<grain>/<window> and reuse
// policy allow-duplicate (the next run reuses the id). It waits for every child, then fails with
// an error naming each failed child. Only when every child succeeded does it start child
// BuildMarts for the day containing that same workflow.Now instant, with workflow id
// build-marts/<source>/<day> and reuse policy allow-duplicate, and wait for it: a failed BuildMarts
// fails the run, and its result is part of the totals returned.
func MaterialiseSource(ctx workflow.Context, in MaterialiseSourceInput) (MaterialiseSourceResult, error) {
	res := MaterialiseSourceResult{Source: in.Source}
	src, ok := Declarations()[in.Source]
	if !ok {
		return res, temporal.NewNonRetryableApplicationError(fmt.Sprintf("unknown source %q", in.Source), errTypeBadInput, nil)
	}
	now := workflow.Now(ctx)

	type child struct {
		id     string
		future workflow.ChildWorkflowFuture
	}
	var children []child
	for _, gl := range grainLookbacks(src) {
		windows, err := window.Lookback(now, gl.grain, gl.lookback)
		if err != nil {
			return res, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("source %q, grain %s: %v", src.Name, gl.grain, err), errTypeBadInput, nil)
		}
		for _, w := range windows {
			id := childWorkflowID(src.Name, w)
			cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
				WorkflowID:            id,
				WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
			})
			f := workflow.ExecuteChildWorkflow(cctx, WorkflowMaterialiseWindow, MaterialiseWindowInput{Source: src.Name, Window: w})
			children = append(children, child{id: id, future: f})
		}
	}

	res.Tables = make([]TableTotals, len(src.Tables))
	index := make(map[string]int, len(src.Tables)) // lookup only; never ranged over
	for i, t := range src.Tables {
		res.Tables[i] = TableTotals{Table: t.Name}
		index[t.Name] = i
	}
	var failed []string
	for _, c := range children {
		var wr MaterialiseWindowResult
		if err := c.future.Get(ctx, &wr); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", c.id, err))
			continue
		}
		res.Windows++
		for _, tr := range wr.Tables {
			i, ok := index[tr.Table]
			if !ok {
				continue
			}
			res.Tables[i].Windows++
			res.Tables[i].Rows += tr.Rows
			res.Tables[i].Bytes += tr.Bytes
			res.Rows += tr.Rows
			res.Bytes += tr.Bytes
		}
	}
	if len(failed) > 0 {
		return res, fmt.Errorf("materialise %s: %d of %d windows failed: %s",
			src.Name, len(failed), len(children), strings.Join(failed, "; "))
	}

	// The day is taken from the same instant as the lookback windows, so the marts a run builds
	// belong to the day it started in, even if ingestion finishes after midnight UTC.
	day := window.Containing(now, window.Day)
	id := buildMartsWorkflowID(src.Name, day)
	bctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID:            id,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
	})
	if err := workflow.ExecuteChildWorkflow(bctx, WorkflowBuildMarts, BuildMartsInput{Window: day}).Get(ctx, &res.BuildMarts); err != nil {
		return res, fmt.Errorf("materialise %s: %s %s failed: %w", src.Name, WorkflowBuildMarts, id, err)
	}
	return res, nil
}
