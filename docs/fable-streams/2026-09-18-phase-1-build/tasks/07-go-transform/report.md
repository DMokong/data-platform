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

## implementer — round 2

### Working-directory check

```
$ cd /Users/dustincheng/projects/data-platform && git rev-parse --show-toplevel && git rev-parse --abbrev-ref HEAD
/Users/dustincheng/projects/data-platform
phase-1
```
Matched the brief's contract; proceeded.

### What I changed

Modified exactly the file the round-2 conductor amendment names:
- `dbt/dbt_project.yml` — added one top-level `flags:` block (`indirect_selection: buildable`)
  after `clean-targets:` and before `models:`, with a four-line comment explaining why (a
  `relationships` test whose two parents span a pre_go model and a post_go mart needs *buildable*
  indirect selection, not the default *eager*, so it isn't swept into `--select tag:pre_go` before
  the post_go mart exists). Nothing else in the file changed — confirmed by the diff below (7
  insertions, 0 deletions, no other hunk).

No other path touched. `internal/transform*`, `internal/transform/catalog*`,
`internal/transform/mentions*`, `cmd/transform*`, `dbt/models/sources.yml`,
`dbt/models/marts/post_go/*` are unchanged from round 1 (`af2afbd`/`36e0a95`), still passing per
the verification below.

### Mention and drift counts

