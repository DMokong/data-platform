-- marts-views.sql: read-only views over the phase-1 marts, for the DuckDB UI
-- (`duckdb -ui -init scripts/marts-views.sql`) or a one-shot
-- `duckdb -c ".read scripts/marts-views.sql" -c "select ..."`. Run it from the repo root.
--
-- This session never opens warehouse/dev.duckdb -- it is in-memory, and every view reads mart
-- Parquet with read_parquet(), the same way a Go transform does. DuckDB allows one read-write
-- process on a database file, or any number of read-only processes, but never both at once
-- (spec §9 / decision log): dbt owns dev.duckdb while it builds, so a reader that opened it too
-- would either block a build or be blocked by one. Reading the mart Parquet directly, in its own
-- process, sidesteps that rule entirely and is what phase 2's lakehouse readers do as well.
--
-- Re-run dbt build or a Go transform, then re-run (or reconnect and re-run) this file: each view
-- is just a read_parquet() over whatever files are on disk at query time.

CREATE OR REPLACE VIEW dim_issues AS
    SELECT * FROM read_parquet('warehouse/marts/dim_issues/**/*.parquet');

CREATE OR REPLACE VIEW dim_issues_scd2 AS
    SELECT * FROM read_parquet('warehouse/marts/dim_issues_scd2/**/*.parquet');

CREATE OR REPLACE VIEW fct_issue_events AS
    SELECT * FROM read_parquet('warehouse/marts/fct_issue_events/**/*.parquet');

CREATE OR REPLACE VIEW mart_daily_throughput AS
    SELECT * FROM read_parquet('warehouse/marts/mart_daily_throughput/**/*.parquet');

CREATE OR REPLACE VIEW mart_agent_activity_hourly AS
    SELECT * FROM read_parquet('warehouse/marts/mart_agent_activity_hourly/**/*.parquet');

CREATE OR REPLACE VIEW mart_issue_mention_drift AS
    SELECT * FROM read_parquet('warehouse/marts/mart_issue_mention_drift/**/*.parquet');
