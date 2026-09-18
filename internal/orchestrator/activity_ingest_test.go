package orchestrator_test

// Behavioral tests for internal/orchestrator's activities -- AC-33 in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md and the "Activities" section of
// tasks/06-temporal-ingest/brief.md: FetchWindow (Fetcher -> spool file), WriteWindow (spool ->
// bronze via bronze.Writer), and DeleteSpool (idempotent cleanup).
//
// Every test drives the real Activities methods through
// go.temporal.io/sdk/testsuite.TestActivityEnvironment against a fake source.Fetcher and temp
// directories, then inspects only externally observable state: the spool file's existence on
// disk, and the resulting bronze Parquet file's bytes and rows (read back with parquet-go's
// public reader -- see helpers_test.go's readParquetRows). No unexported orchestrator symbol is
// referenced anywhere in this file.
//
// REQUIRED IMPLEMENTATION NOTE: these tests construct Activities directly (Sources, Fetchers,
// Writer, SpoolDir, Now) and call its FetchWindow/WriteWindow/DeleteSpool methods as bound values
// (a.FetchWindow etc.) through TestActivityEnvironment.ExecuteActivity, exactly as the brief's
// "Required content" signatures pin them. Nothing here depends on how MaterialiseWindow later
// invokes these activities by name (see workflow_materialise_test.go for that).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/orchestrator"
	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/source"
	"github.com/DMokong/data-platform/internal/source/beads"
	"github.com/DMokong/data-platform/internal/window"
)

// fixedNow is the extraction clock every test below pins Activities.Now to, so _extracted_at and
// SpoolRef.FetchedAt assertions compare against a known value instead of wall-clock time.Now().
var fixedNow = time.Date(2026, 9, 15, 13, 5, 30, 0, time.UTC)

// sampleBatch is a small, valid two-row batch for tests that don't care about type coverage.
// TestSpoolRoundTrip_AllKindsAndNull_AC33 below is the one that covers every record.Kind and NULL.
func sampleBatch() record.Batch {
	return record.Batch{
		Columns: []record.Column{
			{Name: "id", Kind: record.Int64},
			{Name: "title", Kind: record.String},
		},
		Rows: [][]any{
			{int64(1), "first"},
			{int64(2), nil},
		},
	}
}

// newTestActivities builds an *orchestrator.Activities wired to a fake Fetcher (keyed "beads"), a
// bronze.Writer rooted at a fresh temp dir, a spool dir under the same temp root, and a fixed
// extraction clock. It returns the Activities, the fetcher (so a test can assert on its calls),
// the bronze root and the spool dir.
func newTestActivities(t *testing.T, batch record.Batch, fetchErr error, now time.Time) (a *orchestrator.Activities, fetcher *stubFetcher, bronzeRoot, spoolDir string) {
	t.Helper()
	root := t.TempDir()
	spoolDir = filepath.Join(root, "local", "spool")
	bronzeRoot = filepath.Join(root, "bronze")
	fetcher = &stubFetcher{batch: batch, err: fetchErr}
	a = &orchestrator.Activities{
		Sources:  map[string]source.Source{"beads": beads.Declaration()},
		Fetchers: map[string]source.Fetcher{"beads": fetcher},
		Writer:   &bronze.Writer{Root: bronzeRoot},
		SpoolDir: spoolDir,
		Now:      func() time.Time { return now },
	}
	return a, fetcher, bronzeRoot, spoolDir
}

// newTestActivityEnv builds a TestActivityEnvironment with a's FetchWindow, WriteWindow and
// DeleteSpool methods registered -- TestActivityEnvironment.ExecuteActivity panics ("no activity
// is registered for taskqueue ...") if the registry it drives is empty, even when called with a
// bound method value directly, so every test below goes through this constructor rather than
// calling suite.NewTestActivityEnvironment() on its own.
func newTestActivityEnv(t *testing.T, a *orchestrator.Activities) *testsuite.TestActivityEnvironment {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(a.FetchWindow)
	env.RegisterActivity(a.WriteWindow)
	env.RegisterActivity(a.DeleteSpool)
	return env
}

