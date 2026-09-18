{{ config(
    materialized='external',
    location='warehouse/marts/mart_daily_throughput',
    options={'partition_by': 'dt', 'overwrite': true}
) }}

-- Per local (Australia/Sydney) date: issues created, issues closed, and event volume.
-- issues_created / issues_closed come from dim_issues (one row per issue, current state): an
-- issue's created_at and closed_at are fixed points, so grouping the whole dimension by their
-- local date gives each day's totals directly, no history table needed.
with created as (

    select
        timezone('Australia/Sydney', created_at)::date as dt,
        count(*) as issues_created
    from {{ ref('dim_issues') }}
    group by 1

),

closed as (

    select
        timezone('Australia/Sydney', closed_at)::date as dt,
        count(*) as issues_closed
    from {{ ref('dim_issues') }}
    where closed_at is not null
    group by 1

),

events as (

    select
        dt,
        count(*) as events
    from {{ ref('fct_issue_events') }}
    group by 1

),

dates as (

    select dt from created
    union
    select dt from closed
    union
    select dt from events

)

select
    d.dt,
    coalesce(c.issues_created, 0) as issues_created,
    coalesce(cl.issues_closed, 0) as issues_closed,
    coalesce(e.events, 0) as events
from dates d
left join created c using (dt)
left join closed cl using (dt)
left join events e using (dt)
