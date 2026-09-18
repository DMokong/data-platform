# 03-beads-fetcher — report

## test-author — round 1

**Working-directory contract.** Verified before any write: `git rev-parse --show-toplevel` =
`/Users/dustincheng/projects/data-platform`, `git rev-parse --abbrev-ref HEAD` = `phase-1`. CWD-OK.

### Framework / conventions detected

Existing tests (`internal/window/window_test.go`, `internal/bronze/writer_test.go`,
`internal/bronze/path_test.go`) use plain `go test` (stdlib `testing`, no third-party test
framework), black-box `package foo_test` by default (white-box only where an unexported seam is
required, documented at the top of the file), table-driven subtests via `t.Run`, and every test
name/comment anchored to an AC id (`_AC01` suffix or a `// AC-nn:` comment immediately above).
I matched that: black-box `package source_test` / `package beads_test` throughout (none needed a
white-box seam), `_ACnn` suffixes plus doc comments citing the exact spec/brief wording, and
file-level header comments explaining scope and any deliberate design decision, the same way
`writer_test.go` documents its `forceEncodeErr` hook.

### Files created (all new; no existing files were modified except go.mod/go.sum below)

- `internal/source/source_test.go` — AC-10 (generic `Source.Table` lookup contract + Shape
  distinctness, using a hand-built `source.Source` rather than `beads.Declaration()`, so this file
  never imports `beads`).
- `internal/source/beads/config_test.go` — AC-09 (`ConfigFromEnv` defaults, overrides, partial
  override, invalid port, unknown timezone; `Config.DSN()` parsed back with the driver's own
  `mysql.ParseDSN` and checked for `tcp`, no TLS, `parseTime=true`, `loc=UTC`,
  `interpolateParams=true`, and that it actually carries `Config.Password`).
- `internal/source/beads/declaration_test.go` — AC-10 (`Declaration()`'s full seven-table content,
  order, grain/lookback/shape per table; per-table `Table()` lookup through the real declaration;
  purity/no-shared-backing-array across calls).
- `internal/source/beads/query_test.go` — AC-11 (events Sydney-wall-time bounds for both brief
  examples including the Sydney DST-start window; `dolt_history_issues` / `dolt_log` UTC bounds),
  AC-12 (all four snapshot tables' exact `AS OF TIMESTAMP(?)` SQL and the day-2026-09-10 `AS OF`
  arg example), unknown-table rejection, purity. Every SQL assertion is an **exact string match**
  against the brief's literal SQL text, not a substring/lowercase-tolerant check.
- `internal/source/beads/schema_test.go` — AC-13 (`DatabaseTypeName()` -> `record.Kind` mapping:
  String group including JSON-stays-String; Int64 group including all five `UNSIGNED ...` prefixed
  forms the brief's "Observed driver behaviour" note calls out; Float64; Timestamp; unknown type ->
  error naming both column and type).
- `internal/source/beads/fetcher_test.go` — AC-14 (read-only tx options seam), a compile-time
  `source.Fetcher` interface-satisfaction assertion, `NewFetcher`/`Close` happy path with no live
  dial required, `NewFetcher` rejecting an unresolvable `EventsTZ`.
