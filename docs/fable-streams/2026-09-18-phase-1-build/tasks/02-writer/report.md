## verifier — round 1

**Command 1: gofmt check**
```
cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/bronze)" && echo GOFMT-OK
```
Exit code: 0
Output:
```
GOFMT-OK
```

**Command 2: go vet**
```
cd /Users/dustincheng/projects/data-platform && go vet ./internal/bronze/...
```
Exit code: 0
Output: (no output)

**Command 3: go test with -v**
```
cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/bronze/... 2>&1 | tail -40
```
Exit code: 0
Output:
```
=== RUN   TestPath_DayBoundary_AC03
--- PASS: TestPath_DayBoundary_AC03 (0.00s)
=== RUN   TestPath_Pure_AC03
--- PASS: TestPath_Pure_AC03 (0.00s)
=== RUN   TestPath_Errors_AC03
=== RUN   TestPath_Errors_AC03/window_fails_Validate:_misaligned_start
=== RUN   TestPath_Errors_AC03/window_fails_Validate:_unknown_grain
=== RUN   TestPath_Errors_AC03/window_fails_Validate:_non-UTC_location
=== RUN   TestPath_Errors_AC03/unknown_zone
=== RUN   TestPath_Errors_AC03/empty_zone
=== RUN   TestPath_Errors_AC03/empty_dataset
=== RUN   TestPath_Errors_AC03/dataset_segment_has_uppercase
=== RUN   TestPath_Errors_AC03/dataset_segment_has_a_hyphen
=== RUN   TestPath_Errors_AC03/dataset_segment_has_a_space
=== RUN   TestPath_Errors_AC03/dataset_segment_is_'..'
=== RUN   TestPath_Errors_AC03/dataset_is_only_'..'
=== RUN   TestPath_Errors_AC03/dataset_segment_is_'.'
=== RUN   TestPath_Errors_AC03/dataset_looks_absolute_(leading_slash,_empty_first_segment)
=== RUN   TestPath_Errors_AC03/dataset_has_a_trailing_slash_(empty_last_segment)
=== RUN   TestPath_Errors_AC03/dataset_has_a_doubled_slash_(empty_middle_segment)
--- PASS: TestPath_Errors_AC03 (0.00s)
    --- PASS: TestPath_Errors_AC03/window_fails_Validate:_misaligned_start (0.00s)
    --- PASS: TestPath_Errors_AC03/window_fails_Validate:_unknown_grain (0.00s)
    --- PASS: TestPath_Errors_AC03/window_fails_Validate:_non-UTC_location (0.00s)
    --- PASS: TestPath_Errors_AC03/empty_zone (0.00s)
    --- PASS: TestPath_Errors_AC03/empty_dataset (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_has_uppercase (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_has_a_hyphen (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_has_a_space (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_is_'..' (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_is_only_'..' (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_is_'.' (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_looks_absolute_(leading_slash,_empty_first_segment) (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_has_a_trailing_slash_(empty_last_segment) (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_has_a_doubled_slash_(empty_middle_segment) (0.00s)
=== RUN   TestPath_NoEscapeSequenceInAcceptedPaths_AC03
--- PASS: TestPath_NoEscapeSequenceInAcceptedPaths_AC03 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/bronze	0.409s
```

**Command 4: CGO_ENABLED=0 go build and no-duckdb check**
```
cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./internal/bronze/... && ! go list -deps ./internal/bronze/... | grep -qi duckdb && echo NO-CGO-NO-DUCKDB
```
Exit code: 0
Output:
```
NO-CGO-NO-DUCKDB
```

**Command 5: sample write test with duckdb verification**
```
cd /Users/dustincheng/projects/data-platform && rm -rf local/tmp/bronze-sample && BRONZE_SAMPLE_OUT=$PWD/local/tmp/bronze-sample go test -count=1 -run TestWriteSampleForInspection ./internal/bronze/ && f=$(find local/tmp/bronze-sample -name '*.parquet') && echo "$f" && duckdb -c "describe select * from read_parquet('$f')" && duckdb -c "select * from read_parquet('$f')"
```
Exit code: 0
Output (last 30 lines):
```
ok  	github.com/DMokong/data-platform/internal/bronze	0.230s
local/tmp/bronze-sample/raw/sample/kinds/dt=2026-09-15/hh=13.parquet
┌────────────────────────────────────────┐
│                Describe                │
│                                        │
│ a_string      varchar                  │
│ an_int64      bigint                   │
│ a_float64     double                   │
│ a_bool        boolean                  │
│ a_timestamp   timestamp                │
│ _extracted_at timestamp with time zone │
│ _window_start timestamp with time zone │
│ _window_end   timestamp with time zone │
│ _source_file  varchar                  │
│ dt            date                     │
└────────────────────────────────────────┘
┌──────────┬──────────┬───────────┬─────────┬─────────────┬──────────────────────────┬──────────────────────────┬──────────────────────────┬──────────────────────────────────────────────┬────────────┐
│ a_string │ an_int64 │ a_float64 │ a_bool  │ a_timestamp │      _extracted_at       │      _window_start       │       _window_end        │                 _source_file                 │     dt     │
│ varchar  │  int64   │  double   │ boolean │  timestamp  │ timestamp with time zone │ timestamp with time zone │ timestamp with time zone │                   varchar                    │    date    │
├──────────┼──────────┼───────────┼─────────┼─────────────┼──────────────────────────┼──────────────────────────┼──────────────────────────┼──────────────────────────────────────────────┼────────────┤
│ NULL     │     NULL │      NULL │ NULL    │ NULL        │ 2026-09-15 23:05:00+10   │ 2026-09-15 23:00:00+10   │ 2026-09-16 00:00:00+10   │ raw/sample/kinds/dt=2026-09-15/hh=13.parquet │ 2026-09-15 │
└──────────┴──────────┴───────────┴─────────┴──────────────┴──────────────────────────┴──────────────────────────┴──────────────────────────┴──────────────────────────────────────────────┴────────────┘
```

**Command 6: go and toolchain directives**
```
cd /Users/dustincheng/projects/data-platform && grep -E '^(go|toolchain) ' go.mod
```
Exit code: 0
Output:
```
go 1.25
```

**Command 7: parquet-go version and git info**
```
cd /Users/dustincheng/projects/data-platform && grep -n 'parquet-go' go.mod && git log --oneline -3 && git status --short
```
Exit code: 0
Output:
```
5:require github.com/parquet-go/parquet-go v0.32.0
11:	github.com/parquet-go/bitpack v1.0.0 // indirect
12:	github.com/parquet-go/jsonlite v1.0.0 // indirect
edc3d83 feat(bronze): deterministic window paths and atomic Parquet writer
6f491cd docs(stream): wave 1 evidence (01-foundation accepted)
9f13608 fix(window): guard Range against unknown Grain to prevent infinite loop
```

## Summary

All 7 verification commands passed. The implementation includes:
- Proper gofmt formatting
- Clean go vet analysis
- All tests passing (path table, write determinism, overwrite, failure modes, type round-trips, zero-row file, provenance columns)
- CGO disabled build with no duckdb dependency
- Sample output with correct schema (Timestamp columns, _extracted_at/_window_start/_window_end as TIMESTAMP WITH TIME ZONE, _source_file as VARCHAR)
- Go version 1.25 with parquet-go v0.32.0 dependency
- Clean working tree (edc3d83 commit is the bronze writer implementation)

## adversarial-reviewer — round 1

**Result.** The code holds up. The done-check, scope, every AC and the AC-08 mutants all pass. There are three
minor findings. One is about the evidence record, one is a gap in input hardening, and one is a repo `.gitignore`
problem that the conductor owns. None of them affects correctness or scope.

**Re-running the done-check myself**, from `/Users/dustincheng/projects/data-platform` on branch `phase-1` (`CWD-OK`):
```
ok  	github.com/DMokong/data-platform/internal/bronze	0.624s
DONE
exit=0
```
- **Other verification commands.** gofmt printed GOFMT-OK and vet printed VET-OK. All 22 top-level tests pass, and the sample test is skipped
  because the env var is unset. The go directive is `go 1.25` with no toolchain line.
