// Package beads is the Fetcher for the beads tracker: a Dolt server spoken to over the MySQL
// wire protocol, read-only.
//
// Data-engineering concept: extraction from an event log plus time-travel snapshots, without transformation.
//
// Two extraction shapes cover the seven declared tables. The append-only logs (events,
// dolt_history_issues, dolt_log) are read in hourly windows: a window selects the rows whose time
// column lies in [Start, End), so any past hour can be re-extracted and yields the same rows. The
// current-state tables (issues, comments, dependencies, labels) are read as daily snapshots
// through Dolt time travel, SELECT ... AS OF TIMESTAMP(<window end>), so a snapshot of any past
// day can be taken again later rather than only "now". Both make every window replayable, which
// is what lets the bronze layer overwrite on re-run and backfill arbitrary history.
//
// Nothing is cleaned or converted on the way through. Columns keep the table's order and are
// typed from the driver's column types; JSON stays text and TINYINT stays an integer. The one
// timestamp quirk of the source, that events.created_at is stored as Australia/Sydney wall time
// while every other timestamp is UTC, is handled only in the window predicate: the events bounds
// are converted to Sydney wall time, while the values themselves are passed through as served and
// converted later, in dbt staging.
//
// Every query runs inside a read-only transaction that is always rolled back, table names come
// only from the fixed declaration, and ORDER BY a unique key makes each batch deterministic.
package beads
