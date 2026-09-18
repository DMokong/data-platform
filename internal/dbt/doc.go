// Package dbt is the adapter the orchestrator uses to shell out to `dbt build --select
// <selector>` and parse the run_results.json artefact it produces.
//
// Data-engineering concept: an orchestration boundary around a SQL transformation tool, with run
// artefacts as data.
package dbt
