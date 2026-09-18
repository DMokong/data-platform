-- One row per dependency edge per daily snapshot. Timestamp is naive UTC wall time.
-- Key choice (documented in _staging.yml): (dependency_id, snapshot_date). Verified unique against
-- the live bronze data: 1874 rows, 1874 distinct (id, dt) pairs, 0 null ids.
select
    id as dependency_id,
    issue_id,
    "type" as dependency_type,
    depends_on_issue_id,
    timezone('UTC', created_at) as created_at,
    dt as snapshot_date,
    _extracted_at
from {{ source('beads', 'dependencies') }}
