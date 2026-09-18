{{ config(
    materialized='external',
    location='warehouse/marts/mart_issue_mention_drift',
    options={'per_thread_output': true, 'overwrite': true}
) }}

-- Mentions from the latest issue_mentions dt that have no dependency edge, in either direction,
-- between the two issues in the latest stg_beads__dependency_snapshots snapshot (AC-31). Mentions
-- are extracted from dim_issues itself (task 07's brief), so unlike events -> issue_snapshots
-- (stg_beads__events' singular test) there is no extraction race here: the built-in
-- `relationships` test on issue_id -> dim_issues in _post_go.yml needs no restricting filter.
with latest_mention_dt as (

    -- `order by ... limit 1`, not `max(dt)`: DuckDB 1.11.0's statistics propagator hits an
    -- internal assertion ("Attempted to access index N within vector of size N") aggregating over
    -- the hive-partition `dt` column of a `union_by_name` Parquet read when the glob currently
    -- matches exactly one file (the derived source's normal state right after its first window is
    -- materialised) -- confirmed live, reproducible with a bare `select max(dt) from
    -- read_parquet(..., hive_partitioning = true, union_by_name = true)` against exactly one file,
    -- and gone as soon as a second file/partition exists. Top-N (order by + limit) takes a
    -- different plan path that does not trigger it.
    select dt
    from {{ source('derived', 'issue_mentions') }}
    order by dt desc
    limit 1

),

mentions as (

    select m.issue_id, m.mentioned_issue_id, m.field, m.mention_count
    from {{ source('derived', 'issue_mentions') }} m
    inner join latest_mention_dt on m.dt = latest_mention_dt.dt

),

latest_snapshot as (

    select max(snapshot_date) as snapshot_date
    from {{ ref('stg_beads__dependency_snapshots') }}

),

edges as (

    select d.issue_id, d.depends_on_issue_id
    from {{ ref('stg_beads__dependency_snapshots') }} d
    inner join latest_snapshot on d.snapshot_date = latest_snapshot.snapshot_date

),

undeclared as (

    select m.issue_id, m.mentioned_issue_id, m.field, m.mention_count
    from mentions m
    where not exists (
        select 1
        from edges e
        where (e.issue_id = m.issue_id and e.depends_on_issue_id = m.mentioned_issue_id)
           or (e.issue_id = m.mentioned_issue_id and e.depends_on_issue_id = m.issue_id)
    )

)

select
    u.issue_id,
    u.mentioned_issue_id,
    cast(sum(u.mention_count) as bigint) as mention_count,
    count(distinct u.field) as field_count,
    exists (
        select 1 from {{ ref('dim_issues') }} d where d.issue_id = u.mentioned_issue_id
    ) as mentioned_issue_exists
from undeclared u
group by u.issue_id, u.mentioned_issue_id
