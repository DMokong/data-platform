// Command ingest runs the Runner for one declared source: a lookback pass (--once) that
// re-extracts each table's trailing windows, or a backfill (--from/--to) that walks every window
// in a date range. Both call the same Runner methods; backfill needs no special-casing because a
// window is always replayable, which is what makes it a plain command rather than a separate
// tool.
//
// Data-engineering concept: backfill as a first-class command.
package main
