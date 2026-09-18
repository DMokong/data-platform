-- One row per issue per daily snapshot. Timestamps are naive UTC wall time in the source; convert
-- them explicitly with timezone('UTC', ...) rather than a bare cast, because a bare
-- timestamp -> timestamptz cast is interpreted in the session's local time zone, not UTC.
select
    id as issue_id,
    title,
    status,
    issue_type,
    priority,
    assignee,
    timezone('UTC', created_at) as created_at,
    timezone('UTC', updated_at) as updated_at,
    timezone('UTC', closed_at) as closed_at,
    close_reason,
    description,
    design,
    acceptance_criteria,
    notes,
    dt as snapshot_date,
    _extracted_at
from {{ source('beads', 'issues') }}
