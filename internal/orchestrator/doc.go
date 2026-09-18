// Package orchestrator runs ingestion on Temporal: the Fetcher and the Writer become activities,
// a workflow per window and a workflow per source decide what runs, and a Temporal Schedule
// supplies the cadence that a ticker would otherwise provide.
//
// Data-engineering concept: durable, replayable orchestration (activities, workflows, schedules,
// claim-check).
//
// Activities do the I/O. FetchWindow reads one table for one window from the source, WriteWindow
// turns that batch into one bronze Parquet file, and DeleteSpool cleans up between them. Each has
// a start-to-close timeout, a heartbeat timeout and a bounded retry policy, and each is safe to
// retry: a re-run of the same window converges on the same bronze file.
//
// Workflows only decide. MaterialiseWindow runs the three activities for every table of one
// grain in one window; MaterialiseSource computes each grain's lookback windows from
// workflow.Now and starts one MaterialiseWindow child per window, with a workflow id derived from
// the window. Temporal records every decision in the workflow's event history and rebuilds the
// workflow's state by replaying that history, so workflow code must be deterministic: no wall
// clock, native goroutines, randomness, file or network I/O. Those all live in activities.
//
// Claim-check. Temporal limits a payload to 2 MB, and one hourly dolt_history_issues window is
// about 6 MB. So FetchWindow parks the rows in a spool file under <root>/local/spool/ and hands
// the workflow only a SpoolRef (path, row count, fetch instant); WriteWindow redeems the ref.
// WriteWindow never deletes the spool, so every retry of it finds the file. DeleteSpool runs once
// WriteWindow has finished: after it succeeds, and also after it has failed for good (retries
// exhausted or a non-retryable error), because no later activity could redeem that spool.
//
// Schedule. ApplySchedule upserts one Temporal Schedule per source at the source's declared
// cadence. It starts MaterialiseSource, skips a tick while the previous run is still going, and
// catches up at most 10 s of missed ticks after a server outage.
package orchestrator