// mustFetch runs FetchWindow through a TestActivityEnvironment and decodes its SpoolRef, failing
// the test on any error.
func mustFetch(t *testing.T, env *testsuite.TestActivityEnvironment, a *orchestrator.Activities, in orchestrator.FetchWindowInput) orchestrator.SpoolRef {
	t.Helper()
	val, err := env.ExecuteActivity(a.FetchWindow, in)
	if err != nil {
		t.Fatalf("FetchWindow: %v", err)
	}
	var ref orchestrator.SpoolRef
	if err := val.Get(&ref); err != nil {
		t.Fatalf("decode SpoolRef: %v", err)
	}
	return ref
}

// AC-33: FetchWindow stamps FetchedAt from Activities.Now, fetches exactly the requested
// table/window from the Fetcher (once, not zero or twice), and returns a SpoolRef whose Rows
// matches what the Fetcher returned, whose Source/Table/Window match the input, whose Path names
// a file that exists on disk under SpoolDir.
func TestFetchWindow_ProducesSpoolRefAndFile_AC33(t *testing.T) {
	batch := sampleBatch()
	a, fetcher, _, spoolDir := newTestActivities(t, batch, nil, fixedNow)
	w := mustWindow(t, 2026, 9, 15, 13, window.Hour)
	in := orchestrator.FetchWindowInput{Source: "beads", Table: "dolt_history_issues", Window: w}

	env := newTestActivityEnv(t, a)
	ref := mustFetch(t, env, a, in)

	if ref.Source != "beads" {
		t.Errorf("ref.Source = %q, want %q", ref.Source, "beads")
	}
	if ref.Table != "dolt_history_issues" {
		t.Errorf("ref.Table = %q, want %q", ref.Table, "dolt_history_issues")
	}
	if ref.Window != w {
		t.Errorf("ref.Window = %v, want %v", ref.Window, w)
	}
	if ref.Rows != len(batch.Rows) {
		t.Errorf("ref.Rows = %d, want %d", ref.Rows, len(batch.Rows))
	}
	if !ref.FetchedAt.Equal(fixedNow) {
		t.Errorf("ref.FetchedAt = %v, want %v (Activities.Now)", ref.FetchedAt, fixedNow)
	}
	if ref.Path == "" {
		t.Fatalf("ref.Path is empty")
	}
	if !filepathHasPrefix(ref.Path, spoolDir) {
		t.Errorf("ref.Path = %q, want it under SpoolDir %q", ref.Path, spoolDir)
	}
	if _, err := os.Stat(ref.Path); err != nil {
		t.Errorf("spool file %s does not exist: %v", ref.Path, err)
	}
	if got := fetcher.callCount(); got != 1 {
		t.Errorf("Fetcher was called %d times, want exactly 1", got)
	}
}

