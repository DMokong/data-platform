-- One row per beads event, deduplicated on the source id (keep the row with the greatest
-- _extracted_at): the DST fall-back hour maps two UTC windows onto one Australia/Sydney local
-- range, so the same event can be re-read by two adjacent hourly windows.
with renamed as (

    select
        id as event_id,
        issue_id,
        event_type,
        actor,
        old_value,
        new_value,
        comment,
        -- events.created_at is naive Australia/Sydney wall time (spec F1); keep the naive value
        -- under its own name so the timezone conversion below and the local-date extraction each
        -- read the source column, not each other's output.
        created_at as created_at_sydney_naive,
        _extracted_at,
        row_number() over (
            partition by id order by _extracted_at desc
        ) as _rn
    from {{ source('beads', 'events') }}

)

select
    event_id,
    issue_id,
    event_type,
    actor,
    old_value,
    new_value,
    comment,
    timezone('Australia/Sydney', created_at_sydney_naive) as created_at,
    cast(created_at_sydney_naive as date) as created_date_local,
    _extracted_at
from renamed
where _rn = 1
