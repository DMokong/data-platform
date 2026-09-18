// Package bronze writes the platform's raw layer: one Parquet file per source table per time
// window, at a path derived only from that window.
//
// Data-engineering concept: the idempotent, replayable raw layer (overwrite-on-rerun files).
//
// Three ideas make the layer safe to re-run as often as you like.
//
// Immutable raw data. Bronze holds what the source served, untransformed, kept forever. Every
// table downstream (dbt staging views, marts, Go transforms) is derived from these files and can
// be rebuilt from them, so the bronze Parquet is the only state you would miss if a disk died.
// The Writer adds only provenance columns — _extracted_at, _window_start, _window_end and
// _source_file — which record where and when a row came from without changing the row itself.
//
// Window-derived paths. Path turns (zone, dataset, window) into a relative path such as
// "raw/beads/events/dt=2026-09-15/hh=13.parquet" (hourly) or
// "raw/beads/issues/dt=2026-09-15/part-0.parquet" (daily). It is a pure function of the window's
// UTC start, never of the clock at run time, so extracting the 13:00 window today or next month
// names the same file. The Hive-style dt= directories let query engines prune whole days
// without opening them.
//
// Overwrite on re-run. Parquet cannot be appended to (its schema and row-group index live in a
// footer), and a window should only ever exist once. So the Writer encodes the whole window into
// a temp file next to the target and renames it into place. Readers see either the old file or
// the new one, never a half-written one, and never both. A failed or cancelled write removes its
// temp file and leaves the previous file untouched. Because the encoding is deterministic (fixed
// codec and writer options, no clock or random metadata, column and row order as given), the
// same batch, window and extraction instant always produce byte-identical files. Re-running a
// window, a lookback or a whole backfill therefore converges on the same bronze state, without
// duplicates and without a cleanup step.
package bronze
