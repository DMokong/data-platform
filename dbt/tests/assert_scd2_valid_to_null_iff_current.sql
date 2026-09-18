-- AC-25: valid_to is null if and only if is_current. Zero rows = pass.
select *
from {{ ref('dim_issues_scd2') }}
where (valid_to is null) != is_current
