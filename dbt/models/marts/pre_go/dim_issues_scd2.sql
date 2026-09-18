{{ config(
    materialized='external',
    location='warehouse/marts/dim_issues_scd2',
    options={'per_thread_output': true, 'overwrite': true}
) }}

-- SCD Type 2 over daily issue snapshots: one row per issue per contiguous run of unchanged
-- (status, priority, issue_type, assignee, title). A run starts a new group whenever any tracked
-- attribute differs from the previous snapshot_date for the same issue (or at the issue's first
-- snapshot); a running sum of that change flag numbers the runs ("gaps and islands").
with snapshots as (

    select
        issue_id,
        snapshot_date,
        status,
        priority,
        issue_type,
        assignee,
        title
    from {{ ref('stg_beads__issue_snapshots') }}

),

flagged as (

    select
        *,
        case
            when
                lag(status) over w is distinct from status
                or lag(priority) over w is distinct from priority
                or lag(issue_type) over w is distinct from issue_type
                or lag(assignee) over w is distinct from assignee
                or lag(title) over w is distinct from title
            then 1
            else 0
        end as is_change
    from snapshots
    window w as (partition by issue_id order by snapshot_date)

),

grouped as (

    select
        *,
        sum(is_change) over (
            partition by issue_id order by snapshot_date
            rows between unbounded preceding and current row
        ) as run_id
    from flagged

),

runs as (

    select
        issue_id,
        run_id,
        min(snapshot_date) as valid_from,
        max(status) as status,
        max(priority) as priority,
        max(issue_type) as issue_type,
        max(assignee) as assignee,
        max(title) as title
    from grouped
    group by issue_id, run_id

)

select
    issue_id,
    status,
    priority,
    issue_type,
    assignee,
    title,
    valid_from,
    lead(valid_from) over (partition by issue_id order by valid_from) as valid_to,
    lead(valid_from) over (partition by issue_id order by valid_from) is null as is_current
from runs
