// Command worker is the long-lived Temporal worker process. It polls task queue
// "data-platform" and runs the ingestion workflows (MaterialiseSource, MaterialiseWindow), the
// two-pass mart build (BuildMarts) and their activities (FetchWindow, WriteWindow, DeleteSpool,
// DbtBuild, RunTransform) until SIGINT or SIGTERM. With -apply-schedule it instead upserts the
// Temporal Schedule for every declared source and exits.
//
// Data-engineering concept: the long-lived worker process.
//
// The worker holds no state of its own. Temporal keeps each workflow's history, so a worker can
// be stopped at any moment; a restarted worker replays the history and carries on where the last
// one stopped, and an activity that was running is retried. Everything durable a worker produces
// is a Parquet file: bronze from ingestion and the Go transforms, marts from dbt.
//
// Telemetry. Before it connects, the worker installs the OpenTelemetry providers of
// internal/telemetry (service.name data-platform-worker). The Temporal client carries the SDK's
// OpenTelemetry tracing interceptor, which gives every workflow and activity a span, and an
// OpenTelemetry metrics handler for the SDK's own metrics; the activities add the platform's
// metrics and one span per dbt node. On shutdown the providers are flushed, bounded by 5 s.
// -apply-schedule installs none of this.
//
// Flags: -root (repo root; bronze is <root>/bronze, spool files go to <root>/local/spool, dbt runs
// in <root> and Go transforms read <root>/warehouse) and -apply-schedule. Environment:
// TEMPORAL_ADDRESS (default 127.0.0.1:7233; not "localhost", which can resolve to ::1 where the
// dev server does not listen), TEMPORAL_NAMESPACE (default "default"), PLATFORM_DBT_BIN (the dbt
// executable, default "dbt" on PATH), PLATFORM_OTEL_EXPORTER (otlp, the default, stdout or none;
// see internal/telemetry), and the BEADS_* variables read by beads.ConfigFromEnv.
package main
