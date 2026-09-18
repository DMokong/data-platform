-- Proves the F1 Sydney -> UTC conversion (AC-22): every 'created' event's UTC created_at (from
-- stg_beads__events, converted from Australia/Sydney wall time) must lie within 60 seconds of its
-- issue's UTC created_at in the latest issue snapshot (dim_issues, converted from UTC wall time).
-- Zero rows = pass.
select
    e.event_id,
    e.issue_id,
    e.created_at as event_created_at,
    d.created_at as issue_created_at,
    abs(date_diff('second', e.created_at, d.created_at)) as diff_seconds
from {{ ref('stg_beads__events') }} e
inner join {{ ref('dim_issues') }} d on e.issue_id = d.issue_id
where e.event_type = 'created'
  and abs(date_diff('second', e.created_at, d.created_at)) > 60
