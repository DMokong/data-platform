-- One row per issue per Dolt commit that touched it (dolt_history_issues), hourly windows on
-- commit_date. Timestamps are naive UTC wall time in the source.
select
    id as issue_id,
    commit_hash,
    committer,
    timezone('UTC', commit_date) as commit_date,
    status,
    priority,
    issue_type,
    assignee,
    title,
    timezone('UTC', created_at) as created_at,
    timezone('UTC', updated_at) as updated_at,
    timezone('UTC', closed_at) as closed_at,
    close_reason,
    description,
    design,
    acceptance_criteria,
    notes,
    _extracted_at
from {{ source('beads', 'dolt_history_issues') }}