- **Tidiness.** `GOTOOLCHAIN=local go mod tidy -diff` exits 0 and `go mod verify` passes.
- **Sample check.** I re-ran it with `BRONZE_SAMPLE_OUT` set to a scratch directory. DESCRIBE shows `a_timestamp timestamp`, the three
  provenance timestamps as `timestamp with time zone`, `_source_file varchar`, and varchar/bigint/double/boolean
  for the other kinds. The row is all NULL. AC-06's `select distinct _source_file, _window_start, _window_end` returns
  `raw/sample/kinds/dt=2026-09-15/hh=13.parquet | 2026-09-15 13:00:00+00 | 2026-09-15 14:00:00+00`.
- **Comparison with the verifier.** These results match the verifier's record, which shows the same instants in the +10 zone. One
  transcription blemish: the verifier's Command 3 tail has `=== RUN .../unknown_zone` but no matching `--- PASS`
  line, and my run shows that subtest passing. The outcome is not in dispute, so this is not an evidence deadlock.

**Wrong-input attack.** I ran these against a scratch copy of the module; nothing was written into the checkout.
- **Large batch.** 300k rows, over several pages, with non-alphabetical column names including `"Alpha Beta"`. Two roots give identical bytes. The file has
  one row group, no key-value metadata, the constant `created_by` and mode 0644. `Result.Bytes` equals the file size. In
  DuckDB, the per-column NULL counts and values in batch order are exact.
- **Special values.** int64 min and max, NaN, ±Inf and -0.0 all round-trip. So do an empty string and an embedded NUL. Timestamps from 1900,
  pre-epoch with sub-µs, and 9999-12-31 23:59:59.999999999 all come back truncated to µs. An `extractedAt` in a +10 zone
  is stored as the UTC instant.
- **Degenerate batches.** A zero-column batch with two rows writes provenance-only rows. A zero-row batch has the full schema and `count(*)=0`.
- **Failure paths.** When the target path is a directory, the rename errors and no `.tmp-*` file is left behind. A panic inside encode still
  removes the temp file.
- **Schema.** `parquet_schema`: `ts` is INT64 with logical `TimestampType(isAdjustedToUTC=0, MICROS)` and no
  converted type. The provenance columns are REQUIRED `TIMESTAMP_MICROS` with `isAdjustedToUTC=1`.
- **Cross-arch.** `GOARCH=amd64` (Rosetta) passes `TestWrite_GoldenBytes_AC04`.

**AC-08 sensitivity.** I ran mutants in scratch. The suite kills every one:

| Mutant | Tests that fail |
|---|---|
| `_window_end`=Start | Provenance, Golden |
| `_source_file`=absolute target | Deterministic, Golden, Provenance |
| `extractedAt` rounded rather than truncated | Provenance |
| clock date in `created_by` | Golden |
| ctx check removed | Cancelled |
| temp not removed | EncodeFailure, Cancelled |
| alphabetical column order | Golden, Overwrite |
| naive→UTC-adjusted `ts` | TypeRoundTrip, Golden |
| `time.Now()` path | Path table, DayBoundary, … |

**AC walk.**
- **AC-03**: `path.go:34-64` and the full error table.
- **AC-04**: two-root SHA-256 equality and inequality, plus a golden hash.
- **AC-05**: `writer.go:119-160` follows MkdirAll → CreateTemp(dir, ".tmp-*") → sync → close →
  `ctx.Err()` → Rename, with deferred removal.
- **AC-06 and AC-07**: verified above.

**Scope.** `edc3d83` touches only `go.mod`, `go.sum` and `internal/bronze/{doc,path,path_test,writer,writer_test}.go`. The only
file-creating calls are `writer.go:124` (CreateTemp) and `writer.go:155` (Rename).

### Findings

1. **minor: the evidence record is missing the test-author and implementer sections.** `report.md` starts at
   `## verifier — round 1` and has no `## implementer — round 1`. That leaves the brief's Report obligation unmet in
   the record: codec rationale, timestamp approach, commands with exit codes, and the SHA. The tracker comment of 2026-09-18 06:41 says
   "Report: tasks/02-writer/report.md ## test-author - round 1", but that section is not in the file either. The file
   looks overwritten rather than appended to. What mitigates this: the verifier's record has the command evidence and the
   SHA, and the codec and timestamp rationale appear as code comments (`writer.go:64-67`, `writer.go:265-282`).
2. **minor: a case variant of a provenance name gets past the collision guard, and DuckDB then shadows the real provenance
   column.** `writer.go:86-91` matches names exactly. A batch column `_EXTRACTED_AT` is accepted, and DuckDB reads the
   file as:
   ```
   │ _EXTRACTED_AT │     _extracted_at_1      │ _window_start ...
   │ x             │ 2026-09-15 23:05:00+10   │ ...
   ```
   So `select _extracted_at` resolves to the batch's value. AC-06's literal "these names" is met, and no current source
   produces such names, so this is hardening (`strings.EqualFold`) rather than a contract failure. Case-insensitive
   duplicates between batch columns are a `record.Validate` concern and outside this task's scope.
