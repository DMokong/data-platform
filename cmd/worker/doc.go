// Command worker is the long-lived Temporal worker process. It polls task queue
// "data-platform" and runs the ingestion workflows (MaterialiseSource, MaterialiseWindow) and
// activities (FetchWindow, WriteWindow, DeleteSpool) until SIGINT or SIGTERM. With
// -apply-schedule it instead upserts the Temporal Schedule for every declared source and exits.
//
// Data-engineering concept: the long-lived worker process.
//
// The worker holds no state of its own. Temporal keeps each workflow's history, so a worker can
// be stopped at any moment; a restarted worker replays the history and carries on where the last
// one stopped, and an activity that was running is retried. Everything durable a worker produces
// is a bronze Parquet file.
//
// Flags: -root (repo root; bronze is <root>/bronze, spool files go to <root>/local/spool) and
// -apply-schedule. Environment: TEMPORAL_ADDRESS (default 127.0.0.1:7233; not "localhost", which
// can resolve to ::1 where the dev server does not listen), TEMPORAL_NAMESPACE (default
// "default"), and the BEADS_* variables read by beads.ConfigFromEnv.
package main
