# Gate-5 report — phase 1 build

Reviewed 2026-09-19 by the **Fable 5.1 conductor** (session c5342bce, herdr pane w9:p1), whole-branch review of
`main...phase-1` (HEAD 9f92be0 plus this commit). Phases R–4 and the blinded final audit were run at **opus tier in
emulation mode** by session 2c6992a9 (pane wA:p1). Plan approved by Dustin 2026-09-18T06:01Z; paused 07:34Z and
resumed 14:50Z at his request; every wave after approval ran unattended.

## Verdict

**Accept.** Every claim below marked *verified* was re-established in this session with the command shown, not
read from a report. The branch is ready to deliver; two documentation fixes were made during review and are in this
commit. Delivery is Dustin's call (§ Delivery).

## Verified by the Fable reviewer

| Claim | Evidence (this session) |
|---|---|
| Quality gate | `make check` exit 0: fmt-check, vet, `BEADS_LIVE=1 go test`, `CGO_ENABLED=0` build, doc.go, file-writer, hygiene all OK |
| Tests, uncached | `BEADS_LIVE=1 go test -count=1 ./...` — 14 packages ok, 0 failures (cmd/transform, cmd/worker have no tests) |
| Live end to end | `scripts/live-materialise.sh` PASS in 16 s: MaterialiseSource COMPLETED, 26 windows, 27,478 rows, 6.2 MB; BuildMarts pre_go 56 nodes (12 models success, 44 tests pass), transform `issue_mentions` 142 rows, post_go 6 nodes (1 model, 5 tests); spans MaterialiseWindow=26, FetchWindow=80, WriteWindow=80, DbtBuild=2, RunTransform=1, dbt.model.*=13, dbt.test.*=49 |
| Idempotent re-run (spec requirement, AC-16) | `ingest --from 2026-09-14 --to 2026-09-15` twice: file set identical (76 files); 72 byte-identical (zero-row hourly windows); the 4 daily snapshots differ only in `_extracted_at` — content diff excluding it is 0 rows on comments (235), dependencies (77), issues (106), labels (235) |
| No duplicates after lookback re-runs | bronze events: 694 distinct ids = 694 rows across 81 non-empty windows; Dolt `events` = 694 |
| Snapshot reconciles | latest `issues` snapshot dt=2026-09-18: 141 rows (Dolt 141); 99 closed vs Dolt 101 — the snapshot is AS OF the day before, two closed since |
| Marts | dim_issues 141 · dim_issues_scd2 188 rows, 141 current = 141 issues · fct_issue_events 692 · mart_daily_throughput 18 days · mart_agent_activity_hourly 182 · mart_issue_mention_drift 92 |
| Views-only DuckDB (AC-24) | `warehouse/dev.duckdb` information_schema: 13 VIEW, 0 BASE TABLE |
| Freshness | `dbt source freshness`: 8 of 8 PASS |
| Telemetry leaves the machine (AC-40, F8) | Worker run on the default OTLP exporter (`telemetry otlp`): Alloy `otelcol_receiver_accepted` +607 spans / +654 metric points, `otelcol_exporter_sent` +607 spans / +567 metric points, `send_failed` delta 0 → accepted by the Grafana Cloud OTLP gateway |
| Hygiene | `git ls-files` shows nothing under bronze/, warehouse/, local/, .beads/, dbt/target, dbt/logs, bin/ |
| Principle 6 | `go.mod` has no DuckDB module; no `import "C"`; `CGO_ENABLED=0` build succeeds |
| Code read | Writer: temp file + fsync + rename, provenance-collision guard, `[a-z0-9_]` dataset charset keeps paths under Root. Fetcher: `BeginTx(ReadOnly)`, `AS OF TIMESTAMP(?)`, ORDER BY unique keys, Sydney naive-time quirk (F1) handled in staging not ingestion. Orchestrator: `workflow.Now`, spool claim-check, Schedule every 15 m with overlap skip. dbt: `external_location` with `hive_partitioning` + `union_by_name`, staging views tagged pre_go, marts `external` Parquet, `indirect_selection: buildable` |

## Not verified by the Fable reviewer (assumed from reports)

