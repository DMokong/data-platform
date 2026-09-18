-- AC-25: exactly one is_current row per issue. Zero rows = pass.
select issue_id, sum(case when is_current then 1 else 0 end) as current_count
from {{ ref('dim_issues_scd2') }}
group by issue_id
having sum(case when is_current then 1 else 0 end) <> 1