- `internal/source/beads/live_test.go` — AC-12/AC-13 live tests, all named `TestLive...`, all
  skipping unless `BEADS_LIVE=1`: closed-hourly-window idempotency (`dolt_history_issues`
  2026-09-02T16), issues-snapshot idempotency + nonzero rows (day 2026-09-10), pre-history snapshot
  error naming table+window (day 2026-08-01, grounded in spec F12's 2026-08-22 floor), the
  events-row-count-vs-independent-`COUNT(*)` check using the brief's own literal oracle SQL and
  bounds, and a known-row `created_at` wall-clock-preservation check against a second,
  independently opened `*sql.DB` that never goes through `BuildQuery`/`Fetch`.

### Two API-surface gaps I filled (flagging, not a blocker)

The brief's "Required content" block pins an **exact** exported API for `Config`, `DSN`,
`Declaration`, `Query`, `BuildQuery`, `Fetcher`, `NewFetcher`, `Fetch`, `Close` — but the "Unit
tests (no DB)" section separately requires two more things to be independently unit-testable that
have no pinned signature: the type-mapping function (schema.go) and "factor the tx options into a
function or variable you can assert on" (fetcher.go, AC-14). Since a unit test needs a concrete
symbol to call, I fixed both gaps with signatures that satisfy the AC wording exactly and
documented the decision at the top of each test file:

- `func ColumnKind(columnName, databaseTypeName string) (record.Kind, error)` in `schema.go` —
  `columnName` exists specifically so the unknown-type error can "name the column and type" per
  the brief's own phrase.
- `func ReadOnlyTxOptions() *sql.TxOptions` in `fetcher.go`, returning the exact options every
  `Fetch` call passes to `BeginTx`.

If the implementer round prefers different names, that is a small, mechanical rename in the test
files, not a shape-of-the-work change — I did not treat this as `blocker: plan`.

### go.mod / go.sum

Ran `GOTOOLCHAIN=local go get github.com/go-sql-driver/mysql@v1.10.1` (brief's own instruction,
spec F11 pin) so `config_test.go` could validate `DSN()` with the driver's real `mysql.ParseDSN`
instead of a hand-rolled string check, and so `live_test.go` could open a second, independent
`*sql.DB` for the AC-13 "independent COUNT(*)" and known-row oracle checks. Then ran
`GOTOOLCHAIN=local go mod tidy`, which promoted it (and `filippo.io/edwards25519`, its only new
transitive dependency) from `// indirect` to a direct requirement, since two test files now import
it directly. `go.mod`'s `go` directive is unchanged (`go 1.25`, no `toolchain` line):

```
$ grep -E '^(go|toolchain) ' go.mod
go 1.25
```

### Failing-first evidence (Mode A: no implementation exists yet)

`internal/source` has zero non-test `.go` files right now (only test files exist; `source.go`,
`beads/config.go`, etc. are the implementer's round). The task's own Done-check command is
`go test`, which correctly reports a build failure for the missing package content (not a parse
error in my test files — see the compile-check below):

```
$ cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/source/... 2>&1 | tail -10
github.com/DMokong/data-platform/internal/source: no non-test Go files in /Users/dustincheng/projects/data-platform/internal/source
github.com/DMokong/data-platform/internal/source/beads: no non-test Go files in /Users/dustincheng/projects/data-platform/internal/source/beads
FAIL	github.com/DMokong/data-platform/internal/source [build failed]
FAIL	github.com/DMokong/data-platform/internal/source/beads [build failed]
FAIL
```

`go vet ./internal/source/...` reports the identical two lines and exits 1 — the same genuine
"the feature does not exist yet" reason, not a harness defect.

**Go quirk worth flagging for whoever reads the verification commands next:** `go build
./internal/source/...` (the wildcard form the brief's own verification block uses) silently
**exits 0** and prints nothing for this same state — Go's `...`-pattern loader treats a
"test-files-only" package as nothing to build rather than an error, unlike `go vet` and `go test`
on the identical packages. Confirmed directly:

```
$ CGO_ENABLED=0 go build ./internal/source/... && echo BUILD-OK
BUILD-OK
```

So `BUILD-OK` alone is **not** evidence anything is implemented; `go test`/`go vet` are the
authoritative pre-implementation signal, and are what the brief's Done-check actually runs.

**Proving the test files themselves are sound** (not the cause of the build failure, and not
vacuous): I copied the whole tree to a scratch directory (outside this checkout, never committed
into it) and added deliberately-wrong stub implementations for every required symbol (e.g.
`Source.Table` always returns `(Table{}, false)`, `ConfigFromEnv` returns a zero `Config` and never
errors, `BuildQuery` returns an empty `Query` and never errors, `ColumnKind` maps everything to
`record.String`, `ReadOnlyTxOptions` returns `ReadOnly: false`). Against that stub:

```
$ cd $SCRATCH_COPY && go build ./internal/source/... && go vet ./internal/source/...
(both exit 0 — my test files compile cleanly against the pinned + gap-filled API)

$ go test -count=1 -v ./internal/source/... 2>&1 | tail -60
=== RUN   TestSourceTable_Found_AC10
    source_test.go:35: Table("events") ok = false, want true
--- FAIL: TestSourceTable_Found_AC10 (0.00s)
...
=== RUN   TestConfigFromEnv_Defaults_AC09
    config_test.go:76: ConfigFromEnv() = {Host: Port:0 ...}, want defaults {Host:127.0.0.1 ...}
--- FAIL: TestConfigFromEnv_Defaults_AC09 (0.00s)
...
=== RUN   TestBuildQuery_EventsBounds_AC11/2026-09-15T03:00Z_(pre-DST)
    query_test.go:75: SQL = "", want "SELECT * FROM events WHERE created_at >= ? AND created_at < ? ORDER BY created_at, id"
--- FAIL: TestBuildQuery_EventsBounds_AC11 (0.00s)
...
=== RUN   TestColumnKind_Int64_AC13/UNSIGNED_BIGINT
    schema_test.go:70: ColumnKind("UNSIGNED BIGINT") = string, want int64
--- FAIL: TestColumnKind_Int64_AC13 (0.00s)
...
=== RUN   TestReadOnlyTxOptions_AC14
    fetcher_test.go:35: ReadOnlyTxOptions().ReadOnly = false, want true
--- FAIL: TestReadOnlyTxOptions_AC14 (0.00s)
...
=== RUN   TestNewFetcher_UnknownEventsTZ
    fetcher_test.go:103: NewFetcher with an unresolvable EventsTZ returned a nil error
--- FAIL: TestNewFetcher_UnknownEventsTZ (0.00s)
FAIL	github.com/DMokong/data-platform/internal/source/beads	0.540s
FAIL	github.com/DMokong/data-platform/internal/source	0.540s
```

Every AC-bearing test failed for a genuine assertion mismatch against the wrong stub (concrete
expected value vs. the stub's placeholder value) — never a panic, never an "undefined symbol",
never a harness error. A handful of tests legitimately passed against this specific stub by
construction (e.g. `TestSourceTable_NotFound_AC10` — the stub always returns not-found, which
happens to satisfy a not-found check; `TestColumnKind_String_AC13` — the stub maps everything to
`String`, which happens to satisfy the String-group check) — their complementary "found" /
"non-String-kind" tests in the same file did fail, so nothing here is vacuous overall. One bug of
mine surfaced in this pass: `TestDeclaration_Idempotent_AC10` originally indexed
`first.Tables[0]` unconditionally, which panicked against a stub with zero tables and aborted the
rest of the `beads` package's test run. I fixed it with an explicit `len(...)==0` guard
(`t.Fatalf` instead of an index panic) before writing this report; the tail above is from the
corrected file, re-run after the fix, confirming no more panic and the same clean
fail-for-the-right-reason results. The stub tree lived only under the session's scratch directory
and was never written into this checkout.

### Other verification commands (informational; not expected to pass pre-implementation)

```
$ gofmt -l internal/source                          # (empty output) GOFMT-OK
$ grep -rnE '(INSERT|UPDATE|DELETE|REPLACE|CREATE|DROP|ALTER|CALL)[[:space:]]' --include='*.go' internal/source | grep -v '_test.go'
write-statement-lines: 0
```

(`query_test.go` contains the literal string `"events; DROP TABLE events"` as a malicious-input
test case for `BuildQuery`'s unknown-table rejection; it is excluded from the grep count because it
lives in a `_test.go` file, matching the brief's own grep filter.)

### Scope

Touched only: `internal/source/source_test.go`, `internal/source/beads/{config,declaration,query,
schema,fetcher,live}_test.go` (all new), `go.mod`, `go.sum` (dependency add per brief instruction).
No implementation file was created or modified. Nothing under `bronze/`, `warehouse/`, `local/`,
`.beads/`, or any `*.duckdb` file was touched. Other tasks' files (e.g. task 02's
`tasks/02-writer/report.md`) were left untouched.

### Tracker

`bd update trk-bam.3 --claim --actor claudeclaw` run before the first edit above.

## implementer — round 1

**Working-directory contract.** Ran before any write: `git rev-parse --show-toplevel` =
`/Users/dustincheng/projects/data-platform`, `git rev-parse --abbrev-ref HEAD` = `phase-1` → `CWD-OK`
(re-checked right before the commit, output `CWD-OK`).

**Tracker.** `bd update trk-bam.3 --claim --actor claudeclaw` → `✓ Updated issue: trk-bam.3` (the
test-author had already claimed it as claudeclaw).

### Starting state: resumed from an interrupted run

`stream.md` records the 2026-09-18 pause as "implementer interrupted/not run; uncommitted internal/source/ +
go.mod/go.sum in tree". The tree already held uncommitted implementation files (`source.go`, `doc.go`,
`beads/{config,declaration,query,schema,fetcher,doc}.go`, all dated 17:30–17:32) plus
`beads/decode_internal_test.go` (17:33). In `live_test.go`, the known-row test's window was already changed to
2026-09-15T11Z, with a comment citing an "implementer round 1 probe, recorded in report.md" that had never been
written. That run left no report section. I did not trust any of it. I re-read every file against the brief,
re-ran every verification command, and re-probed the live server myself (below). Where I kept the earlier
work, it is because the evidence here supports it. The probe that `live_test.go` cites is now recorded in this
section, under "Live data".

### What I changed (all in File scope)

Committed as **`709dec4`**: `feat(source): beads Fetcher with hourly event windows and AS OF snapshots`,
19 paths. `git show --stat HEAD` lists only these:
- `internal/source/{doc.go, source.go}` hold the exact exported API from the brief: `Shape` (`EventLog`,
  `Snapshot`), `Table`, `Source`, `Source.Table`, `Fetcher`. The concept line is `// Data-engineering
  concept: declared extraction contracts (grain, cadence, lookback).`
- `internal/source/beads/doc.go` has the concept line `// Data-engineering concept: extraction from an event
  log plus time-travel snapshots, without transformation.`
- `config.go` defines `Config`, `ConfigFromEnv` (AC-09 env names and defaults; a port outside 1..65535 or not
  an integer is an error, and so is an unknown or empty tz) and `DSN()` (`tcp`, `tls=false`,
  `parseTime=true`, `loc=UTC`, `interpolateParams=true`).
- `declaration.go` defines `Declaration()` (AC-10): 7 tables in the brief's order, a new value on each call.
- `query.go` defines `Query` and `BuildQuery`. The SQL is one literal per declared table, and the table name
  never comes from input. Events bounds are converted to `eventsLoc` wall time; history and `dolt_log` bounds
  stay in UTC. Snapshots use `AS OF TIMESTAMP(?)` with `w.End` in UTC. An unknown table, an invalid window, a
  grain mismatch or a nil loc for events is an error.
- `schema.go` has two parts:
  - `ColumnKind` maps `DatabaseTypeName()` to a kind: String / Int64 (including `UNSIGNED …`) / Float64 /
    Timestamp. Any other type is an error naming the column and type.
  - `decodeValue` turns the wire value into the kind's Go type. NULL becomes nil. A `uint64` above int64's
    range is an error, not a wrap. Timestamps become `time.UTC` with the source wall-clock fields.
- `fetcher.go` defines `Fetcher`, `NewFetcher`, `Close` and `ReadOnlyTxOptions()`, returning
  `&sql.TxOptions{ReadOnly: true}`, which is what `Fetch` passes to `BeginTx`. `Fetch` behaves as follows:
  - It always rolls back and never commits.
  - It maps Dolt error 1146 to an error naming the table, the window and the `AS OF` instant.
  - It runs `Batch.Validate()` before returning.
- **New, `beads/fetcher_internal_test.go` (my addition).** The test-author's AC-14 test checks only the
  return value of `ReadOnlyTxOptions()`; nothing showed that `Fetch` actually uses it. This white-box test
  runs `Fetch` itself against an in-memory `database/sql` driver, with no DB. It asserts:
  - `BeginTx` is called once with `ReadOnly=true` at the default isolation, with 0 commits and 1 rollback;
  - the SQL and args are exactly what `BuildQuery` returns;
  - the decoded batch is correct (`[]byte`, `uint64`, `time.Time` and NULL inputs);
  - a 1146 error comes back naming `issues` and `2026-08-01` and wrapping the driver error;
  - an unknown column type and a `uint64` overflow both produce errors naming the column.

  I checked that the test catches real bugs. Four mutants of `fetcher.go`, fed in through
  `go test -overlay` so the repo copy was never edited, all fail it:
  - `BeginTx(ctx, &sql.TxOptions{})`;
  - `Commit` instead of `Rollback`;
  - `BuildQuery(…, time.UTC)`;
  - dropping the table/window from the error.

  ```
  == mutant 1
  --- FAIL: TestFetch_ReadOnlyTxRunsBuildQueryAndDecodes_AC14 (0.00s)
  --- FAIL: TestFetch_TableNotFoundNamesTableAndWindow_AC12 (0.00s)
  --- FAIL: TestFetch_UnknownColumnTypeErrors_AC13 (0.00s)
  --- FAIL: TestFetch_UndecodableValueErrors (0.00s)
  == mutant 2
  --- FAIL: TestFetch_ReadOnlyTxRunsBuildQueryAndDecodes_AC14 (0.00s)
  == mutant 3
  --- FAIL: TestFetch_ReadOnlyTxRunsBuildQueryAndDecodes_AC14 (0.00s)
  == mutant 4
  --- FAIL: TestFetch_TableNotFoundNamesTableAndWindow_AC12 (0.00s)
  ```
- `beads/decode_internal_test.go` (from the interrupted run, kept) covers the `decodeValue` success and error
  paths, the `BuildQuery` window guards, and the Sydney DST fall-back bounds (2027-04-03T15Z →
  `[02:00, 02:00)`, 16Z → `[02:00, 03:00)`, so every event is fetched exactly once).
- The test-author's files are unchanged, except for the `live_test.go` window edit described above:
  `source_test.go`, `beads/{config,declaration,query,schema,fetcher,live}_test.go`.
- `go.mod` / `go.sum`: the test-author added mysql v1.10.1. Re-running
  `GOTOOLCHAIN=local go get github.com/go-sql-driver/mysql@v1.10.1` was a byte-for-byte no-op, and
  `GOTOOLCHAIN=local go mod tidy -diff` exits 0. The `go` directive is still `go 1.25`, with no `toolchain`
  line. The only new transitive dependency is `filippo.io/edwards25519 v1.2.0`, required by mysql v1.10.1:
  ```
  GO-GET-NOOP
  tidy-diff exit 0
  github.com/go-sql-driver/mysql v1.10.1
  filippo.io/edwards25519 v1.2.0
  github.com/go-sql-driver/mysql@v1.10.1 filippo.io/edwards25519@v1.2.0
  ```

The API additions beyond the pinned block are the test-author's two gap-fills (`ColumnKind`,
`ReadOnlyTxOptions`), which I implemented as specified. Not committed: `report.md`, and anything under
`bronze/`, `warehouse/`, `local/` or `.beads/`.

### Observed `DatabaseTypeName()` per table (live, go-sql-driver/mysql v1.10.1) and the mapping

I ran a read-only scratch probe, compiled into the module through `go run -overlay` with no file written to
the repo. It opened read-only transactions, ran full-table scans (for `dolt_history_issues`, the largest
window, 2026-09-05T03Z with 4,745 rows), and recorded the Go type each value scanned as.

| Table (cols) | DatabaseTypeName → columns | Scanned Go type | Kind |
|---|---|---|---|
| events (8) | CHAR `id`; VARCHAR `issue_id, event_type, actor`; TEXT `old_value, new_value, comment`; DATETIME `created_at` | `[]uint8` / `[]uint8` / `[]uint8` / `time.Time` | String / String / String / Timestamp |
| dolt_history_issues (57) | VARCHAR ×29; TEXT ×7; INT `priority, estimated_minutes, compaction_level, original_size`; DATETIME ×9 incl. `commit_date`; TINYINT `ephemeral, pinned, is_template, no_history, is_blocked`; JSON `metadata`; BIGINT `timeout_ns`; CHAR `commit_hash` | `[]uint8`; `[]uint8`; `int64`; `time.Time`; `int64`; `[]uint8`; `int64`; `[]uint8` | String; String; Int64; Timestamp; **Int64** (not Bool); **String** (JSON as text); Int64; String |
| dolt_log (12) | TEXT ×9 incl. `commit_hash`; DATETIME `date, author_date`; **UNSIGNED BIGINT** `commit_order` | `[]uint8`; `time.Time`; `uint64` | String; Timestamp; Int64 (overflow-checked) |
| issues (54) | same as dolt_history_issues minus `commit_hash, committer, commit_date` | same | same |
| comments (5) | CHAR `id`; VARCHAR `issue_id, author`; TEXT `text`; DATETIME `created_at` | `[]uint8`; `[]uint8`; `[]uint8`; `time.Time` | String; String; String; Timestamp |
| dependencies (10) | CHAR `id`; VARCHAR ×7; DATETIME `created_at`; JSON `metadata` | `[]uint8`; `[]uint8`; `time.Time`; `[]uint8` | String; String; Timestamp; String |
| labels (2) | VARCHAR `issue_id, label` | `[]uint8` | String |

Every type the server serves is in the mapping. None is FLOAT, DOUBLE, DECIMAL or TIMESTAMP, so those branches
are covered only by unit tests. With v1.10.1, integers arrive as `int64` / `uint64`, not text `[]byte`, even
though `interpolateParams` puts the driver in text-protocol mode. The `[]byte` integer branch is therefore
defensive, and is unit-tested. The probe found no zero dates (`0000-00-00`) in any DATETIME column. NULLs occur
in DATETIME, INT, VARCHAR and TEXT, and scan as `nil`.

### Live data: what surprised me or is worth knowing

- **The brief's example events hour is empty.** `events` in 2026-09-15T03Z (Sydney 13:00–14:00) has **0
  rows**. That is why the known-row test uses 2026-09-15T11Z (Sydney 21:00–22:00), which has 2 rows. The
  closed-window idempotency test uses `dolt_history_issues` 2026-09-02T16Z, which has 2,500 rows.
- **Events per UTC hour on 2026-09-15:** T11=2, T12=6, T13=6, T16=3, T17=1, total **18**. The independent
  `COUNT(*)` over `['2026-09-15 10:00:00', '2026-09-16 10:00:00')` is also **18**.
- **`AS OF TIMESTAMP(?)` is a UTC instant on this AEST server** (`@@time_zone`=`SYSTEM`, system tz `AEST`). I
  took issue `trk-ndq`, first committed at 2026-08-21 22:38:04Z. `AS OF` 22:38:03 returns 0 rows, `AS OF`
  22:38:05 returns 1, and `AS OF` 2026-08-22 00:00:00 returns 1. If the literal were read as AEST, midnight
  would mean 14:00Z and return 0. `trk-c9e` and `trk-pl2` behave the same way. Result: `AS OF` of the exact
  commit second returns 0, which suggests commit times carry sub-second precision.
- **The F2 claim reproduces.** A bare literal `AS OF '2026-09-11 00:00:00'` fails with
  `Error 1105 (HY000): string is not a valid branch or hash`.
- **Before history.** For all four snapshot tables on day 2026-08-01, `Fetch` returns
  `beads: fetch issues window 2026-08-01: table issues did not exist yet at 2026-08-02 00:00:00 UTC (the window
  ends before the table's first commit): Error 1146 (HY000): table not found: issues`. The same form appears
  for `comments`, `dependencies` and `labels`.
- **Empty is not the same as missing.** On day 2026-08-21 (AS OF 08-22 00:00), `comments` and `dependencies`
  exist but have **0 rows**. They correctly return an empty batch rather than an error. `issues` has 12 rows
  and `labels` has 4, which matches F12.
- **Row counts today:** events 640, dolt_log 866, issues 127, comments 260, dependencies 99, labels 260. The
  2026-09-10 snapshot has issues 106, comments 234, dependencies 77, labels 235.

### Verification commands (brief's block), each with exit code and output tail

```
$ cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/source)" && echo GOFMT-OK
GOFMT-OK                                   # exit 0

$ go vet ./internal/source/...
                                           # (no output) exit 0

$ go test -count=1 -v ./internal/source/... 2>&1 | tail -30      # exit 0; last 30 lines:
=== RUN   TestColumnKind_Float64_AC13
... (Int64/Float64/Timestamp subtests PASS)
=== RUN   TestColumnKind_UnknownType_AC13
--- PASS: TestColumnKind_UnknownType_AC13 (0.00s)
    --- PASS: TestColumnKind_UnknownType_AC13/BLOB (0.00s)
    --- PASS: TestColumnKind_UnknownType_AC13/GEOMETRY (0.00s)
    --- PASS: TestColumnKind_UnknownType_AC13/BIT (0.00s)
    --- PASS: TestColumnKind_UnknownType_AC13/YEAR (0.00s)
    --- PASS: TestColumnKind_UnknownType_AC13/TIME (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/source/beads	0.644s
# Summary of the same run from a log capture: 37 top-level "--- PASS", 0 "--- FAIL"; the 5 TestLive* SKIP
# (no BEADS_LIVE); "ok .../internal/source 0.244s", "ok .../internal/source/beads 0.399s".

$ BEADS_LIVE=1 go test -count=1 -v -run Live ./internal/source/beads/ 2>&1 | tail -30   # exit 0
=== RUN   TestLiveFetch_ClosedHourlyWindowIdempotent_AC13
--- PASS: TestLiveFetch_ClosedHourlyWindowIdempotent_AC13 (0.97s)
=== RUN   TestLiveFetch_IssuesSnapshotIdempotent_AC12
--- PASS: TestLiveFetch_IssuesSnapshotIdempotent_AC12 (0.00s)
=== RUN   TestLiveFetch_SnapshotBeforeFirstCommitErrors_AC12
--- PASS: TestLiveFetch_SnapshotBeforeFirstCommitErrors_AC12 (0.00s)
=== RUN   TestLiveFetch_EventsRowCountMatchesIndependentCount_AC13
--- PASS: TestLiveFetch_EventsRowCountMatchesIndependentCount_AC13 (0.01s)
=== RUN   TestLiveFetch_EventsCreatedAtStaysSourceWallTime_AC13
--- PASS: TestLiveFetch_EventsCreatedAtStaysSourceWallTime_AC13 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/source/beads	1.247s

$ grep -rnE '(INSERT|UPDATE|DELETE|REPLACE|CREATE|DROP|ALTER|CALL)[[:space:]]' --include='*.go' internal/source | grep -v '_test.go' ; echo "write-statement-lines: ..."
write-statement-lines: 0                   # exit 0

$ CGO_ENABLED=0 go build ./internal/source/... && echo BUILD-OK
BUILD-OK                                   # exit 0

$ grep -E '^(go|toolchain) ' go.mod
go 1.25                                    # exit 0; no toolchain line

$ grep -n 'go-sql-driver/mysql' go.mod && git log --oneline -3 && git status --short     # exit 0
6:	github.com/go-sql-driver/mysql v1.10.1
709dec4 feat(source): beads Fetcher with hourly event windows and AS OF snapshots
80fa4a6 chore(gitignore): anchor local-state patterns to the repo root
e6ec31a docs(stream): pause wave 2 at user request
?? docs/fable-streams/2026-09-18-phase-1-build/tasks/02-writer/report.md
?? docs/fable-streams/2026-09-18-phase-1-build/tasks/03-beads-fetcher/report.md
?? docs/fable-streams/2026-09-18-phase-1-build/tasks/04-runner/report.md
... (05–10 report.md, all untracked, none mine)
```

`80fa4a6` (gitignore) is a sibling commit that landed during this round. It is not mine, and `709dec4`
contains only the 19 paths listed above.

Done-check:
```
$ cd /Users/dustincheng/projects/data-platform && go test -count=1 ./internal/source/... && BEADS_LIVE=1 go test -count=1 -run Live ./internal/source/beads/ && echo DONE
ok  	github.com/DMokong/data-platform/internal/source	0.236s
ok  	github.com/DMokong/data-platform/internal/source/beads	0.386s
ok  	github.com/DMokong/data-platform/internal/source/beads	1.043s
DONE
```

### Notes for the reviewer (no escalation)

- `BuildQuery` rejects more than an unknown table. It also rejects an invalid window, a window whose grain
  differs from the table's declared grain, and a nil `eventsLoc` for `events`. That is stricter than the
  brief's "unknown table → error", but it is still pure, and it matches what task 04's Runner will pass
  (windows from `window.Lookback`/`Range` at the table's grain).
- At the DST fall-back, events land in one UTC window, not two. The 15Z window gets an empty range and the 16Z
  window gets the doubled local hour. Staging's `id` dedupe (AC-22) stays harmless either way.

**Escalations:** none.

## verifier — round 1

**Verification commands (brief's block), each with exit code and output tail:**

**1.** `cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/source)" && echo GOFMT-OK`
   - Exit code: 0
   - Output:
     ```
     GOFMT-OK
     ```

**2.** `cd /Users/dustincheng/projects/data-platform && go vet ./internal/source/...`
   - Exit code: 0
   - Output: (no output)

**3.** `cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/source/... 2>&1 | tail -30`
   - Exit code: 0
   - Output (last 30 lines):
     ```
     === RUN   TestColumnKind_Float64_AC13
     === RUN   TestColumnKind_Float64_AC13/FLOAT
     === RUN   TestColumnKind_Float64_AC13/DOUBLE
     --- PASS: TestColumnKind_Float64_AC13 (0.00s)
         --- PASS: TestColumnKind_Float64_AC13/FLOAT (0.00s)
         --- PASS: TestColumnKind_Float64_AC13/DOUBLE (0.00s)
     === RUN   TestColumnKind_Timestamp_AC13
     === RUN   TestColumnKind_Timestamp_AC13/DATETIME
     === RUN   TestColumnKind_Timestamp_AC13/TIMESTAMP
     --- PASS: TestColumnKind_Timestamp_AC13 (0.00s)
         --- PASS: TestColumnKind_Timestamp_AC13/DATETIME (0.00s)
         --- PASS: TestColumnKind_Timestamp_AC13/TIMESTAMP (0.00s)
     === RUN   TestColumnKind_UnknownType_AC13
     === RUN   TestColumnKind_UnknownType_AC13/BLOB
     === RUN   TestColumnKind_UnknownType_AC13/GEOMETRY
     === RUN   TestColumnKind_UnknownType_AC13/BIT
     === RUN   TestColumnKind_UnknownType_AC13/YEAR
     === RUN   TestColumnKind_UnknownType_AC13/TIME
     --- PASS: TestColumnKind_UnknownType_AC13 (0.00s)
         --- PASS: TestColumnKind_UnknownType_AC13/BLOB (0.00s)
         --- PASS: TestColumnKind_UnknownType_AC13/GEOMETRY (0.00s)
         --- PASS: TestColumnKind_UnknownType_AC13/BIT (0.00s)
         --- PASS: TestColumnKind_UnknownType_AC13/YEAR (0.00s)
         --- PASS: TestColumnKind_UnknownType_AC13/TIME (0.00s)
     PASS
     ok  	github.com/DMokong/data-platform/internal/source/beads	0.459s
     ```

**4.** `cd /Users/dustincheng/projects/data-platform && BEADS_LIVE=1 go test -count=1 -v -run Live ./internal/source/beads/ 2>&1 | tail -30`
   - Exit code: 0
   - Output (last 30 lines):
     ```
     === RUN   TestLiveFetch_ClosedHourlyWindowIdempotent_AC13
     --- PASS: TestLiveFetch_ClosedHourlyWindowIdempotent_AC13 (0.74s)
     === RUN   TestLiveFetch_IssuesSnapshotIdempotent_AC12
     --- PASS: TestLiveFetch_IssuesSnapshotIdempotent_AC12 (0.01s)
     === RUN   TestLiveFetch_SnapshotBeforeFirstCommitErrors_AC12
     --- PASS: TestLiveFetch_SnapshotBeforeFirstCommitErrors_AC12 (0.00s)
     === RUN   TestLiveFetch_EventsRowCountMatchesIndependentCount_AC13
     --- PASS: TestLiveFetch_EventsRowCountMatchesIndependentCount_AC13 (0.01s)
     === RUN   TestLiveFetch_EventsCreatedAtStaysSourceWallTime_AC13
     --- PASS: TestLiveFetch_EventsCreatedAtStaysSourceWallTime_AC13 (0.00s)
     PASS
     ok  	github.com/DMokong/data-platform/internal/source/beads	0.939s
     ```

**5.** `cd /Users/dustincheng/projects/data-platform && grep -rnE '(INSERT|UPDATE|DELETE|REPLACE|CREATE|DROP|ALTER|CALL)[[:space:]]' --include='*.go' internal/source | grep -v '_test.go' ; echo "write-statement-lines: $(grep -rnE '(INSERT|UPDATE|DELETE|REPLACE|CREATE|DROP|ALTER|CALL)[[:space:]]' --include='*.go' internal/source | grep -vc '_test.go')"`
   - Exit code: 0
   - Output:
     ```
     write-statement-lines: 0
     ```

**6.** `cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./internal/source/... && echo BUILD-OK`
   - Exit code: 0
   - Output:
     ```
     BUILD-OK
     ```

**7.** `cd /Users/dustincheng/projects/data-platform && grep -E '^(go|toolchain) ' go.mod`
   - Exit code: 0
   - Output:
     ```
     go 1.25
     ```

**8.** `cd /Users/dustincheng/projects/data-platform && grep -n 'go-sql-driver/mysql' go.mod && git log --oneline -3 && git status --short`
   - Exit code: 0
   - Output:
     ```
     6:	github.com/go-sql-driver/mysql v1.10.1
     709dec4 feat(source): beads Fetcher with hourly event windows and AS OF snapshots
     80fa4a6 chore(gitignore): anchor local-state patterns to the repo root
     e6ec31a docs(stream): pause wave 2 at user request
     ?? docs/fable-streams/2026-09-18-phase-1-build/tasks/02-writer/report.md
     ?? docs/fable-streams/2026-09-18-phase-1-build/tasks/03-beads-fetcher/report.md
     ?? docs/fable-streams/2026-09-18-phase-1-build/tasks/04-runner/report.md
     ?? docs/fable-streams/2026-09-18-phase-1-build/tasks/05-dbt-project/report.md
     ?? docs/fable-streams/2026-09-18-phase-1-build/tasks/06-temporal-ingest/report.md
     ?? docs/fable-streams/2026-09-18-phase-1-build/tasks/07-go-transform/report.md
     ?? docs/fable-streams/2026-09-18-phase-1-build/tasks/08-telemetry/report.md
     ?? docs/fable-streams/2026-09-18-phase-1-build/tasks/09-buildmarts/report.md
     ?? docs/fable-streams/2026-09-18-phase-1-build/tasks/10-integration/report.md
     ```

**Summary:** All eight verification commands passed with exit code 0. Formatting is correct (GOFMT-OK), all tests pass including live tests against the beads server (BEADS_LIVE=1), no write statements detected (write-statement-lines: 0), build succeeds with CGO disabled (BUILD-OK), Go version is correctly set to 1.25, and the mysql driver is at the pinned version v1.10.1.

## adversarial-reviewer — round 1

**Working-directory contract.** `git rev-parse --show-toplevel` = `/Users/dustincheng/projects/data-platform`,
branch `phase-1` → `CWD-OK` before this append.

**Scope audit.** `git show --stat 709dec4` lists 19 paths: `go.mod`, `go.sum`, `internal/source/{doc,source,source_test}.go`
and `internal/source/beads/*.go` (the brief's Create list, plus `*_test.go` files the brief allows). No report.md, `bronze/`, `warehouse/`,
`local/`, `.beads/` or `*.duckdb` file is included. `.gitignore` changed in sibling commit `80fa4a6` (02-writer fix), not in this task's commit.
The commit message follows the brief's format. `go.mod` diff: + `go-sql-driver/mysql v1.10.1`, + `filippo.io/edwards25519 v1.2.0 // indirect`.
`go 1.25`, no `toolchain` line. `GOTOOLCHAIN=local go mod tidy -diff` exits 0.

**Done-check re-run (independent).** It agrees with the verifier's record:
```
ok  	github.com/DMokong/data-platform/internal/source	0.156s
ok  	github.com/DMokong/data-platform/internal/source/beads	0.234s
ok  	github.com/DMokong/data-platform/internal/source/beads	0.890s
DONE
exit=0
```
I also re-ran the verification commands: GOFMT-OK, vet rc=0, `write-statement-lines: 0`, BUILD-OK, `go 1.25`, mysql v1.10.1.
With `BEADS_LIVE=1 -v`, all 5 `TestLive*` show `--- PASS`, none SKIP. Without it, only the 5 live tests SKIP. The Sydney-tz unit tests run and are not skipped.

**Wrong-input and live attacks.** I ran a read-only scratch test through `go test -overlay` from the session scratchpad. It wrote nothing into
the checkout (`git status --short internal/` was empty afterwards).
```
events: summed hourly fetch=640 over 696 windows, independent COUNT=640      # 2026-08-21..09-18, Sydney bounds 10:00→10:00
dolt_log: summed hourly fetch=867 over 696 windows, independent COUNT=867    # live UNSIGNED BIGINT path, no dup commit_hash
events created_at raw-text check: 18 rows, 0 mismatches                       # vs CAST(created_at AS CHAR), Location==UTC
events 2026-09-15T03 rows=0 cols=8                                            # confirms implementer's "brief example hour is empty"
dolt_history_issues 2026-09-02T16 rows=2500 cols=57                           # idempotency window is non-empty
snapshot {issues,comments,dependencies,labels} 2026-08-20: ERR ... did not exist yet at 2026-08-21 00:00:00 UTC ...: Error 1146
snapshot issues/comments/dependencies/labels 2026-08-21: rows=12/0/0/4        # F12 floor: empty ≠ missing
snapshot ... 2026-09-19 (open day, AS OF future): rows=127/261/99/260         # latest state, no error
attack hour window on snapshot table  -> err=beads: table issues is declared with grain day, got a hour window ...
attack day window on events           -> err=beads: table events is declared with grain hour, got a day window ...
attack zero window                    -> err=beads: table events: window: unknown grain 0
attack unknown table                  -> err=beads: unknown table "issues; DROP TABLE x"
attack system table not declared      -> err=beads: unknown table "dolt_branches"
attack case variant                   -> err=beads: unknown table "Events"
attack non-UTC start                  -> err=beads: table events: window: Start location is x, want time.UTC
--- PASS: TestZZReviewProbe (1.71s)
```
Driver check: `fields.go` in go-sql-driver/mysql v1.10.1 adds the `UNSIGNED ` prefix only to TINYINT/SMALLINT/MEDIUMINT/INT/BIGINT.
Every non-binary name it can report (CHAR, VARCHAR, TEXT, TINYTEXT, MEDIUMTEXT, LONGTEXT, JSON, ENUM, SET, DECIMAL, FLOAT,
DOUBLE, DATETIME, TIMESTAMP) is in `ColumnKind` (schema.go:17-28). Every other name (BLOB, BINARY, DATE, TIME, YEAR, BIT, GEOMETRY and so on) errors, as the brief requires.
With the default isolation, `BeginTx` sends only `START TRANSACTION READ ONLY` (connection.go:148). No session variable is set.

**AC walk.**
- **AC-09:** config.go:43-63. Defaults and overrides are tested, and a bad port or tz errors. The DSN is checked with `mysql.ParseDSN`. No real credential is committed; the test passwords are fixtures.
- **AC-10:** declaration.go:14-28 gives 7 tables in order, the grains, lookbacks 24/2, shapes, and 15m.
- **AC-11:** query.go:33-46 and :71-80. Exact SQL, and all three brief examples are pinned in query_test.go:57-64 and :104.
- **AC-12:** `AS OF TIMESTAMP(?)` with `w.End` in UTC. Fetching the same day twice gives equal batches (live). 1146 maps to an error naming the table and window (fetcher.go:76-79), live for all 4 tables.
- **AC-13:** unique ORDER BY keys. Columns come from `ColumnTypes()`. TINYINT maps to Int64 and JSON to String. `created_at` is not shifted (my raw-text check above).
- **AC-14:** `ReadOnlyTxOptions()` goes to `BeginTx`. Rollback is always used and commit never is. A fake-driver test asserts this at the driver boundary. The write grep finds 0 lines.

**Evidence audit.** Every claim in the implementer's report has command output next to it, and the claims I spot-checked hold:
- the empty 03Z hour;
- the 18-event day;
- the 1146 message text;
- empty-vs-missing on 2026-08-21.

The round-1 `bd comment` on trk-bam.3 is present (2026-09-18 14:57).

**Steelman.** The implementer resumed from an interrupted, undocumented run. They re-verified that work instead of trusting it, and added a
fake-driver test that closes a real gap: the test-author's AC-14 test checked only the options value, not that `Fetch` uses it. They
also mutation-tested that new test. The stricter `BuildQuery` guards (grain mismatch, invalid window, nil loc) stay pure and reject bad
input the brief never named. I found no defect.

```json
{
  "verdict": "pass",
  "findings": [],
  "escalation": null
}
```
