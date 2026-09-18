-- Relationships check, error severity: stg_beads__events.issue_id -> stg_beads__issue_snapshots.issue_id.
--
-- Restricted to events created before the latest issue snapshot's _extracted_at, less a 2-hour
-- margin, so an event about an issue created after that day's snapshot was fetched never flakes
-- this test. issue_snapshots is a daily snapshot; the row for the still-open day is taken AS OF
-- the Fetcher's own extraction instant (a future AS OF instant resolves to "latest state", spec
-- F2), so that row's _extracted_at is exactly the moment its coverage catches up to -- the race the
-- brief names: "an event whose issue was created after that day's snapshot was fetched".
--
-- Written as a singular test, not the built-in `relationships` generic test, because dbt-core
-- 1.12.5's generic-test config parser rejects any macro -- including the built-in ref() -- inside
-- a `where` config value (confirmed live: "the generic test configuration parser currently does
-- not support using custom macros to populate configuration values"). Zero rows = pass.
with cutoff as (

    select max(_extracted_at) - interval '2 hours' as cutoff_at
    from {{ ref('stg_beads__issue_snapshots') }}

)

select e.event_id, e.issue_id, e.created_at
from {{ ref('stg_beads__events') }} e
cross join cutoff
where e.issue_id is not null
  and e.created_at < cutoff.cutoff_at
  and not exists (
    select 1
    from {{ ref('stg_beads__issue_snapshots') }} s
    where s.issue_id = e.issue_id
  )