Unchanged from round 1: the transform writes 142 mention rows across 90 distinct mentioning
issues to `bronze/derived/issue_mentions/dt=2026-09-18/part-0.parquet`; `mart_issue_mention_drift`
holds the undeclared-dependency-edge subset (top rows by `mention_count` shown below, same as
round 1's `trk-6r2 -> trk-9ob` at 6).

### Verification

```
$ cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/transform cmd/transform)" && echo GOFMT-OK
GOFMT-OK
```

```
$ cd /Users/dustincheng/projects/data-platform && go vet ./internal/transform/... ./cmd/transform/...
(no output — clean; exit 0)
```

```
$ cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/transform/... ./cmd/transform/... 2>&1 | tail -40
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
ok  	github.com/DMokong/data-platform/internal/transform/mentions	1.198s
?   	github.com/DMokong/data-platform/cmd/transform	[no test files]
```
(Full run: `internal/transform` and `internal/transform/catalog` also PASS, shown above the tail.)

```
$ cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build -o /dev/null ./cmd/transform/ && ! go list -deps ./cmd/transform/ | grep -qi duckdb && echo NO-CGO-NO-DUCKDB
NO-CGO-NO-DUCKDB
```
(Building to `-o /dev/null` per the round-2 amendment leaves no stray `transform` binary at the
repo root — confirmed by `ls -la transform` below the diff, no match.)

```
$ cd /Users/dustincheng/projects/data-platform && echo "path-leaks: $(grep -rlE 'read_parquet|bronze/' dbt/models --include='*.sql' | wc -l | tr -d ' ')"
path-leaks: 0
```

**The blocker fix, verified directly** (round 1's failing command, re-run unmodified after the
`dbt_project.yml` change):
```
$ cd /Users/dustincheng/projects/data-platform && rm -rf warehouse && mkdir -p warehouse/marts && dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go > /dev/null 2>&1; echo "pass1-exit=$?"; go run ./cmd/transform issue_mentions --window 2026-09-18; echo "transform-exit=$?"; dbt build --project-dir dbt --profiles-dir dbt --select tag:post_go 2>&1 | tail -8
pass1-exit=0
transform=issue_mentions window=2026-09-18 rows=142 path=derived/issue_mentions/dt=2026-09-18/part-0.parquet
transform-exit=0
[...]6 of 6 START test unique_mart_issue_mention_drift_issue_id_mentioned_issue_id .. [RUN]
[...]6 of 6 PASS unique_mart_issue_mention_drift_issue_id_mentioned_issue_id ........ [PASS in 0.01s]
[...]Finished running 1 external model, 5 data tests in 0 hours 0 minutes and 0.13 seconds (0.13s).
[...]Completed successfully
[...]Done. PASS=6 WARN=0 ERROR=0 SKIP=0 NO-OP=0 REUSED=0 TOTAL=6
```
Pass-1 log tail (`/tmp/pass1_round2.log`), confirming the conductor's verified `PASS=56 ERROR=0`
with the `relationships` test correctly excluded (56 pass1 items, not 57 — no error this time):
```
[...]Finished running 5 external models, 44 data tests, 7 view models in 0 hours 0 minutes and 0.75 seconds (0.75s).
[...]Completed successfully
[...]Done. PASS=56 WARN=0 ERROR=0 SKIP=0 NO-OP=0 REUSED=0 TOTAL=56
```

```
$ cd /Users/dustincheng/projects/data-platform && go run ./cmd/transform issue_mentions --window 2026-09-18 >/dev/null && ls -la bronze/derived/issue_mentions/dt=2026-09-18/ && duckdb -c "select count(*) as n_rows, count(distinct issue_id) as n_issues, max(_extracted_at) as extracted from read_parquet('bronze/derived/issue_mentions/dt=2026-09-18/part-0.parquet')"
total 8
drwxr-xr-x@ 3 dustincheng  staff    96 19 Sep 05:54 .
drwxr-xr-x@ 3 dustincheng  staff    96 19 Sep 03:43 ..
-rw-r--r--@ 1 dustincheng  staff  3511 19 Sep 05:54 part-0.parquet
┌────────┬──────────┬───────────────────────────────┐
│ n_rows │ n_issues │           extracted           │
│ int64  │  int64   │   timestamp with time zone    │
├────────┼──────────┼───────────────────────────────┤
│    142 │       90 │ 2026-09-19 05:54:22.100898+10 │
└────────┴──────────┴───────────────────────────────┘
```
The derived directory holds only `part-0.parquet` — matches the brief's expected result.

```
$ cd /Users/dustincheng/projects/data-platform && duckdb -c "select * from read_parquet('warehouse/marts/mart_issue_mention_drift/*.parquet') order by mention_count desc limit 5"
┌──────────┬────────────────────┬───────────────┬─────────────┬────────────────────────┐
│ issue_id │ mentioned_issue_id │ mention_count │ field_count │ mentioned_issue_exists │
│ varchar  │      varchar       │     int64     │    int64    │        boolean         │
├──────────┼────────────────────┼───────────────┼─────────────┼────────────────────────┤
│ trk-6r2  │ trk-9ob            │             6 │           2 │ true                   │
│ trk-6r2  │ trk-g7b            │             4 │           1 │ true                   │
│ trk-9lr  │ trk-c9e            │             4 │           2 │ true                   │
│ trk-7up  │ trk-w7t            │             4 │           1 │ true                   │
│ trk-6r2  │ trk-w7t            │             4 │           2 │ true                   │
└──────────┴────────────────────┴───────────────┴─────────────┴────────────────────────┘
```

```
$ cd /Users/dustincheng/projects/data-platform && dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:post_go --output name
mart_issue_mention_drift
```
The post_go listing is exactly `mart_issue_mention_drift` — matches the brief's expected result.

```
$ cd /Users/dustincheng/projects/data-platform && mkdir -p local/tmp && dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --output name | sort > local/tmp/all-models.txt && ( dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:pre_go --output name; dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:post_go --output name ) | sort > local/tmp/tagged-models.txt && echo "models=$(wc -l < local/tmp/all-models.txt | tr -d ' ') tagged=$(wc -l < local/tmp/tagged-models.txt | tr -d ' ') dup=$(uniq -d local/tmp/tagged-models.txt | wc -l | tr -d ' ')" && diff local/tmp/all-models.txt local/tmp/tagged-models.txt && echo TAGS-EXACT
models=13 tagged=13 dup=0
TAGS-EXACT
```

```
$ cd /Users/dustincheng/projects/data-platform && mkdir -p local/tmp && dbt source freshness --project-dir dbt --profiles-dir dbt --select source:derived > local/tmp/freshness-derived.log 2>&1; echo "freshness-exit=$?"; tail -5 local/tmp/freshness-derived.log
freshness-exit=0
[...]1 of 1 START freshness of derived.issue_mentions ............................... [RUN]
[...]1 of 1 PASS freshness of derived.issue_mentions ................................ [PASS]
[...]Finished running 1 source in 0.06s.
[...]Done.
```

**The brief's literal done-check, run in full (the command that previously exited 1 before `DONE`):**
```
$ cd /Users/dustincheng/projects/data-platform && go test -count=1 ./internal/transform/... ./cmd/transform/... && rm -rf warehouse && mkdir -p warehouse/marts && dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go >/dev/null && go run ./cmd/transform issue_mentions --window 2026-09-18 && dbt build --project-dir dbt --profiles-dir dbt --select tag:post_go && ls warehouse/marts/mart_issue_mention_drift/*.parquet && echo DONE
ok  	github.com/DMokong/data-platform/internal/transform	0.226s
ok  	github.com/DMokong/data-platform/internal/transform/catalog	0.469s
ok  	github.com/DMokong/data-platform/internal/transform/mentions	0.358s
?   	github.com/DMokong/data-platform/cmd/transform	[no test files]
transform=issue_mentions window=2026-09-18 rows=142 path=derived/issue_mentions/dt=2026-09-18/part-0.parquet
[...]1 of 6 START sql external model main.mart_issue_mention_drift .................. [RUN]
[...]1 of 6 OK created sql external model main.mart_issue_mention_drift ............. [OK in 0.05s]
[...]5 of 6 START test relationships_mart_issue_mention_drift_issue_id__issue_id__ref_dim_issues_  [RUN]
[...]5 of 6 PASS relationships_mart_issue_mention_drift_issue_id__issue_id__ref_dim_issues_  [PASS in 0.03s]
[...]6 of 6 PASS unique_mart_issue_mention_drift_issue_id_mentioned_issue_id ........ [PASS in 0.01s]
[...]Done. PASS=6 WARN=0 ERROR=0 SKIP=0 NO-OP=0 REUSED=0 TOTAL=6
warehouse/marts/mart_issue_mention_drift/data_0.parquet
DONE
```
`DONE` printed — the done-check now passes end to end.

```
$ cd /Users/dustincheng/projects/data-platform && git diff dbt/dbt_project.yml
diff --git a/dbt/dbt_project.yml b/dbt/dbt_project.yml
index 6005c07..11951cb 100644
--- a/dbt/dbt_project.yml
+++ b/dbt/dbt_project.yml
@@ -12,6 +12,13 @@ clean-targets:
   - "target"
   - "logs"
 
+# Buildable indirect selection: the post_go relationships test depends on both a pre_go model
+# (dim_issues) and the post_go mart it's declared on. Default "eager" selection would sweep it
+# into `--select tag:pre_go` before the mart exists; "buildable" only runs a test once all of its
+# parents are in the selected build, so it runs in the post_go pass instead.
+flags:
+  indirect_selection: buildable
+
 models:
   data_platform:
     staging:
```
(shown pre-commit; matches exactly the round-2 amendment's instruction — one block, a short
comment, nothing else in the file changed)

```
$ cd /Users/dustincheng/projects/data-platform && ls -la transform
ls: transform: No such file or directory
```
No stray `transform` binary at the repo root (the `-o /dev/null` build left none).

```
$ cd /Users/dustincheng/projects/data-platform && git log --oneline -3 && git status --short
f69645a fix(dbt): use buildable indirect test selection for the two-pass build
21000b1 docs(stream): wave 5 evidence for 08-telemetry (accepted); 07 escalated and resumed
d4fdc7f docs(stream): adjudicate 07-go-transform escalation (buildable indirect selection); amend brief
```
(`git status --short` printed nothing — clean tree; `21000b1` is the conductor's own stream-doc
commit between rounds, not mine.)

### Commit

- `f69645a` — `fix(dbt): use buildable indirect test selection for the two-pass build` (1 file:
  `dbt/dbt_project.yml`, 7 insertions, 0 deletions — exactly the round-2 amendment's scope).
  Staged with `git add -- dbt/dbt_project.yml`, committed with `git commit -- dbt/dbt_project.yml`.

### Disputes of prior findings

None. The round-1 `plan_invalidating_discovery` escalation and the adversarial-reviewer's blocker
finding were both correct as filed against round 1's File scope; the round-2 amendment widened
scope to permit the exact fix both identified, and applying it resolves the blocker as predicted
(pass 1 `PASS=56 ERROR=0`, no relationships test selected; pass 2 `PASS=6 ERROR=0` including the
relationships test) — matching the conductor's own pre-verified numbers in the "fable — round 2"
section above exactly.

## test-author — round 1

> Persisted by the conductor on 2026-09-19 from the test-author's returned text (journal of workflow run wf_0988c278-1de). The harness blocked the agent's Write to report.md. The text below is verbatim; it is appended after later rounds because it arrived out of band.

**Working-directory contract.** Verified before any write: `git rev-parse --show-toplevel` → `/Users/dustincheng/projects/data-platform`, `git rev-parse --abbrev-ref HEAD` → `phase-1`. Match — proceeded.

### Scope
Mode A (fresh tests, no implementation yet). Wrote only the four Go test files the brief's "Tests (Go)" section calls for. Touched no dbt files, no `go.mod`/`go.sum` (already modified in the working tree by task 08's parallel work — left untouched), and nothing under `cmd/transform`. Never ran `go build ./...` / `go test ./...`, only the two package-scoped paths the brief pins.

### Tests written
- `/Users/dustincheng/projects/data-platform/internal/transform/transform_test.go` — `Materialise` (AC-30):
  - `TestMaterialise_WritesThroughSharedWriter_AC30`
  - `TestMaterialise_RerunOverwrites_AC30`
  - `TestMaterialise_RunErrorLeavesNoFile_AC30`
- `/Users/dustincheng/projects/data-platform/internal/transform/catalog/catalog_test.go` — the registry (AC-28):
  - `TestAll_ListsIssueMentionsWithExactContract_AC28`
  - `TestAll_SortedByName_AC28`
  - `TestLookup_FindsIssueMentions_AC28`
  - `TestLookup_UnknownNameNotFound_AC28`
  - `TestLookup_AgreesWithAll_AC28`
- `/Users/dustincheng/projects/data-platform/internal/transform/mentions/extract_test.go` — pure text extraction (AC-29): table-driven `TestExtractMentions` with 13 subtests (punctuation/boundaries, child id `trk-bam.1`, child id at sentence end, hyphenated suffix `trk-7gx-health`→`trk-7gx`, duplicate count, self-mention dropped, unknown prefix ignored, case-sensitivity, multiple prefixes, empty text, no-match text).
- `/Users/dustincheng/projects/data-platform/internal/transform/mentions/mentions_test.go` — `Transform.Run` end-to-end over generated dim_issues-shaped Parquet fixtures (AC-29/AC-30):
  - `TestRun_SchemaAndRowOrder_AC29_AC30` (exact 4-column schema; sort by issue_id/mentioned_id/field via a deliberately non-pre-sorted, two-file fixture)
  - `TestRun_DuplicateMentionsWithinOneFieldAreCounted_AC29`
  - `TestRun_SameMentionInTwoFieldsProducesTwoRows_AC29`
  - `TestRun_SelfMentionDropped_AC29`
  - `TestRun_UnknownPrefixIgnored_AC29`
  - `TestRun_PrefixesAreDerivedFromActualIssueIDs_AC29`
  - `TestRun_NullFieldsDoNotCrashOrProduceRows_AC29`
  - `TestRun_ToleratesExtraDimIssuesColumns_AC30`

### Invented API (brief left unspecified; documented as REQUIRED IMPLEMENTATION HOOK comments, mirroring `internal/bronze/writer_test.go`'s `forceEncodeErr` convention)
- `mentions.Transform` — exported, zero-value struct implementing `transform.Transform` (mirrors `beads.Fetcher`-implements-`source.Fetcher` in `internal/source/beads/fetcher.go`).
- `extractMentions(text, selfID string, prefixes []string) map[string]int64` — unexported, in `extract.go`; the pure single-field extraction `mentions.go`'s `Run` calls per field per issue.

`Contract`/`Env`/`Transform`/`Materialise` and catalog's `All`/`Lookup` needed no invention — used exactly as the brief pins them.

### cmd/transform: no test file created
Brief's "Create" list gives only `cmd/transform/doc.go, main.go` (no conditional test file), unlike task 04's brief which explicitly named `cmd/ingest/main_test.go "if you test flag parsing"`. Read as a deliberate omission; did not create one.

### Failing-first evidence
Validated design correctness in an isolated scratch module (never touched real repo production code): copied the four test files plus hand-written stub implementations into a throwaway module, ran the full suite — all 22 test cases passed. Then mutation-tested: removing the stub's final `sort.Slice` made `TestRun_SchemaAndRowOrder_AC29_AC30` fail 8/8 tries; removing the `selfID` skip in the stub's `extractMentions` made both `TestExtractMentions/self-mention_is_dropped` and `TestRun_SelfMentionDropped_AC29` fail with clear diffs (`got map[trk-other:1 trk-self:1], want map[trk-other:1]`). Restoring passed again. Scratch module discarded.

Then ran the brief's real verification commands against the actual repo (no implementation exists):

```
$ go vet ./internal/transform/...
github.com/DMokong/data-platform/internal/transform: no non-test Go files in .../internal/transform
github.com/DMokong/data-platform/internal/transform/catalog: no non-test Go files in .../internal/transform/catalog
# github.com/DMokong/data-platform/internal/transform/mentions [.../mentions.test]
internal/transform/mentions/extract_test.go:137:11: undefined: extractMentions
# github.com/DMokong/data-platform/internal/transform
vet: internal/transform/transform_test.go:46:11: undefined: Contract
```

```
$ go test -count=1 -v ./internal/transform/...
github.com/DMokong/data-platform/internal/transform: no non-test Go files in .../internal/transform
github.com/DMokong/data-platform/internal/transform/catalog: no non-test Go files in .../internal/transform/catalog
internal/transform/mentions/extract_test.go:137:11: undefined: extractMentions
internal/transform/transform_test.go:46:11: undefined: Contract
internal/transform/transform_test.go:47:41: undefined: Env
internal/transform/transform_test.go:145:14: undefined: Materialise
... too many errors
FAIL	github.com/DMokong/data-platform/internal/transform [build failed]
FAIL	github.com/DMokong/data-platform/internal/transform/catalog [build failed]
FAIL	github.com/DMokong/data-platform/internal/transform/mentions [build failed]
```

**Why this is the right failure, not a broken harness:** every line traces to a symbol/package the brief requires the implementer to add, currently absent — `Contract`/`Env`/`Transform`/`Materialise` in `internal/transform` (unwritten), `extractMentions` in `mentions` (unwritten), and `catalog` failing because both it and its `internal/transform` import have zero non-test Go files. For a brand-new multi-package Go feature at round 1, this whole-chain compile failure is the correct, coarser-grained analogue of "missing route/function the test correctly attempted to call." `gofmt -l internal/transform internal/transform/catalog internal/transform/mentions` → clean, no output.

### Framework/conventions detected
Existing tests (`internal/bronze/writer_test.go`, `cmd/ingest/main_test.go`) use plain `testing`, table-driven subtests, `t.TempDir()` fixtures, direct parquet-go read-back (no testify despite it being in go.mod), one AC id per test name/comment, and a documented "REQUIRED IMPLEMENTATION HOOK" pattern for not-yet-existing seams. Followed all of these.

### Tracker
Not touched — test-author's role instructions describe writing tests and reporting only; left `bd update trk-bam.7 --claim` to whichever agent runs the implementation/commit round.

### Files touched
Created only: `internal/transform/transform_test.go`, `internal/transform/catalog/catalog_test.go`, `internal/transform/mentions/extract_test.go`, `internal/transform/mentions/mentions_test.go`. Nothing staged or committed.

## verifier — round 2

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
ok  	github.com/DMokong/data-platform/internal/transform/mentions	0.458s
?   	github.com/DMokong/data-platform/cmd/transform	[no test files]
```

**Command 4:** `cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build -o /dev/null ./cmd/transform/ && ! go list -deps ./cmd/transform/ | grep -qi duckdb && echo NO-CGO-NO-DUCKDB`
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
Exit code: 0
```
pass1-exit=0
transform=issue_mentions window=2026-09-18 rows=142 path=derived/issue_mentions/dt=2026-09-18/part-0.parquet
transform-exit=0
[0m19:57:08  6 of 6 START test unique_mart_issue_mention_drift_issue_id_mentioned_issue_id .. [RUN]
[0m19:57:08  6 of 6 PASS unique_mart_issue_mention_drift_issue_id_mentioned_issue_id ........ [[32mPASS[0m in 0.01s]
[0m19:57:08  
[0m19:57:08  Finished running 1 external model, 5 data tests in 0 hours 0 minutes and 0.13 seconds (0.13s).
[0m19:57:08  
[0m19:57:08  [32mCompleted successfully[0m
[0m19:57:08  
[0m19:57:08  Done. PASS=6 WARN=0 ERROR=0 SKIP=0 NO-OP=0 REUSED=0 TOTAL=6
```
**Expected:** `pass1-exit=0`, `transform-exit=0`, post_go build `ERROR=0`. All match.

**Command 7:** `cd /Users/dustincheng/projects/data-platform && go run ./cmd/transform issue_mentions --window 2026-09-18 >/dev/null && ls -la bronze/derived/issue_mentions/dt=2026-09-18/ && duckdb -c "select count(*) as n_rows, count(distinct issue_id) as n_issues, max(_extracted_at) as extracted from read_parquet('bronze/derived/issue_mentions/dt=2026-09-18/part-0.parquet')"`
Exit code: 0
```
total 8
drwxr-xr-x@ 3 dustincheng  staff    96 19 Sep 05:57 .
drwxr-xr-x@ 3 dustincheng  staff    96 19 Sep 03:43 ..
-rw-r--r--@ 1 dustincheng  staff  3511 19 Sep 05:57 part-0.parquet
┌────────┬──────────┬───────────────────────────────┐
│ n_rows │ n_issues │           extracted           │
│ int64  │  int64   │   timestamp with time zone    │
├────────┼──────────┼───────────────────────────────┤
│    142 │       90 │ 2026-09-19 05:57:14.102592+10 │
└────────┴──────────┴───────────────────────────────┘
```
Derived directory holds only `part-0.parquet`, 142 mention rows across 90 distinct issues.

**Command 8:** `cd /Users/dustincheng/projects/data-platform && duckdb -c "select * from read_parquet('warehouse/marts/mart_issue_mention_drift/*.parquet') order by mention_count desc limit 5"`
Exit code: 0
```
┌──────────┬────────────────────┬───────────────┬─────────────┬────────────────────────┐
│ issue_id │ mentioned_issue_id │ mention_count │ field_count │ mentioned_issue_exists │
│ varchar  │      varchar       │     int64     │    int64    │        boolean         │
├──────────┼────────────────────┼───────────────┼─────────────┼────────────────────────┤
│ trk-6r2  │ trk-9ob            │             6 │           2 │ true                   │
│ trk-6r2  │ trk-g7b            │             4 │           1 │ true                   │
│ trk-9lr  │ trk-c9e            │             4 │           2 │ true                   │
│ trk-7up  │ trk-w7t            │             4 │           1 │ true                   │
│ trk-6r2  │ trk-w7t            │             4 │           2 │ true                   │
└──────────┴────────────────────┴───────────────┴─────────────┴───────────────────────┘
```

**Command 9:** `cd /Users/dustincheng/projects/data-platform && dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:post_go --output name`
Exit code: 0
```
mart_issue_mention_drift
```
Post_go listing is exactly `mart_issue_mention_drift`.

**Command 10:** `cd /Users/dustincheng/projects/data-platform && mkdir -p local/tmp && dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --output name | sort > local/tmp/all-models.txt && ( dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:pre_go --output name; dbt --quiet ls --project-dir dbt --profiles-dir dbt --resource-type model --select tag:post_go --output name ) | sort > local/tmp/tagged-models.txt && echo "models=$(wc -l < local/tmp/all-models.txt | tr -d ' ') tagged=$(wc -l < local/tmp/tagged-models.txt | tr -d ' ') dup=$(uniq -d local/tmp/tagged-models.txt | wc -l | tr -d ' ')" && diff local/tmp/all-models.txt local/tmp/tagged-models.txt && echo TAGS-EXACT`
Exit code: 0
```
models=13 tagged=13 dup=0
TAGS-EXACT
```
Tag check passes with exact partition.

**Command 11:** `cd /Users/dustincheng/projects/data-platform && mkdir -p local/tmp && dbt source freshness --project-dir dbt --profiles-dir dbt --select source:derived > local/tmp/freshness-derived.log 2>&1; echo "freshness-exit=$?"; tail -5 local/tmp/freshness-derived.log`
Exit code: 0
```
freshness-exit=0
[0m19:57:38  1 of 1 START freshness of derived.issue_mentions ............................... [RUN]
[0m19:57:38  1 of 1 PASS freshness of derived.issue_mentions ................................ [[32mPASS[0m in 0.01s]
[0m19:57:38  
[0m19:57:38  Finished running 1 source in 0 hours 0 minutes and 0.06 seconds (0.06s).
[0m19:57:38  Done.
```
Freshness check passes.

**Command 12:** `cd /Users/dustincheng/projects/data-platform && git log --oneline -3 && git status --short`
Exit code: 0
```
f69645a fix(dbt): use buildable indirect test selection for the two-pass build
21000b1 docs(stream): wave 5 evidence for 08-telemetry (accepted); 07 escalated and resumed
d4fdc7f docs(stream): adjudicate 07-go-transform escalation (buildable indirect selection); amend brief
 M docs/fable-streams/2026-09-18-phase-1-build/tasks/07-go-transform/report.md
```

### Summary

All 12 verification commands pass with exit code 0. Key results match expected values:
- GOFMT-OK: gofmt clean
- go vet: clean
- go test: all tests pass (29/29)
- NO-CGO-NO-DUCKDB: CGO disabled, no DuckDB in dependencies
- path-leaks: 0 (no embedded paths or read_parquet in SQL)
- pass1-exit=0, transform-exit=0: both-pass build succeeds with buildable indirect selection
- post_go build: ERROR=0, all 6 items pass (1 model, 5 tests including relationships)
- Derived directory: 142 mention rows, only part-0.parquet
- Drift mart: top row trk-6r2 -> trk-9ob at 6 mentions
- post_go models: exactly mart_issue_mention_drift
- Model tagging: 13 models, 13 tagged, 0 duplicates (TAGS-EXACT)
- Freshness: derived.issue_mentions PASS
- Commit f69645a applied: dbt_project.yml flags block added

**All-pass.**

## adversarial-reviewer — round 2

### Method

Read the brief (both conductor amendments), the full report (implementer round 1, verifier round
1, adversarial-reviewer round 1's `plan_invalidating_discovery` escalation, the "fable — round 2"
ruling, implementer round 2, the out-of-band test-author round-1 section, verifier round 2), then
`git show` on all three commits (af2afbd, 36e0a95, f69645a) and the current file contents. Then
independently re-ran the done-check and a subset of the verification commands rather than trusting
the recorded tails alone.

### Re-review of the round-1 finding

Round 1's blocker (`dbt build --select tag:pre_go` exits 1 because dbt-core 1.12.5's default eager
indirect selection sweeps the Conductor-amendment `relationships` test into pass 1) was upheld by
the conductor's "fable — round 2" ruling, which widened File scope to permit exactly the fix the
round-1 escalation identified (`flags: {indirect_selection: buildable}` in `dbt/dbt_project.yml`).
The round-2 implementer applied that one-line fix in `f69645a` (7 insertions, 0 deletions, no other
hunk — confirmed below) and re-ran the previously-failing command. I independently confirm the fix
resolves the finding; not re-raising it.

### Scope audit

```
$ git show --name-only --pretty=format: af2afbd 36e0a95 f69645a | sort -u
cmd/transform/doc.go
cmd/transform/main.go
dbt/dbt_project.yml
dbt/models/marts/post_go/_post_go.yml
dbt/models/marts/post_go/mart_issue_mention_drift.sql
dbt/models/sources.yml
internal/transform/catalog/catalog_test.go
internal/transform/catalog/catalog.go
internal/transform/catalog/doc.go
internal/transform/doc.go
internal/transform/mentions/doc.go
internal/transform/mentions/extract_test.go
internal/transform/mentions/extract.go
internal/transform/mentions/mentions_test.go
internal/transform/mentions/mentions.go
internal/transform/transform_test.go
internal/transform/transform.go
```
Exactly the brief's Create list plus the two Modify entries (`sources.yml`, and `dbt_project.yml`
per the round-2 amendment). `go.mod`, `go.sum`, `internal/telemetry`, `internal/dbt` are untouched
by any of the three commits (confirmed those files' only history is task 08's own `82f4c35`). The
`dbt_project.yml` diff is exactly one `flags:` block with a comment, nothing else:
```
$ git show f69645a -- dbt/dbt_project.yml | tail -10
+# Buildable indirect selection: the post_go relationships test depends on both a pre_go model
+# (dim_issues) and the post_go mart it's declared on. Default "eager" selection would sweep it
+# into `--select tag:pre_go` before the mart exists; "buildable" only runs a test once all of its
+# parents are in the selected build, so it runs in the post_go pass instead.
+flags:
+  indirect_selection: buildable
```
`sources.yml`'s diff (af2afbd) is purely additive (new `derived` source block); the existing
`beads` source has no changes. No scope breach.

### Done-check re-run (independent)

```
$ cd /Users/dustincheng/projects/data-platform && go test -count=1 ./internal/transform/... ./cmd/transform/... && rm -rf warehouse && mkdir -p warehouse/marts && dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go >/tmp/rev2_pass1.log 2>&1 && go run ./cmd/transform issue_mentions --window 2026-09-18 && dbt build --project-dir dbt --profiles-dir dbt --select tag:post_go >/tmp/rev2_pass2.log 2>&1 && ls warehouse/marts/mart_issue_mention_drift/*.parquet && echo DONE; echo "overall-exit=$?"
ok  	github.com/DMokong/data-platform/internal/transform	0.435s
ok  	github.com/DMokong/data-platform/internal/transform/catalog	0.771s
ok  	github.com/DMokong/data-platform/internal/transform/mentions	1.182s
?   	github.com/DMokong/data-platform/cmd/transform	[no test files]
transform=issue_mentions window=2026-09-18 rows=142 path=derived/issue_mentions/dt=2026-09-18/part-0.parquet
warehouse/marts/mart_issue_mention_drift/data_0.parquet
DONE
overall-exit=0
```
`DONE` prints, `overall-exit=0`. Pass-1/pass-2 tails confirm `ERROR=0` on both:
```
$ tail -3 /tmp/rev2_pass1.log
Done. PASS=56 WARN=0 ERROR=0 SKIP=0 NO-OP=0 REUSED=0 TOTAL=56
$ tail -3 /tmp/rev2_pass2.log
Done. PASS=6 WARN=0 ERROR=0 SKIP=0 NO-OP=0 REUSED=0 TOTAL=6
```
This corroborates the implementer's round-2 report and the verifier round-2 record exactly (same
`PASS=56`/`PASS=6` counts, same conductor-predicted numbers from the "fable — round 2" section) —
independent confirmation, not a stale-cwd or transcription artifact.

Also re-ran a sample of the other verification commands directly: `GOFMT-OK`, `go vet` clean,
`NO-CGO-NO-DUCKDB`, `path-leaks: 0`, no stray `transform` binary at the repo root (`-o /dev/null`
build), `models=13 tagged=13 dup=0` / `TAGS-EXACT`, `freshness-exit=0`. All match the report.

### AC-by-AC walk

- **AC-28 (transform contract).** `internal/transform/transform.go` declares `Contract{Name,
  Inputs, Output, Grain, Tests}`, `Env`, `Transform`, `Materialise` exactly per the brief's
  Required content (`internal/transform/transform.go:18-55`). `catalog.go`'s `All()`/`Lookup()`
  register `mentions.Transform{}` explicitly (no `init()`). `catalog_test.go`'s
  `TestAll_ListsIssueMentionsWithExactContract_AC28` checks `Inputs=["dim_issues"]`,
  `Output="issue_mentions"`, `Grain=window.Day`, and that `Tests` contains both literal strings the
  brief specifies. Satisfied.
- **AC-29 (mention extraction).** `mentions/extract.go`'s `mentionPattern`/`extractMentions` build
  the exact `\b(<alts>)-[0-9a-z]+(?:\.[0-9]+)*\b` pattern, case-sensitive, via `regexp.QuoteMeta`'d
  prefixes; self-mentions dropped via `selfID` comparison; unknown prefixes structurally never
  match (alternation built only from known prefixes). `extract_test.go` table-drives punctuation/
  boundaries, child id, hyphenated-suffix exclusion, duplicate counts, case-sensitivity, unknown
  prefix, empty text. `mentions_test.go` covers same-id-in-two-fields and null fields at the
  `Transform.Run` level. Satisfied.
- **AC-30 (Parquet in, bronze/derived out).** `go run ./cmd/transform issue_mentions --window
  2026-09-18` reads `warehouse/marts/dim_issues/` via `parquet.NewGenericReader` projecting exactly
  the seven named columns (tolerating a wider real schema, tested by
  `TestRun_ToleratesExtraDimIssuesColumns_AC30`) and writes through the shared Writer
  (`Materialise` → `Writer.Write(ctx, bronze.Derived, ...)`); independently confirmed
  `bronze/derived/issue_mentions/dt=2026-09-18/part-0.parquet`, 142 rows. Re-run overwrite is
  exercised by `TestMaterialise_RerunOverwrites_AC30` (through the shared Writer, whose own
  overwrite semantics are out of this task's scope). Satisfied.
- **AC-31 (declared derived source and post_go mart).** `sources.yml`'s new `derived` source has
  `loaded_at_field: _extracted_at`, the exact `external_location` glob, and 24h/72h freshness.
  `mart_issue_mention_drift.sql` takes the latest `dt` (via `order by dt desc limit 1`, documented
  DuckDB workaround), anti-joins against `stg_beads__dependency_snapshots` edges in both
  directions, grains to `(issue_id, mentioned_issue_id)`. Satisfied.
- **AC-32 (two-pass build exits 0 from a clean warehouse).** Independently re-run above: pass 1
  `ERROR=0`, transform `rows=142`, pass 2 `ERROR=0`, `warehouse/marts/mart_issue_mention_drift/`
  holds `data_0.parquet`. Satisfied — this is the AC the round-1 finding blocked and the round-2
  fix now resolves, confirmed directly rather than by re-trusting the report.

### Everything else checked

- All four `doc.go` files carry the required `// Data-engineering concept:` line
  (`internal/transform/doc.go:7`, `catalog/doc.go:7`, `mentions/doc.go:8`, `cmd/transform/doc.go:6`).
- `dbt/models/marts/post_go/_post_go.yml` declares `unique`/`not_null` on the concatenated key,
  `not_null` on both id columns, and the built-in `relationships` test with `arguments:` nesting
  (not the deprecated flat form), error severity — matches the round-1 Conductor amendment exactly.
- Tracker: `bd show trk-bam.7` shows the story claimed, in progress, round-1 and round-2 comments
  posted with commit SHAs, labels `data-platform, phase-1` only (no `agent`), story not closed.
- Working-directory contract verified independently: `CWD-OK`.
- No untracked stray files beyond this review's own `report.md` edit (`git status --short` shows
  only the report.md modification).

### Verdict rationale

I tried to refute this on scope, on the done-check, on each AC, and on the specific mechanism of
the round-1 finding's fix — all five held up under independent re-execution, not just re-reading
the report. Nothing survived the attack. Finding nothing wrong is a legitimate result here.

```json
{
  "verdict": "pass",
  "findings": [],
  "escalation": null
}
```
