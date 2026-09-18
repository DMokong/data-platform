-- One row per Dolt commit (dolt_log), hourly windows on date. Timestamp is naive UTC wall time.
select
    commit_hash,
    committer,
    timezone('UTC', "date") as committed_at,
    _extracted_at
from {{ source('beads', 'dolt_log') }}
