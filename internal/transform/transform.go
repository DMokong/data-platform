package transform

import (
	"context"
	"fmt"
	"time"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/window"
)

// Contract is declared by every Go transform: the mart(s) it reads (Inputs), the derived dataset
// it writes (Output), the window Grain it runs at, and the dbt tests that guard the output
// downstream (e.g. a source_freshness check and a post_go mart that flags what the transform
// missed). A transform's Contract is the only thing dbt and the orchestrator need to know about
// it without running it.
type Contract struct {
	Name   string       // e.g. "issue_mentions"
	Inputs []string     // table names read, e.g. ["dim_issues"]
	Output string       // derived dataset/table name, e.g. "issue_mentions"
	Grain  window.Grain // the window Run is called with
	Tests  []string     // dbt tests guarding the output, e.g. ["source_freshness:derived.issue_mentions", "mart_issue_mention_drift"]
}

// Env is the read side of a transform's world: the warehouse root a transform reads mart Parquet
// from. It carries no bronze root, because a transform never picks its own output path --
// Materialise does that through the shared Writer.
type Env struct {
	WarehouseRoot string // "<repo>/warehouse"
}

// Transform is one Go transform: it declares its Contract and, given an Env and a window, reads
// whatever mart Parquet it needs and returns the batch to write for that window.
type Transform interface {
	Contract() Contract
	Run(ctx context.Context, env Env, w window.Window) (record.Batch, error)
}

// Materialise runs t for window w and writes its output to the derived zone through the shared
// Writer: Writer.Write(ctx, bronze.Derived, t.Contract().Output, w, batch, extractedAt). If Run
// fails, Materialise returns that error and never calls the Writer, so a failed run never leaves
// a partial or stale-looking derived file for a downstream dbt build to pick up.
func Materialise(ctx context.Context, t Transform, env Env, wr *bronze.Writer, w window.Window, extractedAt time.Time) (bronze.Result, error) {
	contract := t.Contract()
	batch, err := t.Run(ctx, env, w)
	if err != nil {
		return bronze.Result{}, fmt.Errorf("transform: %s: run: %w", contract.Name, err)
	}
	res, err := wr.Write(ctx, bronze.Derived, contract.Output, w, batch, extractedAt)
	if err != nil {
		return bronze.Result{}, fmt.Errorf("transform: %s: write: %w", contract.Name, err)
	}
	return res, nil
}
