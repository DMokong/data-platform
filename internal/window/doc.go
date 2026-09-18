// Package window provides time-window arithmetic for the ingestion pipeline: aligned windows,
// lookback ranges and backfill ranges over a Grain (Hour or Day). Every bronze file is named
// after the window it covers rather than the wall clock at run time, so re-running a window is
// idempotent and a backfill is just Range() applied to history.
//
// Data-engineering concept: event-time windowing and replayable, window-derived partitions.
package window
