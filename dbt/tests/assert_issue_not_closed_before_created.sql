-- Zero rows = pass: no issue's closed_at precedes its created_at.
select issue_id, created_at, closed_at
from {{ ref('dim_issues') }}
where closed_at is not null
  and closed_at < created_at