3. **minor (conductor-owned, outside this task's file scope): the unanchored `bronze/` in `.gitignore:6` ignores
   `internal/bronze/`.** `git ls-files -ci --exclude-standard` lists all five `internal/bronze/*.go` files as
   tracked-but-ignored. `git check-ignore -v internal/bronze/testdata/x` gives `.gitignore:6:bronze/`, so a new file in
   this package, including the brief's allowed `testdata/`, would be silently left out of commits. The implementer handled this correctly
   within scope by force-adding the files and flagging it in the tracker. The fix is anchoring to `/bronze/`, which belongs to whoever owns
   `.gitignore`. This is not a defect in the Writer.

```json
{
  "verdict": "findings",
  "findings": [
    {"severity": "minor", "summary": "report.md lacks the implementer (and test-author) round-1 sections; the brief's Report obligation is unmet in the record, which looks overwritten rather than appended", "evidence": "report.md begins at '## verifier — round 1'; bd comment 2026-09-18 06:41 cites '## test-author - round 1' in report.md, which is absent"},
    {"severity": "minor", "summary": "Provenance-collision guard is case-sensitive; '_EXTRACTED_AT' is accepted and DuckDB then renames the real provenance column to _extracted_at_1", "evidence": "writer.go:86-91 exact match; scratch probe: DuckDB header '_EXTRACTED_AT | _extracted_at_1 | _window_start ...'"},
    {"severity": "minor", "summary": "Unanchored 'bronze/' in .gitignore ignores internal/bronze/ (conductor-owned, outside task scope; files were force-added)", "evidence": "git ls-files -ci --exclude-standard lists internal/bronze/{doc,path,path_test,writer,writer_test}.go; git check-ignore -v internal/bronze/testdata/x -> .gitignore:6:bronze/"}
  ],
  "escalation": null
}
```

## implementer — round 2

Fix round for the three minor findings in `## adversarial-reviewer — round 1`. Working-directory contract checked
before any write: `CWD-OK` (toplevel `/Users/dustincheng/projects/data-platform`, branch `phase-1`). Tracker:
`bd update trk-bam.2 --claim --actor claudeclaw` → `✓ Updated issue: trk-bam.2`.

**Commits for this task:** `edc3d83` (round 1: the Writer) and **`127a7d07ee924679112ba48e2df00a1286327468`**
(round 2: this fix).

### Finding 1: report obligation missing from the record (addressed by this section)

Reports are append-only, so I did not write a `## implementer — round 1` or `## test-author — round 1` section
after the fact. Writing those would put words in the record for agents that did not write them. The file is
untracked (`?? .../02-writer/report.md`) and has never been committed, so git cannot recover whatever was there
before the verifier's section. The test-author's round-1 summary is still in the trk-bam.2 comment of
2026-09-18 06:41. This section covers the brief's full Report obligation for the task as it stands after round 2.
That is every file the task changed, the codec and why, the timestamp approach, every verification command with
its exit code and tail, and the SHAs.

**Files the task has changed.** `git diff 6f491cd..HEAD --stat`, exit 0:
```
 go.mod                         |  14 +
 go.sum                         |  34 ++
 internal/bronze/doc.go         |  30 ++
 internal/bronze/path.go        |  64 ++++
 internal/bronze/path_test.go   | 223 ++++++++++++
 internal/bronze/writer.go      | 352 +++++++++++++++++++
 internal/bronze/writer_test.go | 768 +++++++++++++++++++++++++++++++++++++++++
 7 files changed, 1485 insertions(+)
```
- `go.mod`, `go.sum` (edc3d83): parquet-go v0.32.0 added under `GOTOOLCHAIN=local`. The directive stays `go 1.25` with no `toolchain` line.
- `internal/bronze/doc.go` (edc3d83): learner doc, including the `// Data-engineering concept: ` line.
- `internal/bronze/path.go`, `internal/bronze/path_test.go` (edc3d83): `Zone`, `Raw`, `Derived`, `Path`, and the AC-03 table.
- `internal/bronze/writer.go` (edc3d83 and **127a7d0**): provenance constants, `Writer`, `Result`, `Write`, atomic `replaceFile`, and the encoder.
- `internal/bronze/writer_test.go` (edc3d83 and **127a7d0**): AC-04 to AC-07 tests and `TestWriteSampleForInspection`.

**This round changed only `internal/bronze/writer.go` and `internal/bronze/writer_test.go`:**
```
$ git show --stat --format='%H%n%s' HEAD
127a7d07ee924679112ba48e2df00a1286327468
fix(bronze): reject provenance column names in any letter case
 internal/bronze/writer.go      | 21 +++++++++++++++++----
 internal/bronze/writer_test.go | 23 ++++++++++++++++++-----
 2 files changed, 35 insertions(+), 9 deletions(-)
```

**Compression codec: ZSTD, `zstd.SpeedDefault`, `Concurrency: 1`, the same for every column (`writer.go` `var codec`).**
I did not change this in round 2. There are three reasons for it:
- **Pure Go.** parquet-go's `compress/zstd` wraps klauspost/compress, which is pure Go. That keeps `CGO_ENABLED=0` and principle 6.
- **Deterministic.** The codec's `Encode` builds its encoder only from our pinned level and concurrency plus fixed options (`WithZeroFrames(true)`, `WithEncoderCRC(false)`), then calls `EncodeAll`. The same page bytes therefore always give the same compressed bytes, and nothing depends on the host. The source, `compress/zstd/zstd.go:56-74` in parquet-go v0.32.0:
  ```
  e, err := zstd.NewWriter(nil,
      zstd.WithEncoderConcurrency(c.concurrency()),
      zstd.WithEncoderLevel(c.level()),
      zstd.WithZeroFrames(true),
      zstd.WithEncoderCRC(false),
  )
  ...
  return e.EncodeAll(src, dst[:0]), nil
  ```
  I pinned `Level` and `Concurrency` explicitly rather than relying on parquet-go's defaults, so a library upgrade that changes a default cannot silently change the bytes.
- **Widely readable.** DuckDB 1.5.5 reads the files, as the sample below shows, and so does every mainstream Parquet reader. ZSTD also suits the text-heavy beads payloads (F3: hourly windows of about 6 MB of text).

The other fixed writer options are `DataPageVersion(1)`, `DefaultEncoding(Plain)`, page statistics on, default buffer and row-group sizes (one row group per file), and a constant `CreatedBy`. There is no key-value metadata and no clock.

**How the timestamp logical types are exact.**
- *Provenance (`_extracted_at`, `_window_start`, `_window_end`).* These are `parquet.Required(parquet.TimestampAdjusted(parquet.Microsecond, true))`, which gives TIMESTAMP(MICROS, isAdjustedToUTC=true). The legacy converted type `TIMESTAMP_MICROS` that parquet-go adds means the same thing. Values are `t.UnixMicro()`, the UTC instant whatever the `Location`, floored to µs, which matches the brief's "truncated to µs".
- *Batch `Timestamp` columns.* These are `parquet.TimestampAdjusted(parquet.Microsecond, false)`, wrapped in `naiveTimestampType`, whose `ConvertedType()` returns nil. The wrapper is needed because in parquet-go v0.32.0, `(*timestampType).ConvertedType()` returns `TIMESTAMP_MICROS` whatever `isAdjustedToUTC` is. That legacy annotation means UTC-adjusted, so it would contradict the logical type for any reader that only knows converted types. From `type_timestamp.go:175-184`:
  ```
  func (t *timestampType) ConvertedType() *deprecated.ConvertedType {
  	switch t.Unit.Value.(type) {
  	case *format.MilliSeconds:
  		return &convertedTypes[deprecated.TimestampMillis]
  	case *format.MicroSeconds:
  		return &convertedTypes[deprecated.TimestampMicros]
  ```
  Values are written by `wallClockMicros(t)`, which rebuilds the wall-clock fields in UTC and calls `UnixMicro`, so no zone conversion is applied.
- *Evidence.* `TestWrite_TypeRoundTrip_AC07` asserts isAdjustedToUTC=false for the batch column and true for provenance. The DuckDB DESCRIBE below shows `timestamp` and `timestamp with time zone`.

### Finding 2: case-sensitive provenance-collision guard (fixed in 127a7d0)

**The fix.** The exact `switch c.Name` became `provenanceCollision(name)`, which matches the four provenance names with
`strings.EqualFold`. The error now names both the offending batch column and the provenance column it collides
with.

**The test.** `TestWrite_RejectsProvenanceNamedColumn_AC06` now runs each provenance name in three forms: exact,
upper case (`_EXTRACTED_AT`) and mixed case (`_Extracted_at`). Each case uses a fresh root and also asserts that
the rejected Write created nothing under Root. The rejection happens before any `MkdirAll`.

I wrote the test first. Run against the round-1 guard, the upper- and mixed-case cases fail, which reproduces the
reviewer's probe. Command `go test -count=1 -run TestWrite_RejectsProvenanceNamedColumn_AC06 ./internal/bronze/`, exit 1, tail:
```
    --- FAIL: TestWrite_RejectsProvenanceNamedColumn_AC06/_WINDOW_END (0.00s)
        writer_test.go:560: Write with an input column named "_WINDOW_END" returned a nil error, want rejection
        writer_test.go:567: rejected Write created 1 entries under Root, want none
    --- FAIL: TestWrite_RejectsProvenanceNamedColumn_AC06/_Window_end (0.01s)
        writer_test.go:560: Write with an input column named "_Window_end" returned a nil error, want rejection
        writer_test.go:567: rejected Write created 1 entries under Root, want none
    --- FAIL: TestWrite_RejectsProvenanceNamedColumn_AC06/_SOURCE_FILE (0.01s)
        writer_test.go:560: Write with an input column named "_SOURCE_FILE" returned a nil error, want rejection
        writer_test.go:567: rejected Write created 1 entries under Root, want none
    --- FAIL: TestWrite_RejectsProvenanceNamedColumn_AC06/_Source_file (0.01s)
        writer_test.go:560: Write with an input column named "_Source_file" returned a nil error, want rejection
        writer_test.go:567: rejected Write created 1 entries under Root, want none
FAIL
FAIL	github.com/DMokong/data-platform/internal/bronze	0.631s
```
(8 FAIL subtests in total: the upper and mixed case of each of the 4 names. The exact-case subtests passed.)

After the fix, the same command with `-v` exits 0, tail:
```
--- PASS: TestWrite_RejectsProvenanceNamedColumn_AC06 (0.01s)
    --- PASS: TestWrite_RejectsProvenanceNamedColumn_AC06/_extracted_at (0.00s)
    --- PASS: TestWrite_RejectsProvenanceNamedColumn_AC06/_EXTRACTED_AT (0.00s)
    --- PASS: TestWrite_RejectsProvenanceNamedColumn_AC06/_Extracted_at (0.00s)
    --- PASS: TestWrite_RejectsProvenanceNamedColumn_AC06/_window_start (0.00s)
    --- PASS: TestWrite_RejectsProvenanceNamedColumn_AC06/_WINDOW_START (0.00s)
    --- PASS: TestWrite_RejectsProvenanceNamedColumn_AC06/_Window_start (0.00s)
    --- PASS: TestWrite_RejectsProvenanceNamedColumn_AC06/_window_end (0.00s)
    --- PASS: TestWrite_RejectsProvenanceNamedColumn_AC06/_WINDOW_END (0.00s)
    --- PASS: TestWrite_RejectsProvenanceNamedColumn_AC06/_Window_end (0.00s)
    --- PASS: TestWrite_RejectsProvenanceNamedColumn_AC06/_source_file (0.00s)
    --- PASS: TestWrite_RejectsProvenanceNamedColumn_AC06/_SOURCE_FILE (0.00s)
    --- PASS: TestWrite_RejectsProvenanceNamedColumn_AC06/_Source_file (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/bronze	0.688s
```

**Valid output is unaffected.** `TestWrite_GoldenBytes_AC04` still passes (full run below), so a valid batch still
encodes to exactly the round-1 bytes. The change only rejects more input.

**Scope note.** I did not add case-insensitive duplicate detection between batch columns (for example `id` next to
`ID`). The reviewer placed that in `record.Validate`, which the brief puts out of scope.

### Finding 3: `.gitignore:6` `bronze/` also ignores `internal/bronze/` (not fixed: outside file scope)

I agree with the finding and did not dispute it. `.gitignore` is not in this brief's File scope, so I left it
alone. This round shows the problem again. `git add -- internal/bronze/writer.go internal/bronze/writer_test.go`
exited 1 with `The following paths are ignored by one of your .gitignore files: internal/bronze`. The commit still
went in cleanly because both files are already tracked and the commit used `git commit ... -- <paths>`. The
problem is still open after round 2:
```
$ git check-ignore -v internal/bronze/testdata/x
.gitignore:6:bronze/	internal/bronze/testdata/x
$ git ls-files -ci --exclude-standard
internal/bronze/doc.go
internal/bronze/path.go
internal/bronze/path_test.go
internal/bronze/writer.go
internal/bronze/writer_test.go
```
The conductor or whoever owns `.gitignore` should anchor it: `bronze/` → `/bronze/`. The test-author's 06:41
comment also suggests `warehouse/` → `/warehouse/`. Until then, any new file under `internal/bronze/` needs
`git add -f`.

### Verification commands (brief's list, run after the fix; tails ≤30 lines)

Done-check. **Exit 0.**
```
$ cd /Users/dustincheng/projects/data-platform && go test -count=1 ./internal/bronze/... && CGO_ENABLED=0 go build ./internal/bronze/... && ! go list -deps ./internal/bronze/... | grep -qi duckdb && echo DONE
ok  	github.com/DMokong/data-platform/internal/bronze	0.337s
DONE
```

1. gofmt. **Exit 0.**
```
$ test -z "$(gofmt -l internal/bronze)" && echo GOFMT-OK
GOFMT-OK
```
2. vet. **Exit 0**, no output.
```
$ go vet ./internal/bronze/...
```
3. Verbose tests. **Exit 0** (the `go test` exit status, read through `pipestatus`). The last 30 of 40 tail lines:
```
=== RUN   TestPath_Errors_AC03/dataset_is_only_'..'
=== RUN   TestPath_Errors_AC03/dataset_segment_is_'.'
=== RUN   TestPath_Errors_AC03/dataset_looks_absolute_(leading_slash,_empty_first_segment)
=== RUN   TestPath_Errors_AC03/dataset_has_a_trailing_slash_(empty_last_segment)
=== RUN   TestPath_Errors_AC03/dataset_has_a_doubled_slash_(empty_middle_segment)
--- PASS: TestPath_Errors_AC03 (0.00s)
    --- PASS: TestPath_Errors_AC03/window_fails_Validate:_misaligned_start (0.00s)
    --- PASS: TestPath_Errors_AC03/window_fails_Validate:_unknown_grain (0.00s)
    --- PASS: TestPath_Errors_AC03/window_fails_Validate:_non-UTC_location (0.00s)
    --- PASS: TestPath_Errors_AC03/unknown_zone (0.00s)
    --- PASS: TestPath_Errors_AC03/empty_zone (0.00s)
    --- PASS: TestPath_Errors_AC03/empty_dataset (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_has_uppercase (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_has_a_hyphen (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_has_a_space (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_is_'..' (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_is_only_'..' (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_is_'.' (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_looks_absolute_(leading_slash,_empty_first_segment) (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_has_a_trailing_slash_(empty_last_segment) (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_has_a_doubled_slash_(empty_middle_segment) (0.00s)
=== RUN   TestPath_NoEscapeSequenceInAcceptedPaths_AC03
--- PASS: TestPath_NoEscapeSequenceInAcceptedPaths_AC03 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/bronze	0.320s
```
The tail above shows only path tests, so here are the top-level results for all 16 tests, from
`go test -count=1 -json ./internal/bronze/`: 15 pass and 1 skip. The skip is `TestWriteSampleForInspection`,
because `BRONZE_SAMPLE_OUT` is unset.
```
pass TestWrite_DeterministicBytes_AC04          pass TestWrite_TypeRoundTrip_AC07
pass TestWrite_GoldenBytes_AC04                 pass TestWrite_ZeroRowBatchWritesFullSchema_AC07
pass TestWrite_RejectsInvalidInputsWithoutWriting   skip TestWriteSampleForInspection
pass TestWrite_OverwriteIsAtomicNotAppend_AC05  pass TestPath_ValidExamples_AC03
pass TestWrite_EncodeFailureLeavesPriorFileIntact_AC05  pass TestPath_DayBoundary_AC03
pass TestWrite_ContextCancelledBeforeRename_AC05        pass TestPath_Pure_AC03
pass TestWrite_ProvenanceColumns_AC06           pass TestPath_Errors_AC03
pass TestWrite_RejectsProvenanceNamedColumn_AC06        pass TestPath_NoEscapeSequenceInAcceptedPaths_AC03
```
4. No-CGO build and no DuckDB in the dependencies. **Exit 0.**
```
$ CGO_ENABLED=0 go build ./internal/bronze/... && ! go list -deps ./internal/bronze/... | grep -qi duckdb && echo NO-CGO-NO-DUCKDB
NO-CGO-NO-DUCKDB
```
5. Sample file and DuckDB inspection. **Exit 0.** The DESCRIBE's ANSI colour codes are stripped here.

The DESCRIBE shows everything the brief expects:
- `a_timestamp` is `timestamp`.
- The three provenance timestamps are `timestamp with time zone`.
- `_source_file` is `varchar`.
- The other kinds are varchar, bigint, double and boolean.
- The row is all NULL.
- `dt` is DuckDB's hive partition column (F4). It is not in the file.
```
ok  	github.com/DMokong/data-platform/internal/bronze	0.234s
local/tmp/bronze-sample/raw/sample/kinds/dt=2026-09-15/hh=13.parquet
│ a_string      varchar                  │
│ an_int64      bigint                   │
│ a_float64     double                   │
│ a_bool        boolean                  │
│ a_timestamp   timestamp                │
│ _extracted_at timestamp with time zone │
│ _window_start timestamp with time zone │
│ _window_end   timestamp with time zone │
│ _source_file  varchar                  │
│ dt            date                     │
│ a_string │ an_int64 │ a_float64 │ a_bool  │ a_timestamp │      _extracted_at       │      _window_start       │       _window_end        │                 _source_file                 │     dt     │
│ NULL     │     NULL │      NULL │ NULL    │ NULL        │ 2026-09-15 23:05:00+10   │ 2026-09-15 23:00:00+10   │ 2026-09-16 00:00:00+10   │ raw/sample/kinds/dt=2026-09-15/hh=13.parquet │ 2026-09-15 │
```
AC-06 DuckDB check on the same file, with the session in UTC. **Exit 0.**
```
$ duckdb -c "SET TimeZone='UTC'; select distinct _source_file, _window_start, _window_end, _extracted_at from read_parquet('$f')"
│ raw/sample/kinds/dt=2026-09-15/hh=13.parquet │ 2026-09-15 13:00:00+00   │ 2026-09-15 14:00:00+00   │ 2026-09-15 13:05:00+00   │
```
6. go and toolchain directives. **Exit 0.** The directive is `go 1.25` and there is no `toolchain` line.
```
$ grep -E '^(go|toolchain) ' go.mod
go 1.25
```
7. parquet-go version and git state. **Exit 0.** The only untracked file is this report, which is never committed.
```
$ grep -n 'parquet-go' go.mod && git log --oneline -3 && git status --short
5:require github.com/parquet-go/parquet-go v0.32.0
11:	github.com/parquet-go/bitpack v1.0.0 // indirect
12:	github.com/parquet-go/jsonlite v1.0.0 // indirect
127a7d0 fix(bronze): reject provenance column names in any letter case
edc3d83 feat(bronze): deterministic window paths and atomic Parquet writer
6f491cd docs(stream): wave 1 evidence (01-foundation accepted)
?? docs/fable-streams/2026-09-18-phase-1-build/tasks/02-writer/report.md
```

**Tracker.** Posted `bd comment trk-bam.2 --actor claudeclaw "round 2 (implementer): ..."` →
`✓ Comment added to trk-bam.2`. I did not close the story or change its labels.

**Disputes:** none. **Escalation:** none.

## verifier — round 2

**Command 1: gofmt check**
```
cd /Users/dustincheng/projects/data-platform && test -z "$(gofmt -l internal/bronze)" && echo GOFMT-OK
```
Exit code: 0
Output:
```
GOFMT-OK
```

**Command 2: go vet**
```
cd /Users/dustincheng/projects/data-platform && go vet ./internal/bronze/...
```
Exit code: 0
Output: (no output)

**Command 3: go test with -v**
```
cd /Users/dustincheng/projects/data-platform && go test -count=1 -v ./internal/bronze/... 2>&1 | tail -40
```
Exit code: 0
Output (last 30 lines):
```
=== RUN   TestPath_DayBoundary_AC03
--- PASS: TestPath_DayBoundary_AC03 (0.00s)
=== RUN   TestPath_Pure_AC03
--- PASS: TestPath_Pure_AC03 (0.00s)
=== RUN   TestPath_Errors_AC03
=== RUN   TestPath_Errors_AC03/window_fails_Validate:_misaligned_start
=== RUN   TestPath_Errors_AC03/window_fails_Validate:_unknown_grain
=== RUN   TestPath_Errors_AC03/window_fails_Validate:_non-UTC_location
=== RUN   TestPath_Errors_AC03/unknown_zone
=== RUN   TestPath_Errors_AC03/empty_zone
=== RUN   TestPath_Errors_AC03/empty_dataset
=== RUN   TestPath_Errors_AC03/dataset_segment_has_uppercase
=== RUN   TestPath_Errors_AC03/dataset_segment_has_a_hyphen
=== RUN   TestPath_Errors_AC03/dataset_segment_has_a_space
=== RUN   TestPath_Errors_AC03/dataset_segment_is_'..'
=== RUN   TestPath_Errors_AC03/dataset_is_only_'..'
=== RUN   TestPath_Errors_AC03/dataset_segment_is_'.'
=== RUN   TestPath_Errors_AC03/dataset_looks_absolute_(leading_slash,_empty_first_segment)
=== RUN   TestPath_Errors_AC03/dataset_has_a_trailing_slash_(empty_last_segment)
=== RUN   TestPath_Errors_AC03/dataset_has_a_doubled_slash_(empty_middle_segment)
--- PASS: TestPath_Errors_AC03 (0.00s)
    --- PASS: TestPath_Errors_AC03/window_fails_Validate:_misaligned_start (0.00s)
    --- PASS: TestPath_Errors_AC03/window_fails_Validate:_unknown_grain (0.00s)
    --- PASS: TestPath_Errors_AC03/window_fails_Validate:_non-UTC_location (0.00s)
    --- PASS: TestPath_Errors_AC03/unknown_zone (0.00s)
    --- PASS: TestPath_Errors_AC03/empty_zone (0.00s)
    --- PASS: TestPath_Errors_AC03/empty_dataset (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_has_uppercase (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_has_a_hyphen (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_has_a_space (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_is_'..' (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_is_only_'..' (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_segment_is_'.' (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_looks_absolute_(leading_slash,_empty_first_segment) (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_has_a_trailing_slash_(empty_last_segment) (0.00s)
    --- PASS: TestPath_Errors_AC03/dataset_has_a_doubled_slash_(empty_middle_segment) (0.00s)
=== RUN   TestPath_NoEscapeSequenceInAcceptedPaths_AC03
--- PASS: TestPath_NoEscapeSequenceInAcceptedPaths_AC03 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/bronze	0.425s
```

**Command 4: CGO_ENABLED=0 go build and no-duckdb check**
```
cd /Users/dustincheng/projects/data-platform && CGO_ENABLED=0 go build ./internal/bronze/... && ! go list -deps ./internal/bronze/... | grep -qi duckdb && echo NO-CGO-NO-DUCKDB
```
Exit code: 0
Output:
```
NO-CGO-NO-DUCKDB
```

**Command 5: sample write test with duckdb verification**
```
cd /Users/dustincheng/projects/data-platform && rm -rf local/tmp/bronze-sample && BRONZE_SAMPLE_OUT=$PWD/local/tmp/bronze-sample go test -count=1 -run TestWriteSampleForInspection ./internal/bronze/ && f=$(find local/tmp/bronze-sample -name '*.parquet') && echo "$f" && duckdb -c "describe select * from read_parquet('$f')" && duckdb -c "select * from read_parquet('$f')"
```
Exit code: 0
Output (last 30 lines):
```
ok  	github.com/DMokong/data-platform/internal/bronze	0.238s
local/tmp/bronze-sample/raw/sample/kinds/dt=2026-09-15/hh=13.parquet
┌────────────────────────────────────────┐
│                Describe                │
│                                        │
│ a_string      varchar                  │
│ an_int64      bigint                   │
│ a_float64     double                   │
│ a_bool        boolean                  │
│ a_timestamp   timestamp                │
│ _extracted_at timestamp with time zone │
│ _window_start timestamp with time zone │
│ _window_end   timestamp with time zone │
│ _source_file  varchar                  │
│ dt            date                     │
│ a_string │ an_int64 │ a_float64 │ a_bool  │ a_timestamp │      _extracted_at       │      _window_start       │       _window_end        │                 _source_file                 │     dt     │
│ varchar  │  int64   │  double   │ boolean │  timestamp  │ timestamp with time zone │ timestamp with time zone │ timestamp with time zone │                   varchar                    │    date    │
├──────────┼──────────┼───────────┼─────────┼─────────────┼──────────────────────────┼──────────────────────────┼──────────────────────────┼──────────────────────────────────────────────┼────────────┤
│ NULL     │     NULL │      NULL │ NULL    │ NULL        │ 2026-09-15 23:05:00+10   │ 2026-09-15 23:00:00+10   │ 2026-09-16 00:00:00+10   │ raw/sample/kinds/dt=2026-09-15/hh=13.parquet │ 2026-09-15 │
└──────────┴──────────┴───────────┴─────────┴─────────────┴──────────────────────────┴──────────────────────────┴──────────────────────────┴──────────────────────────────────────────────┴────────────┘
```

**Command 6: go and toolchain directives**
```
cd /Users/dustincheng/projects/data-platform && grep -E '^(go|toolchain) ' go.mod
```
Exit code: 0
Output:
```
go 1.25
```

**Command 7: parquet-go version and git info**
```
cd /Users/dustincheng/projects/data-platform && grep -n 'parquet-go' go.mod && git log --oneline -3 && git status --short
```
Exit code: 0
Output:
```
5:require github.com/parquet-go/parquet-go v0.32.0
11:	github.com/parquet-go/bitpack v1.0.0 // indirect
12:	github.com/parquet-go/jsonlite v1.0.0 // indirect
127a7d0 fix(bronze): reject provenance column names in any letter case
edc3d83 feat(bronze): deterministic window paths and atomic Parquet writer
6f491cd docs(stream): wave 1 evidence (01-foundation accepted)
?? docs/fable-streams/2026-09-18-phase-1-build/tasks/02-writer/report.md
```

**Summary.** All 7 verification commands passed. The implementation continues to meet all requirements:
- Proper gofmt formatting (GOFMT-OK)
- Clean go vet analysis
- All tests passing, including the case-insensitive provenance column rejection tests from round 2
- CGO disabled build with no duckdb dependency (NO-CGO-NO-DUCKDB)
- Sample output with correct schema verified by DuckDB
- Go version 1.25 with parquet-go v0.32.0 dependency
- Clean working tree with parquet-go dependency in go.mod


## adversarial-reviewer — round 2

**Result: pass.** The round-2 change (`127a7d0`, only `internal/bronze/writer.go` and `writer_test.go`) fixes
finding 2, and my own re-run matches the verifier's round-2 record. I have no new findings.

**Done-check, re-run by me** from `/Users/dustincheng/projects/data-platform` on `phase-1` (`CWD-OK`). Exit 0:
```
ok  	github.com/DMokong/data-platform/internal/bronze	0.410s
DONE
exit=0
```
- **Other checks.** gofmt printed GOFMT-OK and vet printed VET-OK. The go directive is `go 1.25` with no toolchain line.
- **Test count.** `go test -json` shows 16 top-level tests: 15 pass, and `TestWriteSampleForInspection` is skipped. There are 39 subtests. This matches the implementer's table. The "22 top-level tests" in my round-1 section was a miscount.
- **Sample.** I re-ran it into a scratch root. DESCRIBE shows `a_timestamp timestamp`, the three provenance columns as `timestamp with time zone`, and `_source_file varchar`. The row is all NULL. With `TimeZone='UTC'` the row reads `13:05:00+00 | 13:00:00+00 | 14:00:00+00 | raw/sample/kinds/dt=2026-09-15/hh=13.parquet`.

**Attacking the fix.** I ran mutants on a `git archive HEAD` copy in scratch; nothing was written into the checkout.
- **Mutant 1: exact-match guard restored.** 9 `--- FAIL` lines, which is 8 subtests plus the parent. The upper- and mixed-case cases fail on both the nil-error assertion and the nothing-under-Root assertion. This matches the implementer's red run.
- **Mutant 2: guard disabled.** 12 subtests fail.
- **Guard order.** The guard runs at `writer.go:100-104`, before `Path` and `replaceFile`, so a rejected batch creates no directory. The test checks this with `os.ReadDir(root)`.
- **Golden bytes.** `TestWrite_GoldenBytes_AC04` still passes, so valid batches encode exactly as in round 1.
- **Unicode folding.** `strings.EqualFold` also treats U+017F `ſ` as `s`, so it would reject `_ſource_file`. Rejecting too much is harmless here, and every ASCII case variant DuckDB would collide on is caught.

**What happened to each round-1 finding**
- **Finding 1 (report obligation): dropped.** The `## implementer — round 2` section meets the brief's Report obligation. It lists the files, the codec and the reasons for it (with parquet-go source cited), the timestamp logical-type approach (with source cited), every command with its exit code and tail, and both SHAs. The implementer was right not to write round-1 sections for other agents in an append-only record. The lost test-author text is still in the trk-bam.2 comment of 06:41. I confirmed the round-2 tracker comment of 07:07 with `bd comments`.
- **Finding 2 (case-sensitive guard): fixed.** Verified above.
- **Finding 3 (`.gitignore:6` `bronze/`): not re-raised against this task.** The implementer agreed with it, and it is outside the task's File scope. I am passing it to the conductor. I checked every path named in the stream's briefs with `git check-ignore`, and the only unintended hit is `internal/bronze/` (the `dbt/target/` and `dbt/logs/` hits are intended). My round-1 wording also needs a correction: the brief requires explicit `git add -- <path>`, and that fails loudly on an ignored path (the implementer's round-2 run exited 1). So a new file would be refused, not silently dropped. It is still worth anchoring the pattern to `/bronze/`.

**Scope.** `git diff --stat 6f491cd..HEAD` shows only `go.mod`, `go.sum` and `internal/bronze/{doc,path,path_test,writer,writer_test}.go`. The working tree has no changes to tracked files. The commit message has the `Stream-task: ... round 2` trailer and the Co-Authored-By trailer.

```json
{
  "verdict": "pass",
  "findings": [],
  "escalation": null
}
```

## test-author — round 1

> Persisted by the conductor on 2026-09-19 from this agent's returned text (journal of workflow run wf_b2881e4a-491). The harness blocked the agent from creating report.md. The text below is verbatim; it is appended after later rounds because it arrived out of band.

**Working-directory contract.** Verified before any write: `git rev-parse --show-toplevel` = `/Users/dustincheng/projects/data-platform`, `git rev-parse --abbrev-ref HEAD` = `phase-1`. No mismatch.

**Mode.** Mode A (fresh tests, no implementation yet). `internal/bronze/` did not exist before this round.

**Framework/conventions detected.** `internal/window/window_test.go` and `internal/record/record_test.go` (task 01-foundation) set the convention: plain `go test`, no third-party framework, table-driven `t.Run` subtests, `Test<Thing>_AC<NN>` names with an AC comment above each, black-box `package <name>_test` where the surface is fully exported. I followed this. AC-05 requires "an encode failure injected through an unexported test seam," which needs same-package access, so `writer_test.go` is `package bronze` (white-box); `path_test.go` stays `package bronze_test` (black-box, since `Path` is fully exported and pure).

### Tests written

- `/Users/dustincheng/projects/data-platform/internal/bronze/path_test.go` (`package bronze_test`) — AC-03: `TestPath_ValidExamples_AC03` (hh=00, hh=23, daily window, multi-segment dataset, the spec's own example), `TestPath_DayBoundary_AC03` (2026-09-15T23 vs 2026-09-16T00 roll to different `dt`), `TestPath_Pure_AC03` (repeat calls identical), `TestPath_Errors_AC03` (11-case table: misaligned window, unknown grain, unknown/empty zone, empty dataset, uppercase/hyphen/space segment, `..` segment, leading/trailing/doubled slash), `TestPath_NoEscapeSequenceInAcceptedPaths_AC03`.
- `/Users/dustincheng/projects/data-platform/internal/bronze/writer_test.go` (`package bronze`) — AC-04..AC-07 plus the brief's required sample-inspection test: `TestWrite_DeterministicBytes_AC04` (SHA-256 equal across two roots, unequal when only `extractedAt` changes), `TestWrite_OverwriteIsAtomicNotAppend_AC05` (3 rows then 1 row → exactly 1 row, no `.tmp-*`), `TestWrite_EncodeFailureLeavesPriorFileIntact_AC05` (uses the `forceEncodeErr` seam — see hook below), `TestWrite_ContextCancelledBeforeRename_AC05` (pre-cancelled context), `TestWrite_ProvenanceColumns_AC06` (reads back `_extracted_at` truncated to µs from a sub-µs-nanos input, `_window_start`, `_window_end`, `_source_file` == `Result.Path` == `Path(...)`), `TestWrite_RejectsProvenanceNamedColumn_AC06` (one subtest per provenance name), `TestWrite_TypeRoundTrip_AC07` (one column per Kind + NULLs; asserts column order, physical Kind, Optional/Required, String/Timestamp logical types incl. `IsAdjustedToUTC` false-vs-true, and all values), `TestWrite_ZeroRowBatchWritesFullSchema_AC07`, `TestWriteSampleForInspection` (the `BRONZE_SAMPLE_OUT`-gated sample, required content backing the brief's DuckDB verification command).

### Required implementation hook (read before round 2)

`TestWrite_EncodeFailureLeavesPriorFileIntact_AC05` sets a package-level variable `forceEncodeErr` that does not exist yet. It must be declared in a **non-test** file (e.g. `writer.go`) — declaring it only in a `_test.go` file would make `CGO_ENABLED=0 go build ./internal/bronze/...` fail with `undefined: forceEncodeErr`, since non-test files never see test-file identifiers:

```go
// forceEncodeErr, when non-nil, is returned by (*Writer).Write in place of a successful encode,
// as if the Parquet encoder had failed after Batch validation but before (or during) writing
// rows to the temp file. Write must still remove the temp file it had begun (or never create
// one) and leave any existing target file untouched, exactly like any other encode failure
// (AC-05). Always nil in production; only this package's own tests set it, and reset it after.
var forceEncodeErr error
```

Consult it once per `Write` call, at/before the point rows are actually encoded. Full comment is also at the top of `writer_test.go`.

### go.mod / go.sum

Ran `GOTOOLCHAIN=local go get github.com/parquet-go/parquet-go@v0.32.0` (spec F11) so the read-back tests can import `parquet-go` / `parquet-go/format`. Go directive stayed `go 1.25`, no `toolchain` line:

```
$ grep -E '^(go|toolchain) ' go.mod
go 1.25
```

`go.sum` was newly created (no third-party deps existed before this task). **Left uncommitted** — see Blocker below.

### Self-validation: scratch reference implementation, written then deleted, never committed

To rule out a broken harness / vacuous tests, I wrote a throwaway `path.go` + `writer.go` (not `doc.go`) implementing the brief's exact API, ran the suite (all tests passed cleanly first try), then ran deliberate mutations to confirm each test goes red for the right reason, then deleted both scratch files (`internal/bronze/` now holds only the two test files — confirmed via `ls`).

Full pass against the scratch implementation (tail):
```
--- PASS: TestWrite_TypeRoundTrip_AC07 (0.01s)
--- PASS: TestWrite_ZeroRowBatchWritesFullSchema_AC07 (0.00s)
--- SKIP: TestWriteSampleForInspection (0.00s)   // BRONZE_SAMPLE_OUT not set
--- PASS: TestPath_Errors_AC03 (12 subtests, all PASS)
--- PASS: TestPath_NoEscapeSequenceInAcceptedPaths_AC03 (0.00s)
PASS
ok  	github.com/DMokong/data-platform/internal/bronze	0.615s
```

Mutation spot-checks (each reverted immediately after capture):
- Wall-clock metadata leak → `TestWrite_DeterministicBytes_AC04` FAIL: `same batch/window/extractedAt into different roots produced different bytes`.
- `extractedAt` replaced with `time.Now()` → `TestWrite_ProvenanceColumns_AC06` FAIL: `_extracted_at = 2026-09-18 06:37:..., want 2026-09-15 13:05:30.123456 +0000 UTC`.
- Leftover temp file on the `forceEncodeErr` path → `TestWrite_EncodeFailureLeavesPriorFileIntact_AC05` FAIL: `leftover temp file ".tmp-..." in .../dt=2026-09-15`.
- Rows doubled (simulated append bug) → `TestWrite_OverwriteIsAtomicNotAppend_AC05` FAIL: `file has 2 rows after overwrite, want 1`.
- Data `Timestamp` column's unit flipped to `isAdjustedToUTC=true` → `TestWrite_TypeRoundTrip_AC07` FAIL: `column "ts" IsAdjustedToUTC = true, want false`.
- Provenance columns reordered before batch columns → `TestWrite_TypeRoundTrip_AC07` FAIL on every column-order assertion.

A final `go test` after restoring the scratch files (`diff` clean against backup) passed cleanly before deletion.

### Failing-first evidence (current state — package genuinely absent)

```
$ go test -count=1 -v ./internal/bronze/... 2>&1
# github.com/DMokong/data-platform/internal/bronze [github.com/DMokong/data-platform/internal/bronze.test]
internal/bronze/writer_test.go:203:10: undefined: Writer
internal/bronze/writer_test.go:204:47: undefined: Raw
internal/bronze/writer_test.go:208:10: undefined: Writer
internal/bronze/writer_test.go:209:47: undefined: Raw
internal/bronze/writer_test.go:224:47: undefined: Raw
internal/bronze/writer_test.go:240:9: undefined: Writer
internal/bronze/writer_test.go:245:46: undefined: Raw
internal/bronze/writer_test.go:254:46: undefined: Raw
internal/bronze/writer_test.go:281:9: undefined: Writer
internal/bronze/writer_test.go:286:45: undefined: Raw
internal/bronze/writer_test.go:286:45: too many errors
FAIL	github.com/DMokong/data-platform/internal/bronze [build failed]
FAIL
```

This fails for the right reason under Mode A: "missing route/function that the test correctly attempted to call" — every unresolved symbol (`Writer`, `Raw`, and, truncated by `go/types`' 10-error cap, also `Path`, `Zone`, `Derived`, `Result`, `Col{ExtractedAt,WindowStart,WindowEnd,SourceFile}`) is named verbatim in the brief's "Required exported API." The self-validation section above independently proves this isn't a harness bug, since these identical files compiled and passed against a real (scratch) implementation.

`gofmt -l internal/bronze` → clean (no output).

### Blocker: `.gitignore`'s unanchored `bronze/` pattern swallows `internal/bronze/`

```
$ git check-ignore -v internal/bronze/path_test.go internal/bronze/writer_test.go
.gitignore:6:bronze/	internal/bronze/path_test.go
.gitignore:6:bronze/	internal/bronze/writer_test.go
```

`.gitignore` line 6 is bare `bronze/` (no leading `/`), so it matches a directory named `bronze` at **any** depth, not just the top-level local Parquet zone CLAUDE.md and the spec describe. It also matches `internal/bronze/`, this task's entire deliverable directory. Right now `git add -- internal/bronze/path_test.go internal/bronze/writer_test.go` refuses unless forced with `-f`, and the same collision will hit the implementer's `doc.go`/`path.go`/`writer.go` in round 2. `warehouse/` (line 7) has the same latent risk for a hypothetical `internal/warehouse`.

I did not fix this: `.gitignore` is not in this task's file scope (the Modify list names only `go.mod`/`go.sum`), and task 01-foundation's brief explicitly lists "Editing `CLAUDE.md` or `.gitignore`" as out of scope. Flagging per my role's boundary ("stop and flag a scope breach rather than reaching outside") rather than force-adding or editing it myself. Minimal fix for whoever owns `.gitignore`: anchor the two zone entries to the repo root, `bronze/` → `/bronze/` and `warehouse/` → `/warehouse/` (matches CLAUDE.md's own description of them as top-level zones, stops matching same-named subdirectories).

**Consequence for this round:** no `git add`/`git commit` was run. `go.mod` and the newly created `go.sum` are modified/untracked but uncommitted; `internal/bronze/path_test.go` and `internal/bronze/writer_test.go` are on disk, validated, but untracked-and-ignored. Nothing was force-added.

```
$ git status --short
 M go.mod
?? go.sum
```

(`internal/bronze/*_test.go` don't appear — `git status` doesn't list ignored-and-untracked paths by default.)

### Report obligation cross-check

This round's deliverable is tests only; no implementation files were left behind (`ls internal/bronze/` → `path_test.go`, `writer_test.go` only). The `## implementer — round <N>` section, the commit, and the `go build`/`NO-CGO-NO-DUCKDB`/DuckDB sample verification commands are round 2's job, and will need the `.gitignore` blocker resolved first or its `git add` will fail the same way mine would have.

### Tracker

`bd update trk-bam.2 --claim --actor claudeclaw` and `bd comment trk-bam.2 --actor claudeclaw "round 1 (test-author): ..."` both run, recording the same findings and the `.gitignore` blocker.

---

**Files relevant to this round** (all under `/Users/dustincheng/projects/data-platform`):
- `internal/bronze/path_test.go` (new)
- `internal/bronze/writer_test.go` (new)
- `go.mod` (modified, uncommitted — parquet-go v0.32.0 added, `go 1.25` unchanged)
- `go.sum` (new, uncommitted)
- `.gitignore` (NOT modified — flagged as a blocker, see above; line 6 `bronze/` needs `/bronze/`)

## implementer — round 1

> Persisted by the conductor on 2026-09-19 from this agent's returned text (journal of workflow run wf_b2881e4a-491). The harness blocked the agent from creating report.md. The text below is verbatim; it is appended after later rounds because it arrived out of band.

**Status:** done, no escalation. Commit `edc3d831758e1a3d9dc7a4d5604bcf8c84257e59`. The working-directory check printed `CWD-OK`. `trk-bam.2` was claimed before the first edit and has a round-1 comment.

### Files changed
- **`internal/bronze/doc.go` (new):** a learner-facing doc on immutable raw data, window-derived paths and overwrite on re-run. It has a one-line `// Data-engineering concept: ` entry.
- **`internal/bronze/path.go` (new):** `Zone`, `Raw`, `Derived` and `Path` (AC-03). `Path` is pure: it calls `w.Validate()`, accepts only known zones, rejects an empty dataset, and checks every `/` segment against `^[a-z0-9_]+$`. `dt` and `hh` come from `w.Start.UTC()`.
- **`internal/bronze/writer.go` (new):** the `Col*` constants, `Writer`, `Result`, `Write`, and `var forceEncodeErr error`. `Write` runs these steps in order:
  1. `b.Validate()`, then reject provenance-named columns, then `Path`, then reject an empty `Root`.
  2. `MkdirAll`, then `CreateTemp(dir, ".tmp-*")`, then chmod 0644.
  3. Encode, then `Sync`, `Stat` (for `Bytes`), `Close`.
  4. Check `ctx.Err()` immediately before `os.Rename`.
  - Cleanup depends on a `renamed` flag, so any error or panic removes the temp file and leaves the target alone.
  - `forceEncodeErr` is checked after the temp file exists, so the cleanup path really runs in that test.
  - Rows are built as explicit `parquet.Row`s with definition levels; no reflection is used.
  - Column order is kept by an `orderedGroup` node, because `parquet.Group` sorts fields by name. parquet-go uses the same approach internally in `type_variant.go`.
- **`path_test.go` / `writer_test.go`:** the test-author's tests are kept. My additions are under "Test extensions" below.
- **`go.mod` / `go.sum`:**
  - `GOTOOLCHAIN=local go get …parquet-go@v0.32.0` left go.mod unchanged; it was already in that state.
  - `GOTOOLCHAIN=local go mod tidy` only made parquet-go a direct requirement and added go.sum hashes.
  - The directive is still `go 1.25`, there is no `toolchain` line, and `go mod tidy -diff` exits 0.

### Compression codec: ZSTD (`Level: SpeedDefault, Concurrency: 1`)
- **Same bytes on every architecture.** klauspost's zstd encoder is pure Go. Snappy in klauspost v1.17.9 uses a hand-written amd64 encoder (`s2/encode_amd64.go:134-160`) and a Go encoder on arm64. I compared both codecs as arm64 and amd64 (Rosetta) test binaries:
  - zstd output was identical at every size;
  - Snappy differed at 3 of 4 sizes, for example n=20000 gave arm64 `len=2484 db924ce5…` vs amd64 `len=2452 ef9b682a…`.

  Phase 2 moves ingestion to EKS, so a codec that is only deterministic per architecture would weaken AC-04.
- **Much smaller files:** about 3x smaller than Snappy on text (6005 vs ~18250 bytes). That matters for the ~6 MB windows (F3).
- **Readers:** DuckDB, Spark, Trino and Iceberg all read ZSTD Parquet.

Other writer options are also fixed rather than left as library defaults:
- `CreatedBy` is a constant. The library default depends on which binary wrote the file.
- Data page version 1 with PLAIN encoding for every kind, for reader compatibility.
- Statistics on, one row group per file, no key/value metadata, schema root named `bronze`.

### How the timestamp logical types were made exact
- **Batch `Timestamp` columns:** these use `TimestampAdjusted(Microsecond,false)`. parquet-go also attaches the legacy `TIMESTAMP_MICROS` converted type, which means UTC-adjusted and contradicts `isAdjustedToUTC=false`. A wrapper type, `naiveTimestampType`, drops it.
- **Batch `Timestamp` values:** written as raw `Int64Value`s built from the wall-clock fields (`time.Date(...,UTC).UnixMicro()`). This avoids parquet-go's reflection path, which converts non-adjusted timestamps through `time.Local`.
- **Provenance columns:** `TimestampAdjusted(Microsecond,true)`, keeping the `TIMESTAMP_MICROS` converted type. `_extracted_at` is `extractedAt` truncated to µs.

Evidence:
- DuckDB `parquet_schema`: `ts` has `converted_type=NULL` and `isAdjustedToUTC=0`; `_extracted_at` has `TIMESTAMP_MICROS` and `isAdjustedToUTC=1`.
- Every column is ZSTD with PLAIN encoding.
- A wall-clock `23:45:30.123456` reads back unchanged.
- `_extracted_at` input `…30.999999999` reads back as `…30.999999+00`.

### Test extensions
- **`TestWrite_GoldenBytes_AC04`:** the SHA-256 of a fixed write must equal the recorded `ad878300…e8c6`. The same hash came from arm64 and amd64 builds. The existing two-write test cannot catch output that depends on a coarse clock: with a footer-date mutant added, only this new test failed.
- **`TestWrite_RejectsInvalidInputsWithoutWriting`:** a ragged row, a wrong Go type, a duplicate column, an unknown zone, `../events`, a misaligned window, and an empty `Root` (run under `t.Chdir`). Each must return an error and create nothing.
- **Overwrite test:** the window directory must hold exactly one entry.
- **Encode-failure and cancelled-context tests:** a failed first write of a new window must leave neither a target file nor a temp file.
- **Provenance test:** `Result.Bytes` must equal the file size.
- **Path errors:** added a non-UTC window, `..` on its own, and a `.` segment.

I ran four mutants against the final suite; each was caught:

| Mutant | Caught by |
|---|---|
| date written into the footer metadata | `GoldenBytes` |
| no `ctx.Err()` check before rename | `ContextCancelled` |
| no `b.Validate()` call | `RejectsInvalidInputs` |
| temp file not removed on failure | `EncodeFailure` and `ContextCancelled` |

writer.go was restored afterwards and the diff checked.

### Notes (not escalations)
1. **`.gitignore` also ignores the Go package.** The root `.gitignore` pattern `bronze/` matches `internal/bronze/` (`git check-ignore` shows `.gitignore:6:bronze/`), so a plain `git add` refuses these files.
   - I staged exactly the 7 in-scope paths with `git add -f -- <paths>` and committed with `git commit … -- <paths>`.
   - Recommend anchoring the patterns as `/bronze/`, `/warehouse/` and `/local/`. Until then, any new file in `internal/bronze/` also needs `-f`.
   - The AC-44 hygiene check should anchor its grep, e.g. `^bronze/`. Right now `git ls-files | grep -E '^(bronze|warehouse|local|\.beads)/|\.duckdb$'` prints nothing.
2. **Two small additions beyond the brief:** an empty `Root` is rejected (otherwise files land relative to the working directory), and the file mode is set to 0644 (CreateTemp gives 0600).
3. **`go mod tidy`:** this task owns go.mod/go.sum in this wave, so running it here is allowed. Task 03 starts from a tidy module.

### Verification (exit codes; outputs trimmed)
1. `test -z "$(gofmt -l internal/bronze)" && echo GOFMT-OK` → exit 0, prints `GOFMT-OK`.
2. `go vet ./internal/bronze/...` → exit 0, no output.
3. `go test -count=1 -v ./internal/bronze/...` → exit 0.
   - All pass: `DeterministicBytes`, `GoldenBytes`, `RejectsInvalidInputsWithoutWriting`, `OverwriteIsAtomicNotAppend`, `EncodeFailureLeavesPriorFileIntact`, `ContextCancelledBeforeRename`, `ProvenanceColumns`, `RejectsProvenanceNamedColumn`, `TypeRoundTrip`, `ZeroRowBatchWritesFullSchema`, `Path_ValidExamples`, `Path_DayBoundary`, `Path_Pure`, `Path_Errors` (15 subtests), `Path_NoEscapeSequence`.
   - `WriteSampleForInspection` is skipped when `BRONZE_SAMPLE_OUT` is unset.
   - Result: `ok … 0.297s`. Also passes with `GOARCH=amd64` and with `-count=3`.
4. `CGO_ENABLED=0 go build … && ! go list -deps … | grep -qi duckdb` → exit 0, prints `NO-CGO-NO-DUCKDB`.
5. Sample file plus DuckDB → exit 0.
   - File: `local/tmp/bronze-sample/raw/sample/kinds/dt=2026-09-15/hh=13.parquet`.
   - `DESCRIBE`: `a_string` varchar, `an_int64` bigint, `a_float64` double, `a_bool` boolean, `a_timestamp` timestamp; `_extracted_at`, `_window_start`, `_window_end` timestamp with time zone; `_source_file` varchar; plus `dt` date from hive-partition detection.
   - `SELECT` returns one row: the five batch columns are NULL, `_extracted_at` is `2026-09-15 23:05:00+10`, the window is `23:00+10` to `00:00+10`, and `_source_file` is `raw/sample/kinds/dt=2026-09-15/hh=13.parquet`. The +10 is only the DuckDB session display timezone; the instants are 13:05Z, 13:00Z and 14:00Z.
   - The AC-06 query `select distinct _source_file, _window_start, _window_end` returns exactly one row.
6. `grep -E '^(go|toolchain) ' go.mod` → exit 0, prints `go 1.25`. There is no `toolchain` line.
7. `grep -n parquet-go go.mod && git log --oneline -3 && git status --short` → exit 0.
   - go.mod line 5 is `require github.com/parquet-go/parquet-go v0.32.0`.
   - The log starts with `edc3d83 feat(bronze): …`.
   - `git status --short` prints nothing.
- **Done-check** → exit 0, prints `ok … 0.305s` then `DONE`.

### Commit
`edc3d831758e1a3d9dc7a4d5604bcf8c84257e59` contains exactly `go.mod`, `go.sum` and `internal/bronze/{doc,path,writer,path_test,writer_test}.go`. The trailer is `Stream-task: 2026-09-18-phase-1-build/02-writer round 1`. The report is not committed.
