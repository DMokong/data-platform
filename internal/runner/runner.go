package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/source"
	"github.com/DMokong/data-platform/internal/window"
)

// Defaults applied when the corresponding Runner field is left at its zero value.
const (
	defaultAttempts    = 3
	defaultConcurrency = 4
)

// Runner wires one Source's Fetcher to a bronze Writer over windows. It has no loop and no
// ticker: RunOnce and Backfill each make one pass over Source.Tables and return.
type Runner struct {
	Source      source.Source
	Fetcher     source.Fetcher
	Writer      *bronze.Writer
	Now         func() time.Time // extraction clock for _extracted_at; nil -> time.Now
	Attempts    int              // tries per window; 0 -> 3
	Backoff     time.Duration    // base delay between tries, doubled each retry; tests set it tiny
	Concurrency int              // windows in flight; 0 -> 4
	Log         *slog.Logger     // nil -> slog.Default()
}

// TableSummary reports what one table's run did.
type TableSummary struct {
	Table   string
	Windows int
	Rows    int
	Bytes   int64
	Failed  []string // "table window: error"
}

// Summary reports every table's run, in the Source's declaration order.
type Summary struct {
	Tables []TableSummary
}

// RunOnce re-extracts every table's trailing lookback windows as of now: per table,
// window.Lookback(now, table.Grain, table.Lookback).
func (r *Runner) RunOnce(ctx context.Context, now time.Time) (Summary, error) {
	return r.run(ctx, func(t source.Table) ([]window.Window, error) {
		return window.Lookback(now, t.Grain, t.Lookback)
	})
}

// Backfill re-extracts every window in [from, to] for every table: per table,
// window.Range(from, to, table.Grain).
func (r *Runner) Backfill(ctx context.Context, from, to time.Time) (Summary, error) {
	return r.run(ctx, func(t source.Table) ([]window.Window, error) {
		return window.Range(from, to, t.Grain)
	})
}

// job is one (table, window) unit of work.
type job struct {
	tableIdx int
	table    source.Table
	window   window.Window
}

// jobResult is what running one job produced: either rows/bytes on success, or err on failure
// (after all retries were exhausted).
type jobResult struct {
	window window.Window
	rows   int
	bytes  int64
	err    error
}

// run selects windows per table via windowsFor, fetches and writes every (table, window) with
// bounded concurrency and bounded per-window retries, then assembles the Summary and, if any
// window failed after retries, a joined error naming every failed table and window.
func (r *Runner) run(ctx context.Context, windowsFor func(source.Table) ([]window.Window, error)) (Summary, error) {
	tables := r.Source.Tables
	windowsByTable := make([][]window.Window, len(tables))
	var jobs []job
	for i, t := range tables {
		ws, err := windowsFor(t)
		if err != nil {
			return Summary{}, fmt.Errorf("runner: table %s: %w", t.Name, err)
		}
		windowsByTable[i] = ws
		for _, w := range ws {
			jobs = append(jobs, job{tableIdx: i, table: t, window: w})
		}
	}

	concurrency := r.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}

	results := make([]jobResult, len(jobs))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, j job) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = r.runWindow(ctx, j)
		}(i, j)
	}
	wg.Wait()

	// Regroup results by table, preserving each table's window order: jobs (and so results, by
	// shared index) were built table by table, window by window, in windowsFor's order, so a
	// plain index-order walk reconstructs that order regardless of goroutine completion order.
	resultsByTable := make([][]jobResult, len(tables))
	for i, res := range results {
		ti := jobs[i].tableIdx
		resultsByTable[ti] = append(resultsByTable[ti], res)
	}

	summary := Summary{Tables: make([]TableSummary, len(tables))}
	var errs []error
	for i, t := range tables {
		ts := TableSummary{Table: t.Name, Windows: len(windowsByTable[i])}
		for _, res := range resultsByTable[i] {
			if res.err != nil {
				msg := fmt.Sprintf("%s %s: %v", t.Name, res.window, res.err)
				ts.Failed = append(ts.Failed, msg)
				errs = append(errs, errors.New(msg))
				continue
			}
			ts.Rows += res.rows
			ts.Bytes += res.bytes
		}
		summary.Tables[i] = ts
	}

	if len(errs) > 0 {
		return summary, errors.Join(errs...)
	}
	return summary, nil
}

// runWindow fetches and writes one (table, window), retrying up to Attempts times with Backoff
// doubling between tries, and logs exactly one structured line for the window: the successful
// attempt's rows/bytes, or the final attempt's error.
func (r *Runner) runWindow(ctx context.Context, j job) jobResult {
	attempts := r.Attempts
	if attempts <= 0 {
		attempts = defaultAttempts
	}
	logger := r.Log
	if logger == nil {
		logger = slog.Default()
	}

	wait := r.Backoff
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		rows, bytes, err := r.fetchAndWrite(ctx, j)
		if err == nil {
			logger.Info("ingest window", "table", j.table.Name, "window", j.window.String(),
				"rows", rows, "bytes", bytes, "attempt", attempt)
			return jobResult{window: j.window, rows: rows, bytes: bytes}
		}
		lastErr = fmt.Errorf("attempt %d/%d: %w", attempt, attempts, err)
		if attempt == attempts {
			logger.Warn("ingest window failed", "table", j.table.Name, "window", j.window.String(),
				"attempt", attempt, "error", err)
			break
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return jobResult{window: j.window, err: ctx.Err()}
		case <-timer.C:
		}
		wait *= 2
	}
	return jobResult{window: j.window, err: lastErr}
}

// fetchAndWrite stamps extractedAt immediately before Fetch, then writes the resulting batch to
// bronze under dataset "<source>/<table>".
func (r *Runner) fetchAndWrite(ctx context.Context, j job) (rows int, bytesWritten int64, err error) {
	now := r.Now
	if now == nil {
		now = time.Now
	}
	extractedAt := now()

	batch, err := r.Fetcher.Fetch(ctx, j.table.Name, j.window)
	if err != nil {
		return 0, 0, fmt.Errorf("fetch: %w", err)
	}
	dataset := r.Source.Name + "/" + j.table.Name
	result, err := r.Writer.Write(ctx, bronze.Raw, dataset, j.window, batch, extractedAt)
	if err != nil {
		return 0, 0, fmt.Errorf("write: %w", err)
	}
	return result.Rows, result.Bytes, nil
}
