// Package orchestrator runs the platform on Temporal: the Fetcher and the Writer become activities,
// a workflow per window and a workflow per source decide what runs, a workflow builds the marts
// once ingestion has succeeded, and a Temporal Schedule supplies the cadence that a ticker would
// otherwise provide.
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
// Two-pass mart build. Once every MaterialiseWindow child has succeeded, MaterialiseSource starts
// one BuildMarts child for the day it started in (id build-marts/<source>/<day>); if any
// ingestion child failed, the marts are not rebuilt over incomplete bronze. BuildMarts runs the
// DbtBuild activity with tag:pre_go (staging views and the marts the Go transforms read), then
// one RunTransform activity per catalog transform (each writes a derived bronze file through the
// same Writer), then DbtBuild with tag:post_go (the marts built on the derived files). dbt's own
// DAG orders the models inside each pass. A dbt failure is non-retryable and names the failed
// nodes from run_results.json; any failed step stops the workflow before the next one.
//
// Telemetry. Every activity records its duration and outcome on platform.activity.duration.
// WriteWindow and RunTransform add what they wrote to platform.rows_written and
// platform.bytes_written, and DbtBuild turns run_results.json into one span per dbt node under
// its own activity span. The workflow and activity spans themselves come from the Temporal
// OpenTelemetry interceptor the worker installs.
//
// Schedule. ApplySchedule upserts one Temporal Schedule per source at the source's declared
// cadence. It starts MaterialiseSource, skips a tick while the previous run is still going, and
// catches up at most 10 s of missed ticks after a server outage.
package orchestrator
