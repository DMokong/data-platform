-- One row per comment per daily snapshot. Timestamp is naive UTC wall time.
select
    id as comment_id,
    issue_id,
    author,
    text as comment_text,
    timezone('UTC', created_at) as created_at,
    dt as snapshot_date,
    _extracted_at
from {{ source('beads', 'comments') }}