// filepathHasPrefix reports whether path lies inside the directory prefix, using filepath.Rel so
// it is robust to trailing slashes and "." segments (a plain strings.HasPrefix would not be).
func filepathHasPrefix(path, prefix string) bool {
	rel, err := filepath.Rel(prefix, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// AC-33: WriteWindow reads the spool file FetchWindow produced and writes it to bronze via
// bronze.Writer.Write; the resulting file's _extracted_at column equals the SpoolRef's FetchedAt
// on every row, and the row count matches what was fetched.
func TestFetchThenWrite_BronzeExtractedAtMatchesFetchedAt_AC33(t *testing.T) {
	batch := sampleBatch()
	a, _, bronzeRoot, _ := newTestActivities(t, batch, nil, fixedNow)
	w := mustWindow(t, 2026, 9, 15, 13, window.Hour)
	in := orchestrator.FetchWindowInput{Source: "beads", Table: "dolt_log", Window: w}

	env := newTestActivityEnv(t, a)
	ref := mustFetch(t, env, a, in)

	writeVal, err := env.ExecuteActivity(a.WriteWindow, ref)
	if err != nil {
		t.Fatalf("WriteWindow: %v", err)
	}
	var result bronze.Result
	if err := writeVal.Get(&result); err != nil {
		t.Fatalf("decode bronze.Result: %v", err)
	}
	if result.Rows != len(batch.Rows) {
		t.Errorf("bronze.Result.Rows = %d, want %d", result.Rows, len(batch.Rows))
	}

	path := filepath.Join(bronzeRoot, result.Path)
	rows := readParquetRows(t, path)
	if len(rows) != len(batch.Rows) {
		t.Fatalf("bronze file has %d rows, want %d", len(rows), len(batch.Rows))
	}
	for i, row := range rows {
		v, ok := row[bronze.ColExtractedAt]
		if !ok {
			t.Fatalf("row %d has no %s column", i, bronze.ColExtractedAt)
		}
		got := timestampMicros(v)
		if !got.Equal(ref.FetchedAt) {
			t.Errorf("row %d: %s = %v, want %v (ref.FetchedAt)", i, bronze.ColExtractedAt, got, ref.FetchedAt)
		}
	}
}

// AC-33: the spool file FetchWindow wrote survives WriteWindow (so a retried WriteWindow whose
// earlier completion was lost still finds it), DeleteSpool then removes it, and a second
// DeleteSpool on the now-missing file still succeeds -- "a missing file counts as success".
func TestDeleteSpool_SurvivesWriteThenIdempotentlyRemoves_AC33(t *testing.T) {
	batch := sampleBatch()
	a, _, _, _ := newTestActivities(t, batch, nil, fixedNow)
	w := mustWindow(t, 2026, 9, 15, 13, window.Hour)
	in := orchestrator.FetchWindowInput{Source: "beads", Table: "events", Window: w}

	env := newTestActivityEnv(t, a)
	ref := mustFetch(t, env, a, in)

	if _, err := env.ExecuteActivity(a.WriteWindow, ref); err != nil {
		t.Fatalf("WriteWindow: %v", err)
	}
	if _, err := os.Stat(ref.Path); err != nil {
		t.Fatalf("spool file gone after WriteWindow; it must survive until DeleteSpool runs: %v", err)
	}

	if _, err := env.ExecuteActivity(a.DeleteSpool, ref); err != nil {
		t.Fatalf("first DeleteSpool: %v", err)
	}
	if _, err := os.Stat(ref.Path); !os.IsNotExist(err) {
		t.Errorf("spool file still present after DeleteSpool (stat err = %v)", err)
	}

	if _, err := env.ExecuteActivity(a.DeleteSpool, ref); err != nil {
		t.Errorf("second DeleteSpool on an already-missing file returned %v, want nil (idempotent per AC-33)", err)
	}
}

// AC-33: a retried WriteWindow (the spool file is still present because DeleteSpool has not run)
// produces byte-identical bronze output the second time, since it derives everything from the
// same SpoolRef / FetchedAt and the same batch on disk.
func TestWriteWindow_RetriedIsByteIdentical_AC33(t *testing.T) {
	batch := sampleBatch()
	a, _, bronzeRoot, _ := newTestActivities(t, batch, nil, fixedNow)
	w := mustWindow(t, 2026, 9, 15, 13, window.Hour)
	in := orchestrator.FetchWindowInput{Source: "beads", Table: "dolt_history_issues", Window: w}

	env := newTestActivityEnv(t, a)
	ref := mustFetch(t, env, a, in)

	val1, err := env.ExecuteActivity(a.WriteWindow, ref)
	if err != nil {
		t.Fatalf("first WriteWindow: %v", err)
	}
	var r1 bronze.Result
	if err := val1.Get(&r1); err != nil {
		t.Fatalf("decode: %v", err)
	}

	val2, err := env.ExecuteActivity(a.WriteWindow, ref)
	if err != nil {
		t.Fatalf("second (retried) WriteWindow: %v", err)
	}
	var r2 bronze.Result
	if err := val2.Get(&r2); err != nil {
		t.Fatalf("decode: %v", err)
	}

	h1 := sha256File(t, filepath.Join(bronzeRoot, r1.Path))
	h2 := sha256File(t, filepath.Join(bronzeRoot, r2.Path))
	if h1 != h2 {
		t.Errorf("retried WriteWindow produced different bytes: %s vs %s", h1, h2)
	}
}

// AC-33: every record.Kind, including an all-NULL row and a Timestamp's wall-clock fields, round
// trips losslessly from the Fetcher's Batch, through the spool file FetchWindow writes, to the
// bronze Parquet file WriteWindow produces. This is the spool encoding's correctness contract:
// any lossy or type-erasing choice (e.g. text-only encoding without type tags, or collapsing
// NULL/zero-value together) would surface here as a changed value or a changed NULL-ness on
// read-back.
func TestSpoolRoundTrip_AllKindsAndNull_AC33(t *testing.T) {
	seenAt := time.Date(2026, 9, 15, 13, 45, 30, 123456000, time.UTC) // whole microseconds only
	batch := record.Batch{
		Columns: []record.Column{
			{Name: "id", Kind: record.Int64},
			{Name: "title", Kind: record.String},
			{Name: "score", Kind: record.Float64},
			{Name: "active", Kind: record.Bool},
			{Name: "seen_at", Kind: record.Timestamp},
		},
		Rows: [][]any{
			{int64(42), "hello", 3.5, true, seenAt},
			{nil, nil, nil, nil, nil},
		},
	}
	a, _, bronzeRoot, _ := newTestActivities(t, batch, nil, fixedNow)
	w := mustWindow(t, 2026, 9, 15, 13, window.Hour)
	in := orchestrator.FetchWindowInput{Source: "beads", Table: "dolt_log", Window: w}

	env := newTestActivityEnv(t, a)
	ref := mustFetch(t, env, a, in)

	writeVal, err := env.ExecuteActivity(a.WriteWindow, ref)
	if err != nil {
		t.Fatalf("WriteWindow: %v", err)
	}
	var result bronze.Result
	if err := writeVal.Get(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}

	rows := readParquetRows(t, filepath.Join(bronzeRoot, result.Path))
	if len(rows) != 2 {
		t.Fatalf("bronze file has %d rows, want 2", len(rows))
	}

	full := rows[0]
	if v, ok := full["id"]; !ok || v.IsNull() || v.Int64() != 42 {
		t.Errorf("id = %v, want 42", full["id"])
	}
	if v, ok := full["title"]; !ok || v.IsNull() || string(v.ByteArray()) != "hello" {
		t.Errorf("title = %v, want %q", full["title"], "hello")
	}
	if v, ok := full["score"]; !ok || v.IsNull() || v.Double() != 3.5 {
		t.Errorf("score = %v, want 3.5", full["score"])
	}
	if v, ok := full["active"]; !ok || v.IsNull() || v.Boolean() != true {
		t.Errorf("active = %v, want true", full["active"])
	}
	if v, ok := full["seen_at"]; !ok || v.IsNull() {
		t.Errorf("seen_at = %v, want %v", full["seen_at"], seenAt)
	} else if got := timestampMicros(v); !got.Equal(seenAt) {
		t.Errorf("seen_at = %v, want %v", got, seenAt)
	}

	null := rows[1]
	for _, col := range []string{"id", "title", "score", "active", "seen_at"} {
		v, ok := null[col]
		if !ok || !v.IsNull() {
			t.Errorf("row 1 column %q = %v, want NULL", col, v)
		}
	}
}