- The test-adversary results on the Writer (7 survivors hardened, 1 equivalent mutant) and each task's per-round
  reviewer verdicts are taken from `tasks/*/report.md`. The equivalent-mutant ruling (parquet-go zstd `EncodeAll` is
  single-goroutine, so `Concurrency` has no effect on bytes) is plausible and consistent with my byte-identity result,
  but I did not re-run the probe.
- Metrics and traces are **not** confirmed queryable in Grafana Cloud: the UI instance is hibernated ("Click on the
  checkbox to continue loading your instance", HTTP 503 on every API path). The OTLP gateway accepted the data;
  wake the instance and query `{__name__=~"platform_.*"}` and service `data-platform-worker` in Tempo to close this.

## Audit findings versus this review

The blinded audit (opus spec-auditor, 5-voter haiku panel per AC, 231 agents) read 46/46 ACs satisfied and raised 5
refute findings; the opus conductor ruled all 5 non-defects. I concur on all five with my own evidence: AC-41/43
(`make check` green after the voters' scratch packages were removed), AC-45 (`go 1.25.4` in go.mod is the minimum
language version forced by Temporal SDK v1.48.0; toolchain 1.25.5 is what runs), AC-24 (13 views, 0 tables), AC-21
(freshness 8/8).

What the audit did not raise and I found:

1. **Spec §3 overstated `hh=` pruning** (handoff F4). Only the `dt=` directory is a Hive partition; `hh=HH.parquet`
   is a file name. Fixed in `docs/data-platform-spec.md` in this commit.
2. **Stale implementation-hook comments** in `internal/bronze/writer_test.go` (the hooks exist; the Sync seam
   *replaces* `tmp.Sync()` rather than following it). Rewritten in this commit; `go test ./internal/bronze` passes.
3. **`cmd/worker` (229 lines) has no tests**; its `main_test.go` was removed as out of scope in task 06. Follow-up.
4. **Report-contract nits**: `tasks/02-writer/report.md` carries a non-role `## Summary` H2 and three sections stamped
   `## test-breaker — round 1` (one disambiguated as "(breaker-1)"). Evidence-format debt only.
5. **The live script pins `PLATFORM_OTEL_EXPORTER=stdout`** so it can count spans; running it never exercises the
   OTLP path. Fine as a self-check, but the runbook should say so, or the script should accept the default exporter
   and skip step 10 when it does. Follow-up.
6. **Two design notes, not defects**: staging dedupes `events` on `id` (spec's fallback rule, justified by the DST
   fall-back hour under F1); and a zero-row hourly window writes a Parquet file with no `_extracted_at` value, so
   freshness on hourly tables reflects the last *non-empty* window — documented in `sources.yml` with generous
   thresholds. Both are worth a line in the spec when the SNS push source (which makes empty windows routine) lands.

## Escalations

`escalations.md` holds 4 adjudicated rows (02-writer ×2, 07-go-transform, 09-buildmarts), all ruled at opus tier;
none deferred, none reopened here. The 07 escalation was caused by the conductor's own brief amendment and fixed by
`indirect_selection: buildable`; the 09 "deadlock" was a conductor scratch file. Both rulings are correct on the
evidence.

## Conductor tiers (mandatory disclosure)

`conductor_model: opus (claude-opus-5, emulation mode, fable-mode loaded) — phases R-4 and final audit; fable
(claude-fable-5-1) — phase 5 whole-branch review`. Cost as reported by the opus pane: about $260 and 22 h wall
time including the 7-hour pause.

## Delivery

No remote exists. `main` holds only the bootstrap commit; `phase-1` is 37 linear commits on top of it, so a
fast-forward is available. Options for Dustin:

1. **`git merge --ff-only phase-1`** into `main` (recommended: keeps the per-task history, which is the learning
   record). Then create the GitHub repo the module path already names, `github.com/DMokong/data-platform`, and push.
2. Keep `phase-1` unmerged as the working branch.
3. Squash-merge (not recommended for a learning repo; it erases the fix-round history).

Follow-up stories to file under a phase-1.1 epic: `cmd/worker` tests; live script exporter passthrough; the SNS →
SQS → landing-zone push source; empty-window freshness semantics.
