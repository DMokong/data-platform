// Package runner wires a source's Fetcher to the Writer over windows: a lookback run that
// re-extracts each table's trailing windows on a fixed cadence tick, and a backfill that walks
// every window in a date range and overwrites each file. The Runner has no loop and no ticker —
// something else (a shell loop today, a Temporal Schedule later) supplies cadence; a call to
// RunOnce or Backfill is one pass over the declared tables that always terminates.
//
// Data-engineering concept: replayable backfill and lookback re-extraction.
package runner
