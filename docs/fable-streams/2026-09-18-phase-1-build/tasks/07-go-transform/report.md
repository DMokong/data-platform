## implementer — round 1

### Working-directory check

```
$ cd /Users/dustincheng/projects/data-platform && git rev-parse --show-toplevel && git rev-parse --abbrev-ref HEAD
/Users/dustincheng/projects/data-platform
phase-1
```
Matches the brief's contract; proceeded.

### What I changed

Note: `internal/transform/transform_test.go`, `internal/transform/catalog/catalog_test.go`,
`internal/transform/mentions/extract_test.go` and `internal/transform/mentions/mentions_test.go`
already existed on disk when I started this round (a prior test-adversary pass, per this stream's
pattern), pinning the exact API the brief's Required content describes. I read them first and
implemented against them; report.md had no prior sections and I created it fresh.

Created (all inside the brief's File scope):
- `internal/transform/doc.go`, `internal/transform/transform.go` — `Contract`, `Env`, `Transform`,
  `Materialise` exactly as the brief's Required content pins. `Materialise` runs `t.Run`, then
  `Writer.Write(ctx, bronze.Derived, t.Contract().Output, w, batch, extractedAt)`; a `Run` error is
  wrapped (`errors.Is`-compatible) and never reaches the Writer, so a failed run leaves no file.
- `internal/transform/catalog/doc.go`, `catalog.go` — `All()` (sorted by name) and `Lookup(name)`
  over one explicit `registered` slice (no `init()` registration), holding `mentions.Transform{}`.
- `internal/transform/mentions/doc.go`, `mentions.go`, `extract.go` — the `issue_mentions`
  transform. `extractMentions` builds a case-sensitive
  `\b(<alt1>|<alt2>|...)-[0-9a-z]+(?:\.[0-9]+)*\b` regexp (prefixes `regexp.QuoteMeta`'d) per call.
  `Transform.Run` reads every `*.parquet` under `<WarehouseRoot>/marts/dim_issues/` via
  `parquet.NewGenericReader[dimIssuesRow]` (a 7-field struct that projects the six text columns
  plus `issue_id` out of whatever wider schema the real mart carries — verified this against
  parquet-go v0.32.0's `Convert`/`convertRowGroupTo` source, which fills a target-only optional
  column with NULL rather than erroring when the source file lacks it), derives prefixes as
  everything before the first `-` across the batch's own `issue_id`s, extracts per field, sums
  duplicate mentions, drops self-mentions, and sorts by `(issue_id, mentioned_issue_id, field)`.
- `cmd/transform/doc.go`, `main.go` — `transform <name> --window YYYY-MM-DD [--root .]` (name
  parsed positionally first, then flags), Day window, `extractedAt = time.Now()`, prints
  `transform=<name> window=<date> rows=<n> path=<path>`, exit 1 on a run/parse error, exit 2 and a
  catalog listing on an unknown name. Follows `cmd/ingest`'s `run(args, stdout, stderr) int` shape.
- `dbt/models/marts/post_go/mart_issue_mention_drift.sql`, `_post_go.yml` — see "The dbt build" below.

Modified:
- `dbt/models/sources.yml` — added source `derived` only (table `issue_mentions`,
  `loaded_at_field: _extracted_at`, freshness warn 24h / error 72h, the `external_location` the
  brief specifies). Did not touch the `beads` source.

Not touched: `go.mod`/`go.sum` (task 08's), `internal/telemetry`, `internal/dbt` (task 08's,
untracked in the working tree throughout), any pre_go/staging model, `dbt/dbt_project.yml` (see
escalation below).

### The dbt build

`mart_issue_mention_drift.sql`: latest `dt` of `source('derived','issue_mentions')` (via `order by
dt desc limit 1` — see the DuckDB finding below, not `max(dt)`), anti-joined against the latest
`stg_beads__dependency_snapshots` snapshot's edges in either direction, grouped to
`(issue_id, mentioned_issue_id)` with `mention_count` (summed, cast to `bigint`), `field_count`
(`count(distinct field)`) and `mentioned_issue_exists` (`exists (... ref('dim_issues') ...)`).
`_post_go.yml`: `unique`/`not_null` on `issue_id || '|' || mentioned_issue_id`, `not_null` on both
id columns, and the Conductor amendment's built-in `relationships` test from
`mart_issue_mention_drift.issue_id` to `ref('dim_issues')` field `issue_id`, error severity, with
`arguments:` nesting (the flat `to:`/`field:` form dbt-core 1.12.5 accepts but flags as deprecated
— confirmed by `--show-all-deprecations`, fixed in commit 36e0a95).

**DuckDB finding (fixed, in scope).** `max(dt)` over the `derived` source's hive-partitioned,
`union_by_name` Parquet read crashes DuckDB 1.11.0 with an internal assertion
(`Attempted to access index 8 within vector of size 8`) whenever the glob currently matches
exactly one file — i.e. the source's normal state right after `issue_mentions`'s first window is
ever materialised. Reproduced directly against the same read shape with the `duckdb` CLI; the
crash disappears as soon as a second `dt` partition exists, and does not happen for `select *` or
`count(*)` on the same one-file glob. `order by dt desc limit 1` (a Top-N plan, not an aggregate)
takes a different path and never triggers it, so `latest_mention_dt` uses that instead of `max`.

### Escalation — `scope_breach`

**The literal `dbt build --select tag:pre_go` invocation the brief's Verification commands and
done-check both use, with no flags, cannot exit 0 as long as `_post_go.yml` carries the Conductor
amendment's built-in `relationships` test — and the only fix I found requires editing
`dbt/dbt_project.yml`, which is outside this task's declared File scope (Modify: `sources.yml`
only).**

Root cause, confirmed live: dbt-core 1.12.5's *indirect test selection* defaults to `eager` — a
schema test is swept into a `--select tag:X` run if **any** of its parent nodes matches, not all.
The `relationships` test's two parents are `dim_issues` (tag `pre_go`) and
`mart_issue_mention_drift` (tag `post_go`, its own primary model). Because `dim_issues` is selected
by `tag:pre_go`, eager mode pulls the test into pass 1 too, where `mart_issue_mention_drift`
genuinely does not exist yet (it depends on `source('derived','issue_mentions')`, which the Go
transform hasn't written until *between* the two passes) — so DuckDB errors with `Catalog Error:
Table with name mart_issue_mention_drift does not exist!` and pass 1 exits 1.

I confirmed the only knob that fixes this is dbt-core's project-level `indirect_selection` flag
(`ProjectFlags.indirect_selection` in `dbt/contracts/project.py`; there is no per-test config for
it — checked `TestConfig` in `dbt/artifacts/resources/v1/config.py`, no such field). I added
`flags: {indirect_selection: buildable}` to `dbt/dbt_project.yml` as a diagnostic (not committed —
reverted immediately after, `git diff dbt/dbt_project.yml` now empty, confirmed below) and re-ran
`dbt build --select tag:pre_go` clean: `ERROR=0`, the relationships test correctly excluded from
pass 1 (44 tests instead of 45) because `mart_issue_mention_drift` is not selected and is not an
*ancestor* of anything pass 1 selects. I did not find any in-scope alternative:
- No per-test `indirect_selection` config exists in dbt-core 1.12.5 (verified in source).
- A singular test in `dbt/tests/` (the pattern `stg_beads__events` uses for a similar
  cross-dependency check) would dodge the graph issue, but the brief's Conductor amendment
  explicitly requires the *built-in* `relationships` generic test here ("the project gets its
  first use of the generic test here") and `dbt/tests/` is not in this task's Create list either.
- Dropping `ref('dim_issues')` from the test's `to:` argument (using a bare table-name string
  instead, which dbt's static parser would not track as a graph edge) would sidestep the indirect
  selection issue, but again contradicts the brief's literal instruction
  (`to: ref('dim_issues')`) and would be a silent, unannounced deviation from an explicit
  requirement — worse than surfacing this.

Everything else is independently verified working, including the exact interaction this blocks:
`dbt build --select tag:post_go`, run once `mart_issue_mention_drift` exists, exits 0 with all 5
tests (including the `relationships` test) passing (see below) — the model, the test and the Go
transform are all correct; only the fixed pass-1 invocation, unmodified, cannot get past the
indirectly-selected test with `dbt_project.yml` out of scope.

I committed everything in scope (all tests I can run independently pass) and stopped short of
editing `dbt_project.yml`. Suggested next-round paths: amend this task's File scope to permit one
line in `dbt/dbt_project.yml` (`flags: {indirect_selection: buildable}`, confirmed sufficient), or
amend the fixed pre_go verification/done-check command to pass `--indirect-selection buildable`
explicitly.

### Verification

```
$ cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/transform cmd/transform)" && echo GOFMT-OK
GOFMT-OK

$ cd /Users/dustincheng/projects/data-platform && go vet ./internal/transform/... ./cmd/transform/...
(no output — clean)

$ cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/transform/... ./cmd/transform/... 2>&1 | tail -30
    --- PASS: TestExtractMentions/trailing_sentence_period_is_not_part_of_the_id (0.00s)
    --- PASS: TestExtractMentions/child_id_with_a_dotted_suffix (0.00s)
    --- PASS: TestExtractMentions/child_id_at_sentence_end_keeps_the_dotted_suffix_and_drops_only_the_sentence_period (0.00s)
    --- PASS: TestExtractMentions/a_hyphenated_suffix_run_against_the_id_is_not_part_of_it (0.00s)
    --- PASS: TestExtractMentions/duplicate_mentions_within_one_field_are_counted (0.00s)
    --- PASS: TestExtractMentions/self-mention_is_dropped (0.00s)
    --- PASS: TestExtractMentions/unknown_prefix_is_ignored (0.00s)
    --- PASS: TestExtractMentions/case-sensitive:_an_uppercase_prefix_does_not_match_a_lowercase_one (0.00s)
    --- PASS: TestExtractMentions/multiple_known_prefixes_both_match (0.00s)
    --- PASS: TestExtractMentions/empty_text_yields_no_mentions (0.00s)
    --- PASS: TestExtractMentions/text_with_no_matching_prefix_at_all_yields_no_mentions (0.00s)
=== RUN   TestRun_SchemaAndRowOrder_AC29_AC30
--- PASS: TestRun_SchemaAndRowOrder_AC29_AC30 (0.00s)
=== RUN   TestRun_DuplicateMentionsWithinOneFieldAreCounted_AC29
--- PASS: TestRun_DuplicateMentionsWithinOneFieldAreCounted_AC29 (0.00s)
=== RUN   TestRun_SameMentionInTwoFieldsProducesTwoRows_AC29
--- PASS: TestRun_SameMentionInTwoFieldsProducesTwoRows_AC29 (0.00s)
=== RUN   TestRun_SelfMentionDropped_AC29
--- PASS: TestRun_SelfMentionDropped_AC29 (0.00s)
=== RUN   TestRun_UnknownPrefixIgnored_AC29
--- PASS: TestRun_UnknownPrefixIgnored_AC29 (0.00s)
=== RUN   TestRun_PrefixesAreDerivedFromActualIssueIDs_AC29
--- PASS: TestRun_PrefixesAreDerivedFromActualIssueIDs_AC29 (0.00s)
=== RUN   TestRun_NullFieldsDoNotCrashOrProduceRows_AC29
--- PASS: TestRun_NullFieldsDoNotCrashOrProduceRows_AC29 (0.00s)
=== RUN   TestRun_ToleratesExtraDimIssuesColumns_AC30
--- PASS: TestRun_ToleratesExtraDimIssuesColumns_AC30 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/transform/mentions	0.364s
?   	github.com/DMokong/data-platform/cmd/transform	[no test files]
```
(Full run: `internal/transform` 3/3 PASS, `internal/transform/catalog` 5/5 PASS,
`internal/transform/mentions` 21/21 PASS — all shown passing in the full log above the tail.)

```
$ cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./cmd/transform/ && ! go list -deps ./cmd/transform/ | grep -qi duckdb && echo NO-CGO-NO-DUCKDB
NO-CGO-NO-DUCKDB

$ cd /Users/dustincheng/projects/data-platform && echo "path-leaks: $(grep -rlE 'read_parquet|bronze/' dbt/models --include='*.sql' | wc -l | tr -d ' ')"
path-leaks: 0
```

```
$ cd /Users/dustincheng/projects/data-platform && rm -rf warehouse bronze/derived && mkdir -p warehouse/marts
$ dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go > /tmp/final_pass1.log 2>&1; echo "pass1-exit=$?"
pass1-exit=1
$ tail -20 /tmp/final_pass1.log
[...] 56 of 57 OK created sql external model main.mart_daily_throughput .............. [OK in 0.02s]
[...] 57 of 57 PASS not_null_mart_daily_throughput_dt ................................ [PASS in 0.01s]
Finished running 5 external models, 45 data tests, 7 view models in 0.79s.
Completed with 1 error, 0 partial successes, and 0 warnings:
[ERROR]: in test relationships_mart_issue_mention_drift_issue_id__issue_id__ref_dim_issues_ (models/marts/post_go/_post_go.yml)
  Runtime Error in test relationships_mart_issue_mention_drift_issue_id__issue_id__ref_dim_issues_
  Catalog Error: Table with name mart_issue_mention_drift does not exist!
  LINE 17:     from "dev"."main"."mart_issue_mention_drift"
Done. PASS=56 WARN=0 ERROR=1 SKIP=0 NO-OP=0 REUSED=0 TOTAL=57
```
All 5 pre_go marts (incl. `dim_issues`) and 44 of 45 tests genuinely pass; only the indirectly-
swept `relationships` test errors, per the scope_breach escalation above.

```
$ cd /Users/dustincheng/projects/data-platform && go run ./cmd/transform issue_mentions --window 2026-09-18; echo "transform-exit=$?"
transform=issue_mentions window=2026-09-18 rows=142 path=derived/issue_mentions/dt=2026-09-18/part-0.parquet
transform-exit=0

$ ls -la bronze/derived/issue_mentions/dt=2026-09-18/
total 8
-rw-r--r--@ 1 dustincheng staff 3511 19 Sep 03:43 part-0.parquet

$ duckdb -c "select count(*) n_rows, count(distinct issue_id) n_issues, max(_extracted_at) extracted from read_parquet('bronze/derived/issue_mentions/dt=2026-09-18/part-0.parquet')"
┌────────┬──────────┬──────────────────────────────┐
│ n_rows │ n_issues │           extracted           │
├────────┼──────────┼──────────────────────────────┤
│    142 │       90 │ 2026-09-19 03:43:40.82031+10 │
└────────┴──────────┴──────────────────────────────┘
```

```
$ cd /Users/dustincheng/projects/data-platform && dbt build --project-dir dbt --profiles-dir dbt --select tag:post_go > /tmp/final_pass2.log 2>&1; echo "post2-exit=$?"
post2-exit=0
$ tail -18 /tmp/final_pass2.log
1 of 6 OK created sql external model main.mart_issue_mention_drift ............. [OK in 0.06s]
2 of 6 PASS not_null_mart_issue_mention_drift_issue_id ......................... [PASS in 0.03s]
3 of 6 PASS not_null_mart_issue_mention_drift_issue_id_mentioned_issue_id ...... [PASS in 0.04s]
4 of 6 PASS not_null_mart_issue_mention_drift_mentioned_issue_id ............... [PASS in 0.03s]
5 of 6 PASS relationships_mart_issue_mention_drift_issue_id__issue_id__ref_dim_issues_  [PASS in 0.03s]
6 of 6 PASS unique_mart_issue_mention_drift_issue_id_mentioned_issue_id ........ [PASS in 0.01s]
Finished running 1 external model, 5 data tests in 0.15s.
Completed successfully
Done. PASS=6 WARN=0 ERROR=0 SKIP=0 NO-OP=0 REUSED=0 TOTAL=6

$ ls warehouse/marts/mart_issue_mention_drift/*.parquet
warehouse/marts/mart_issue_mention_drift/data_0.parquet

$ duckdb -c "select count(*) from read_parquet('warehouse/marts/mart_issue_mention_drift/*.parquet')"
92

$ duckdb -c "select * from read_parquet('warehouse/marts/mart_issue_mention_drift/*.parquet') order by mention_count desc limit 5"
┌──────────┬────────────────────┬───────────────┬─────────────┬────────────────────────┐
│ issue_id │ mentioned_issue_id │ mention_count │ field_count │ mentioned_issue_exists │
├──────────┼────────────────────┼───────────────┼─────────────┼────────────────────────┤
│ trk-6r2  │ trk-9ob            │             6 │           2 │ true                   │
│ trk-6r2  │ trk-w7t            │             4 │           2 │ true                   │
│ trk-6r2  │ trk-g7b            │             4 │           1 │ true                   │
│ trk-9lr  │ trk-c9e            │             4 │           2 │ true                   │
│ trk-7up  │ trk-w7t            │             4 │           1 │ true                   │
└──────────┴────────────────────┴───────────────┴─────────────┴────────────────────────┘
```
142 mentions across 90 mentioning issues; 92 of those (issue_id, mentioned_issue_id) pairs have no
declared dependency edge in either direction.

```
$ cd /Users/dustincheng/projects/data-platform && dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:post_go --output name
mart_issue_mention_drift

$ mkdir -p local/tmp && dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --output name | sort > local/tmp/all-models.txt && ( dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:pre_go --output name; dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:post_go --output name ) | sort > local/tmp/tagged-models.txt && echo "models=$(wc -l < local/tmp/all-models.txt | tr -d ' ') tagged=$(wc -l < local/tmp/tagged-models.txt | tr -d ' ') dup=$(uniq -d local/tmp/tagged-models.txt | wc -l | tr -d ' ')" && diff local/tmp/all-models.txt local/tmp/tagged-models.txt && echo TAGS-EXACT
models=13 tagged=13 dup=0
TAGS-EXACT

$ dbt source freshness --project-dir dbt --profiles-dir dbt --select source:derived > local/tmp/freshness-derived.log 2>&1; echo "freshness-exit=$?"; tail -5 local/tmp/freshness-derived.log
freshness-exit=0
1 of 1 START freshness of derived.issue_mentions ............................... [RUN]
1 of 1 PASS freshness of derived.issue_mentions ................................ [PASS]
Finished running 1 source in 0.06s.
Done.
```

```
$ cd /Users/dustincheng/projects/data-platform && git diff dbt/dbt_project.yml
(empty — confirms the diagnostic edit was reverted before any commit)

$ git log --oneline -4
36e0a95 fix(dbt): avoid a path-leak grep false positive in a code comment
af2afbd feat(transform): issue_mentions Go transform and post_go drift mart
82f4c35 feat(telemetry,dbt): OTel setup/instruments and dbt run-results adapter
38253e4 docs(stream): wave 4 evidence (05, 06 accepted); amend 07 brief with generic relationships test

$ git status --short
 M docs/fable-streams/2026-09-18-phase-1-build/tasks/08-telemetry/report.md
```
The only remaining working-tree change is task 08's own report.md (a parallel sibling task's
file, correctly left untouched per the brief's parallel-wave hygiene note).

### Commits

- `af2afbd` — `feat(transform): issue_mentions Go transform and post_go drift mart` (16 files: the
  full Create list plus the `sources.yml` addition).
- `36e0a95` — `fix(dbt): avoid a path-leak grep false positive in a code comment` (reworded a
  comment in `mart_issue_mention_drift.sql` after the path-leak check flagged a prose mention of
  `read_parquet` inside my own explanatory comment).

Both commits stage only File-scope paths via `git add -- <paths>` / `git commit -- <paths>`;
`dbt/dbt_project.yml` was never staged (confirmed via the empty `git diff` above).
## verifier — round 1

**Command 1:** `cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/transform cmd/transform)" && echo GOFMT-OK`
Exit code: 0
```
GOFMT-OK
```

**Command 2:** `cd /Users/dustincheng/projects/data-platform && go vet ./internal/transform/... ./cmd/transform/...`
Exit code: 0
```
(no output)
```

**Command 3:** `cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/transform/... ./cmd/transform/... 2>&1 | tail -40`
Exit code: 0
```
=== RUN   TestExtractMentions/duplicate_mentions_within_one_field_are_counted
=== RUN   TestExtractMentions/self-mention_is_dropped
=== RUN   TestExtractMentions/unknown_prefix_is_ignored
=== RUN   TestExtractMentions/case-sensitive:_an_uppercase_prefix_does_not_match_a_lowercase_one
=== RUN   TestExtractMentions/multiple_known_prefixes_both_match
=== RUN   TestExtractMentions/empty_text_yields_no_mentions
=== RUN   TestExtractMentions/text_with_no_matching_prefix_at_all_yields_no_mentions
--- PASS: TestExtractMentions (0.00s)
    --- PASS: TestExtractMentions/parenthesised_mention (0.00s)
    --- PASS: TestExtractMentions/comma_after_a_mention (0.00s)
    --- PASS: TestExtractMentions/trailing_sentence_period_is_not_part_of_the_id (0.00s)
    --- PASS: TestExtractMentions/child_id_with_a_dotted_suffix (0.00s)
    --- PASS: TestExtractMentions/child_id_at_sentence_end_keeps_the_dotted_suffix_and_drops_only_the_sentence_period (0.00s)
    --- PASS: TestExtractMentions/a_hyphenated_suffix_run_against_the_id_is_not_part_of_it (0.00s)
    --- PASS: TestExtractMentions/duplicate_mentions_within_one_field_are_counted (0.00s)
    --- PASS: TestExtractMentions/self-mention_is_dropped (0.00s)
    --- PASS: TestExtractMentions/unknown_prefix_is_ignored (0.00s)
    --- PASS: TestExtractMentions/case-sensitive:_an_uppercase_prefix_does_not_match_a_lowercase_one (0.00s)
    --- PASS: TestExtractMentions/multiple_known_prefixes_both_match (0.00s)
    --- PASS: TestExtractMentions/empty_text_yields_no_mentions (0.00s)
    --- PASS: TestExtractMentions/text_with_no_matching_prefix_at_all_yields_no_matches (0.00s)
=== RUN   TestRun_SchemaAndRowOrder_AC29_AC30
--- PASS: TestRun_SchemaAndRowOrder_AC29_AC30 (0.00s)
=== RUN   TestRun_DuplicateMentionsWithinOneFieldAreCounted_AC29
--- PASS: TestRun_DuplicateMentionsWithinOneFieldAreCounted_AC29 (0.00s)
=== RUN   TestRun_SameMentionInTwoFieldsProducesTwoRows_AC29
--- PASS: TestRun_SameMentionInTwoFieldsProducesTwoRows_AC29 (0.00s)
=== RUN   TestRun_SelfMentionDropped_AC29
--- PASS: TestRun_SelfMentionDropped_AC29 (0.00s)
=== RUN   TestRun_UnknownPrefixIgnored_AC29
--- PASS: TestRun_UnknownPrefixIgnored_AC29 (0.00s)
=== RUN   TestRun_PrefixesAreDerivedFromActualIssueIDs_AC29
--- PASS: TestRun_PrefixesAreDerivedFromActualIssueIDs_AC29 (0.00s)
=== RUN   TestRun_NullFieldsDoNotCrashOrProduceRows_AC29
--- PASS: TestRun_NullFieldsDoNotCrashOrProduceRows_AC29 (0.00s)
=== RUN   TestRun_ToleratesExtraDimIssuesColumns_AC30
--- PASS: TestRun_ToleratesExtraDimIssuesColumns_AC30 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/transform/mentions	1.142s
?   	github.com/DMokong/data-platform/cmd/transform	[no test files]
```

**Command 4:** `cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./cmd/transform/ && ! go list -deps ./cmd/transform/ | grep -qi duckdb && echo NO-CGO-NO-DUCKDB`
Exit code: 0
```
NO-CGO-NO-DUCKDB
```

**Command 5:** `cd /Users/dustincheng/projects/data-platform && echo "path-leaks: $(grep -rlE 'read_parquet|bronze/' dbt/models --include='*.sql' | wc -l | tr -d ' ')"`
Exit code: 0
```
path-leaks: 0
```

**Command 6:** `cd /Users/dustincheng/projects/data-platform && rm -rf warehouse && mkdir -p warehouse/marts && dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go > /dev/null 2>&1; echo "pass1-exit=$?"; go run ./cmd/transform issue_mentions --window 2026-09-18; echo "transform-exit=$?"; dbt build --project-dir dbt --profiles-dir dbt --select tag:post_go 2>&1 | tail -8`
Exit code: 1 (FAILURE)
```
pass1-exit=1
transform=issue_mentions window=2026-09-18 rows=142 path=derived/issue_mentions/dt=2026-09-18/part-0.parquet
transform-exit=0
[0m17:47:32  6 of 6 START test unique_mart_issue_mention_drift_issue_id_mentioned_issue_id .. [RUN]
[0m17:47:32  6 of 6 PASS unique_mart_issue_mention_drift_issue_id_mentioned_issue_id ........ [[32mPASS[0m in 0.01s]
[0m17:47:32  
[0m17:47:32  Finished running 1 external model, 5 data tests in 0 hours 0 minutes and 0.14 seconds (0.14s).
[0m17:47:32  
[0m17:47:32  [32mCompleted successfully[0m
[0m17:47:32  
[0m17:47:32  Done. PASS=6 WARN=0 ERROR=0 SKIP=0 NO-OP=0 REUSED=0 TOTAL=6
```
**Expected:** `pass1-exit=0`. **Observed:** `pass1-exit=1`. This matches the implementer's scope_breach escalation: the `relationships` test is indirectly selected into pass 1 due to dbt-core 1.12.5's eager indirect selection mode, causing it to fail because `mart_issue_mention_drift` does not exist until after the Go transform runs between the two passes.

**Command 7:** `cd /Users/dustincheng/projects/data-platform && go run ./cmd/transform issue_mentions --window 2026-09-18 >/dev/null && ls -la bronze/derived/issue_mentions/dt=2026-09-18/ && duckdb -c "select count(*) as n_rows, count(distinct issue_id) as n_issues, max(_extracted_at) as extracted from read_parquet('bronze/derived/issue_mentions/dt=2026-09-18/part-0.parquet')"`
Exit code: 0
```
total 8
drwxr-xr-x@ 3 dustincheng  staff    96 19 Sep 03:47 .
drwxr-xr-x@ 3 dustincheng  staff    96 19 Sep 03:43 ..
-rw-r--r--@ 1 dustincheng  staff  3511 19 Sep 03:47 part-0.parquet
┌────────┬──────────┬───────────────────────────────┐
│ n_rows │ n_issues │           extracted           │
│ int64  │  int64   │   timestamp with time zone    │
├────────┼──────────┼───────────────────────────────┤
│    142 │       90 │ 2026-09-19 03:47:47.864857+10 │
└────────┴──────────┴───────────────────────────────┘
```

**Command 8:** `cd /Users/dustincheng/projects/data-platform && duckdb -c "select * from read_parquet('warehouse/marts/mart_issue_mention_drift/*.parquet') order by mention_count desc limit 5"`
Exit code: 0
```
┌──────────┬────────────────────┬───────────────┬─────────────┬────────────────────────┐
│ issue_id │ mentioned_issue_id │ mention_count │ field_count │ mentioned_issue_exists │
│ varchar  │      varchar       │     int64     │    int64    │        boolean         │
├──────────┼────────────────────┼───────────────┼─────────────┼────────────────────────┤
│ trk-6r2  │ trk-9ob            │             6 │           2 │ true                   │
│ trk-6r2  │ trk-w7t            │             4 │           2 │ true                   │
│ trk-6r2  │ trk-g7b            │             4 │           1 │ true                   │
│ trk-9lr  │ trk-c9e            │             4 │           2 │ true                   │
│ trk-7up  │ trk-w7t            │             4 │           1 │ true                   │
└──────────┴────────────────────┴───────────────┴─────────────┴────────────────────────┘
```

**Command 9:** `cd /Users/dustincheng/projects/data-platform && dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:post_go --output name`
Exit code: 0
```
mart_issue_mention_drift
```

**Command 10:** `cd /Users/dustincheng/projects/data-platform && mkdir -p local/tmp && dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --output name | sort > local/tmp/all-models.txt && ( dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:pre_go --output name; dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:post_go --output name ) | sort > local/tmp/tagged-models.txt && echo "models=$(wc -l < local/tmp/all-models.txt | tr -d ' ') tagged=$(wc -l < local/tmp/tagged-models.txt | tr -d ' ') dup=$(uniq -d local/tmp/tagged-models.txt | wc -l | tr -d ' ')" && diff local/tmp/all-models.txt local/tmp/tagged-models.txt && echo TAGS-EXACT`
Exit code: 0
```
models=13 tagged=13 dup=0
TAGS-EXACT
```

**Command 11:** `cd /Users/dustincheng/projects/data-platform && mkdir -p local/tmp && dbt source freshness --project-dir dbt --profiles-dir dbt --select source:derived > local/tmp/freshness-derived.log 2>&1; echo "freshness-exit=$?"; tail -5 local/tmp/freshness-derived.log`
Exit code: 0
```
freshness-exit=0
[0m17:48:13  1 of 1 START freshness of derived.issue_mentions ............................... [RUN]
[0m17:48:13  1 of 1 PASS freshness of derived.issue_mentions ................................ [[32mPASS[0m in 0.01s]
[0m17:48:13  
[0m17:48:13  Finished running 1 source in 0 hours 0 minutes and 0.06 seconds (0.06s).
[0m17:48:13  Done.
```

**Command 12:** `cd /Users/dustincheng/projects/data-platform && git log --oneline -3 && git status --short`
Exit code: 0
```
36e0a95 fix(dbt): avoid a path-leak grep false positive in a code comment
af2afbd feat(transform): issue_mentions Go transform and post_go drift mart
82f4c35 feat(telemetry,dbt): OTel setup/instruments and dbt run-results adapter
 M docs/fable-streams/2026-09-18-phase-1-build/tasks/07-go-transform/report.md
 M docs/fable-streams/2026-09-18-phase-1-build/tasks/08-telemetry/report.md
?? transform
```

## adversarial-reviewer — round 1

### Method

Read the brief, the implementer's round-1 report and the verifier's round-1 record, then the
diff/commits, then independently re-ran the done-check and cross-checked the implementer's
`scope_breach` escalation against the installed dbt-core 1.12.5 source.

### Scope audit

`git show --stat af2afbd` and `36e0a95` touch exactly the brief's Create list plus `sources.yml`
(16 + 1 files); `dbt/dbt_project.yml` has no diff at HEAD since an earlier, unrelated commit
(`git log --oneline -- dbt/dbt_project.yml` → `49be8c8` only; `git diff HEAD -- dbt/dbt_project.yml`
empty). `go.mod`/`go.sum`/`internal/telemetry`/`internal/dbt` are untouched by either commit (those
lines in `git show --stat` belong to task 08's own commit `82f4c35`, confirmed by diffing
`main..phase-1` on those paths separately). No scope breach.

### Done-check re-run (independent)

Ran the brief's literal done-check myself, not trusting the report/verifier tails alone:

```
$ cd /Users/dustincheng/projects/data-platform && go test -count=1 ./internal/transform/... ./cmd/transform/... && rm -rf warehouse && mkdir -p warehouse/marts && dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go >/dev/null && go run ./cmd/transform issue_mentions --window 2026-09-18 && dbt build --project-dir dbt --profiles-dir dbt --select tag:post_go && ls warehouse/marts/mart_issue_mention_drift/*.parquet && echo DONE; echo "overall-exit=$?"
ok  	github.com/DMokong/data-platform/internal/transform	0.385s
ok  	github.com/DMokong/data-platform/internal/transform/catalog	0.489s
ok  	github.com/DMokong/data-platform/internal/transform/mentions	0.226s
?   	github.com/DMokong/data-platform/cmd/transform	[no test files]
overall-exit=1
```

`DONE` never prints; the chain stops at `dbt build --select tag:pre_go`, matching both the
implementer's round-1 evidence and the verifier's round-1 command-6 record (`pass1-exit=1`,
recorded as `Exit code: 1 (FAILURE)`), which independently corroborates this is not a stale-cwd or
transcription artifact — all three runs (implementer, verifier, mine) fail identically:

```
[ERROR]: in test relationships_mart_issue_mention_drift_issue_id__issue_id__ref_dim_issues_ (models/marts/post_go/_post_go.yml)
  Catalog Error: Table with name mart_issue_mention_drift does not exist!
  LINE 17:     from "dev"."main"."mart_issue_mention_drift"
Done. PASS=56 WARN=0 ERROR=1 SKIP=0 NO-OP=0 REUSED=0 TOTAL=57
```

This is also a direct violation of `spec.md` AC-32 ("Starting from a warehouse with no marts,
`dbt build --select tag:pre_go`, then the transform, then `dbt build --select tag:post_go` each
exit 0"): pass 1 exits 1, not 0.

### Checking the implementer's `scope_breach` escalation

Verified the technical claim independently against the installed dbt-core rather than taking the
report's word for it:

```
$ which dbt && dbt --version
/Users/dustincheng/.local/bin/dbt
Core: - installed: 1.12.5
Plugins: - duckdb: 1.11.0
$ grep -n "class TestConfig" -A 20 .../dbt/artifacts/resources/v1/config.py
class TestConfig(NodeAndTestConfig):
    ...
    severity / store_failures / where / limit / fail_calc / warn_if / error_if ...
    # no indirect_selection field
$ grep -rl "indirect_selection" .../dbt/*.py
dbt/flags.py dbt/contracts/project.py dbt/graph/selector.py dbt/graph/cli.py dbt/cli/params.py dbt/cli/main.py
```

`indirect_selection` exists only as a project-level `flags:` setting / CLI flag / env var — there
is no per-test config to scope it to just `tag:post_go`. `dbt/graph/selector.py` confirms default
`IndirectSelection.Eager`: a test is swept into a `--select` run if *any* parent matches, and the
`relationships` test's parents are `dim_issues` (pre_go) and `mart_issue_mention_drift` (post_go),
so eager mode always pulls it into pass 1, where the mart doesn't exist yet. This confirms the
implementer's claim: fixing this without touching `dbt_project.yml` (or amending the fixed
done-check text to add `--indirect-selection buildable`) is not possible under dbt-core 1.12.5 as
installed. The Conductor amendment (add the built-in `relationships` test, "no race filter needed")
and AC-32 / the fixed done-check text are mutually incompatible under the current File scope
(`sources.yml` only) — this is a genuine contradiction in the brief as amended, not an
implementation gap.

### Everything else

Independently re-verified and found sound:
- `internal/transform`, `catalog`, `mentions`, `cmd/transform` match the Required content's exact
  API; regex behaviour checked by hand against the hyphenated-suffix case
  (`internal/transform/mentions/extract.go:8-20`: `[0-9a-z]+` excludes `-`, so `trk-7gx-health` →
  `trk-7gx`, matching the brief).
- `sources.yml`'s `derived` source and `mart_issue_mention_drift.sql`/`_post_go.yml` match the
  brief's required shape (latest-`dt`, anti-join on dependency edges in both directions, grain,
  tests, `relationships` test with `arguments:` nesting).
- Pass 2 in isolation exits 0 with all 5 tests (incl. `relationships`) passing; 142 mentions / 90
  issues in bronze, 92 undeclared-mention rows in the mart — evidence sits next to the report's
  claims (implementer report, verified again in the verifier's independently-run command 7/8).
- `dbt build --select tag:pre_go` and `dbt build --select tag:post_go`'s tag partition is exact
  (`models=13 tagged=13 dup=0`, `TAGS-EXACT`), `path-leaks: 0`, `NO-CGO-NO-DUCKDB`, `gofmt`/`vet`
  clean, freshness PASS.
- Tracker: claimed before edits, milestone comment posted with SHAs, story not closed, no `agent`
  label (`bd show trk-bam.7`).
- One untracked leftover in the repo root: a 15 MB `transform` Mach-O binary (from running the
  verification suite's own `go build ./cmd/transform/` command), not staged or committed in either
  commit — cosmetic, not a scope or done-check issue.

### Verdict rationale

The done-check fails and AC-32 is violated — that alone is `blocker` by the severity rubric. But I
independently confirmed (not just re-stated) that this is a structural conflict the brief itself
created (the 2026-09-19 Conductor amendment vs. AC-32 vs. the File-scope restriction to
`sources.yml`), not a fixable implementation gap: no further round of this task, working only
within its current File scope, can make the literal done-check exit 0. That is a
plan-invalidating discovery, not an ordinary fix-it finding.

```json
{
  "verdict": "escalate",
  "findings": [
    {
      "severity": "blocker",
      "summary": "The brief's done-check does not pass: `dbt build --select tag:pre_go` exits 1 because dbt-core 1.12.5's default eager indirect test selection sweeps the Conductor-amendment `relationships` test into pass 1, where `mart_issue_mention_drift` does not yet exist. This also violates spec.md AC-32 (\"dbt build --select tag:pre_go ... exit 0\").",
      "evidence": "Independent re-run: `overall-exit=1`, DONE never printed; `dbt build --select tag:pre_go` fails with `Catalog Error: Table with name mart_issue_mention_drift does not exist!` in test relationships_mart_issue_mention_drift_issue_id__issue_id__ref_dim_issues_. Identical failure recorded by the implementer (round-1 report, 'The dbt build') and the verifier (round-1, Command 6, 'Exit code: 1 (FAILURE)')."
    }
  ],
  "escalation": {
    "trigger": "plan_invalidating_discovery",
    "detail": "Independently confirmed against the installed dbt-core 1.12.5 source (dbt/artifacts/resources/v1/config.py's TestConfig has no per-test indirect_selection field; dbt/graph/selector.py defaults to IndirectSelection.Eager) that no fix exists within this task's current File scope (sources.yml only) and the done-check's fixed command text: the only lever is dbt_project.yml's project-level `flags: {indirect_selection: buildable}` (out of scope) or adding `--indirect-selection buildable` to the done-check/verification commands themselves (not this task's to change). The 2026-09-19 Conductor amendment requiring the built-in `relationships` test from mart_issue_mention_drift.issue_id to ref('dim_issues') is structurally incompatible with AC-32's two-pass exit-0 requirement and the sources.yml-only File scope, as currently written. Needs a scope/brief decision: widen File scope to permit the one-line dbt_project.yml flag, amend the done-check/verification commands to pass --indirect-selection buildable, or reconsider how the relationships test is declared."
  }
}
```

## fable — round 2

Conductor ruling (2026-09-18T19:53:10Z, opus tier). The escalation is upheld. The fault was the conductor's own round-1 brief
amendment: the generic `relationships` test on the post_go mart. Under dbt-core 1.12.5's default eager indirect
selection, that test is swept into `--select tag:pre_go`.

Settled fact for round 2:
- Add `flags: {indirect_selection: buildable}` to `dbt/dbt_project.yml`. It is now in scope, and the brief says
  exactly which block.
- Keep the relationships test.
- The conductor verified on a scratch copy of the project against a clean warehouse: pass 1 `PASS=56 ERROR=0`,
  no relationships test selected; transform `rows=142`; pass 2 `PASS=6 ERROR=0`, including
  `relationships_mart_issue_mention_drift_issue_id__issue_id__ref_dim_issues_`.
- The verification build check now uses `-o /dev/null`. The old command left a `transform` binary at the repo
  root, which the conductor removed.
