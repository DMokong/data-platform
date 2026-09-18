-- AC-25: no two runs for the same issue have overlapping [valid_from, valid_to) intervals.
-- Zero rows = pass.
with intervals as (

    select
        issue_id,
        valid_from,
        coalesce(valid_to, date '9999-12-31') as valid_to_bound
    from {{ ref('dim_issues_scd2') }}

)

select
    a.issue_id,
    a.valid_from as a_valid_from,
    a.valid_to_bound as a_valid_to,
    b.valid_from as b_valid_from,
    b.valid_to_bound as b_valid_to
from intervals a
inner join intervals b
    on a.issue_id = b.issue_id
    and a.valid_from < b.valid_from
    and a.valid_to_bound > b.valid_from
