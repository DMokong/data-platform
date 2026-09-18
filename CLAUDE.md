# data-platform

Personal data-engineering learning project: a Go + dbt data platform that starts on one Mac with
DuckDB and grows into an EKS deployment. **Read `docs/data-platform-spec.md` first** — it is the
architecture spec and the decision log. Companion documents in `docs/`: the architecture review,
the self-hosted DuckDB viability analysis, and interactive archify diagrams under `docs/diagrams/`.

## Principles that shape code here (from the spec)

1. The warehouse is the contract: dbt models and Go transforms agree only on table names.
2. Bronze Parquet is immutable, window-derived, overwrite-on-rerun. Everything else is rebuildable.
3. Ingestion never transforms; provenance columns (`_extracted_at`, `_window_start`, `_window_end`, `_source_file`) are allowed.
4. Storage is separate from compute.
5. Open-source tooling, no new vendor subscriptions.
6. Go never writes to the warehouse and never embeds DuckDB (no CGO). Go writes Parquet; readers use Postgres wire or Parquet.

## Layout (phase 1)

- `cmd/` and `internal/` — Go module: Fetcher, Writer, Runner, Temporal workflows/activities, Go transforms.
- `dbt/` — dbt-duckdb project: `sources.yml` over bronze paths, staging views, marts as Parquet (`external` materialisation).
- `bronze/`, `warehouse/` — local Parquet zones (gitignored).
- `docs/` — spec, decision log, reviews, diagrams. `docs/fable-streams/` holds conductor streams.

## Tracker

Work is tracked in the shared beads tracker (`trk-*`, `~/projects/tracker`, reached via `.beads/redirect`).
Use `bd` for all task tracking; pass `--actor claudeclaw` on writes. Never add the `agent` label
(it hands the issue to the autonomous task runner). Keep issues current: claim before the first
edit, comment at milestones, close in the same turn work finishes.

## Toolchain

Go 1.25 is installed; dbt-core + dbt-duckdb, the DuckDB CLI and the Temporal CLI are also
installed; pinned versions are in the spec's decision log. Install commands: DuckDB CLI and
Temporal CLI via `brew install duckdb temporal`; dbt-core + dbt-duckdb via
`uv tool install --python 3.12 dbt-core --with dbt-duckdb`.

## Source one: beads

Dolt server `127.0.0.1:49209`, database `trk`, MySQL wire protocol, user `root`, no TLS. Tables and
extraction shapes are in spec §1. Treat it as read-only.
