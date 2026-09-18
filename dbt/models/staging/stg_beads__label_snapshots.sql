-- One row per issue label per daily snapshot.
select
    issue_id,
    label,
    dt as snapshot_date,
    _extracted_at
from {{ source('beads', 'labels') }}
