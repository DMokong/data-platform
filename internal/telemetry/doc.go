// Package telemetry sets up OpenTelemetry tracing and metrics for the platform's Temporal
// worker, and turns dbt's run_results.json into spans and metrics in the same stream as the
// orchestrator.
//
// Data-engineering concept: pipeline observability (traces and metrics for data runs).
package telemetry
