{{ config(
    materialized='external',
    location='warehouse/marts/fct_issue_events',
    options={'partition_by': 'dt', 'overwrite': true}
) }}

-- Events (already deduplicated on id in staging) enriched with the issue's current issue_type.
-- Partitioned by dt = created_date_local (Australia/Sydney).
select
    e.event_id,
    e.issue_id,
    e.event_type,
    e.actor,
    e.created_at,
    e.created_date_local,
    d.issue_type,
    e.created_date_local as dt
from {{ ref('stg_beads__events') }} e
left join {{ ref('dim_issues') }} d on e.issue_id = d.issue_id
