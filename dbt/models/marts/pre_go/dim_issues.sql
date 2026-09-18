{{ config(
    materialized='external',
    location='warehouse/marts/dim_issues',
    options={'per_thread_output': true, 'overwrite': true}
) }}

-- One row per issue, taken from the latest snapshot_date. Task 07's Go transform reads the text
-- columns (description, design, acceptance_criteria, notes) by name (brief "Required content").
with snapshots as (

    select * from {{ ref('stg_beads__issue_snapshots') }}

),

latest as (

    select max(snapshot_date) as snapshot_date from snapshots

)

select
    s.issue_id,
    s.title,
    s.status,
    s.issue_type,
    s.priority,
    s.assignee,
    s.created_at,
    s.closed_at,
    case
        when s.closed_at is not null
            then date_diff('second', s.created_at, s.closed_at) / 3600.0
        else null
    end as cycle_time_hours,
    s.description,
    s.design,
    s.acceptance_criteria,
    s.notes,
    s.close_reason
from snapshots s
inner join latest l on s.snapshot_date = l.snapshot_date
