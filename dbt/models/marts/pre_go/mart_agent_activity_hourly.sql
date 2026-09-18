{{ config(
    materialized='external',
    location='warehouse/marts/mart_agent_activity_hourly',
    options={'partition_by': 'dt', 'overwrite': true}
) }}

-- Per UTC hour and actor: event count (events.actor) and commit count (dolt_log.committer),
-- unioned onto one actor dimension. Partitioned by dt = the local (Australia/Sydney) date of the
-- hour.
with events_hourly as (

    select
        date_trunc('hour', created_at) as hour_utc,
        actor,
        count(*) as events
    from {{ ref('stg_beads__events') }}
    group by 1, 2

),

commits_hourly as (

    select
        date_trunc('hour', committed_at) as hour_utc,
        committer as actor,
        count(*) as commits
    from {{ ref('stg_beads__commits') }}
    group by 1, 2

),

combined as (

    select hour_utc, actor from events_hourly
    union
    select hour_utc, actor from commits_hourly

)

select
    c.hour_utc,
    c.actor,
    coalesce(e.events, 0) as events,
    coalesce(cm.commits, 0) as commits,
    timezone('Australia/Sydney', c.hour_utc)::date as dt
from combined c
left join events_hourly e using (hour_utc, actor)
left join commits_hourly cm using (hour_utc, actor)
