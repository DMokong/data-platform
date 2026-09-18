package runner_test

// Behavioral tests for runner.Runner, anchored to AC-15 through AC-19 in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md and the "Runner behaviour" / "Tests" sections
// of tasks/04-runner/brief.md. Every test is commented with the AC(s) it proves.
//
// Black-box (package runner_test): Runner's whole contract (Source, Fetcher, Writer, Now,
// Attempts, Backoff, Concurrency, Log, RunOnce, Backfill, Summary, TableSummary) is exported, so no
// internal hook is needed. Tests use a fake source.Fetcher (in-memory, deterministic per
// (table, window) unless configured to fail) and a real *bronze.Writer rooted at t.TempDir(), and
// assert only on Runner's return values (Summary) and the bytes it leaves on disk — read back
// through parquet-go's public reader API, the same way internal/bronze's own writer_test.go does,
// never through Runner's internal state.
//
// Pre-implementation (this round): internal/runner does not exist yet, so this file will fail to
// compile ("undefined: runner.Runner", etc.) until the implementer adds runner.go. That is the
// expected failing-first signal for a brand-new package, not a harness bug.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/runner"
	"github.com/DMokong/data-platform/internal/source"
	"github.com/DMokong/data-platform/internal/window"
)

// --- test fixtures: a fake Source and a fake Fetcher ------------------------------------------

// fakeSource declares two tables shaped like the real beads.Declaration() (one hourly, lookback
// 24; one daily, lookback 2), under a name distinct from "beads" so nothing here can be confused
// with a live-source test.
func fakeSource() source.Source {
	return source.Source{
		Name:    "acme",
		Cadence: 15 * time.Minute,
		Tables: []source.Table{
			{Name: "events", Grain: window.Hour, Lookback: 24, Shape: source.EventLog},
			{Name: "issues", Grain: window.Day, Lookback: 2, Shape: source.Snapshot},
		},
	}
}

func fetchKey(table string, w window.Window) string {
	return table + "|" + w.String()
}

// fakeFetcher is a source.Fetcher whose behaviour per (table, window) is configured before the
// run: fail its first N calls then succeed, or fail forever. On success it returns one row whose
// "id" is the window's Start (as Unix seconds) plus a fixed offset, so two fakeFetcher instances
// with different offsets produce distinguishably different, but each internally deterministic,
// content — needed to prove a window was actually re-fetched rather than left over from an earlier
// run. Every method is safe for concurrent use, since Runner.Concurrency may fetch several windows
// at once.
type fakeFetcher struct {
	offset int64

	mu     sync.Mutex
	calls  map[string]int
	failN  map[string]int
	always map[string]bool
}

func newFakeFetcher(offset int64) *fakeFetcher {
	return &fakeFetcher{
		offset: offset,
		calls:  map[string]int{},
		failN:  map[string]int{},
		always: map[string]bool{},
	}
}

// failNTimes makes the (table, window) fail its first n Fetch calls, then succeed from call n+1.
func (f *fakeFetcher) failNTimes(table string, w window.Window, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failN[fetchKey(table, w)] = n
}

// alwaysFail makes the (table, window) fail every Fetch call.
func (f *fakeFetcher) alwaysFail(table string, w window.Window) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.always[fetchKey(table, w)] = true
}

func (f *fakeFetcher) callCount(table string, w window.Window) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[fetchKey(table, w)]
}

func (f *fakeFetcher) Fetch(_ context.Context, table string, w window.Window) (record.Batch, error) {
	key := fetchKey(table, w)
	f.mu.Lock()
	f.calls[key]++
	n := f.calls[key]
	shouldFail := f.always[key] || n <= f.failN[key]
	f.mu.Unlock()
	if shouldFail {
		return record.Batch{}, fmt.Errorf("fakeFetcher: forced failure for %s, attempt %d", key, n)
	}
	return record.Batch{
		Columns: []record.Column{{Name: "id", Kind: record.Int64}},
		Rows:    [][]any{{w.Start.Unix() + f.offset}},
	}, nil
}

// var _ source.Fetcher = (*fakeFetcher)(nil) documents the interface this fixture satisfies.
var _ source.Fetcher = (*fakeFetcher)(nil)

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func newTestRunner(fetcher source.Fetcher, src source.Source, root string, clock func() time.Time) *runner.Runner {
	return &runner.Runner{
		Source:   src,
		Fetcher:  fetcher,
		Writer:   &bronze.Writer{Root: root},
		Now:      clock,
		Attempts: 3,
		Backoff:  time.Millisecond,
	}
}

func hourWindow(y int, m time.Month, d, hh int) window.Window {
	start := time.Date(y, m, d, hh, 0, 0, 0, time.UTC)
	return window.Window{Start: start, End: start.Add(time.Hour), Grain: window.Hour}
}

func dayWindow(y int, m time.Month, d int) window.Window {
	start := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return window.Window{Start: start, End: start.Add(24 * time.Hour), Grain: window.Day}
}

// --- filesystem / parquet read-back helpers ----------------------------------------------------

func sha256File(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sumFileSizes returns the total size in bytes of every ".parquet" file under dir, recursively.
func sumFileSizes(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".parquet") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("WalkDir(%s): %v", dir, err)
	}
	return total
}

// parquetFileCount returns how many ".parquet" files exist under dir, recursively; missing dir
// counts as zero.
func parquetFileCount(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".parquet") {
			n++
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("WalkDir(%s): %v", dir, err)
	}
	return n
}

// readRowsExcludingProvenance reads every row of the parquet file at path into column-name-keyed
// maps, dropping the four provenance columns the Writer adds, so the result reflects only what a
// Fetcher served.
func readRowsExcludingProvenance(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	pf, err := parquet.OpenFile(f, info.Size())
	if err != nil {
		t.Fatalf("parquet.OpenFile(%s): %v", path, err)
	}
	reader := parquet.NewReader(pf)
	defer reader.Close()

	provenance := map[string]bool{
		bronze.ColExtractedAt: true,
		bronze.ColWindowStart: true,
		bronze.ColWindowEnd:   true,
		bronze.ColSourceFile:  true,
	}
	cols := pf.Schema().Columns()
	var out []map[string]any
	buf := make([]parquet.Row, 8)
	for {
		n, err := reader.ReadRows(buf)
		for i := 0; i < n; i++ {
			row := buf[i]
			m := map[string]any{}
			for _, v := range row {
				name := cols[v.Column()][0]
				if provenance[name] {
					continue
				}
				m[name] = parquetValue(v)
			}
			out = append(out, m)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("ReadRows(%s): %v", path, err)
		}
		if n == 0 {
			break
		}
	}
	return out
}

func parquetValue(v parquet.Value) any {
	if v.IsNull() {
		return nil
	}
	switch v.Kind() {
	case parquet.Int64:
		return v.Int64()
	case parquet.Double:
		return v.Double()
	case parquet.Boolean:
		return v.Boolean()
	case parquet.ByteArray:
		return string(v.ByteArray())
	default:
		return v.String()
	}
}

// snapshotContent walks dir for ".parquet" files and reads each one's non-provenance rows, keyed
// by its path relative to dir. Comparing two snapshots with reflect.DeepEqual proves both that the
// file set is identical and that every file's content (excluding _extracted_at and friends) is
// identical.
func snapshotContent(t *testing.T, dir string) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".parquet") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out[rel] = readRowsExcludingProvenance(t, path)
		return nil
	})
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("WalkDir(%s): %v", dir, err)
		}
	}
	return out
}

func tableSummary(t *testing.T, s runner.Summary, name string) runner.TableSummary {
	t.Helper()
	for _, ts := range s.Tables {
		if ts.Table == name {
			return ts
		}
	}
	t.Fatalf("Summary.Tables has no entry named %q (got %v)", name, tableNames(s))
	return runner.TableSummary{}
}

func tableNames(s runner.Summary) []string {
	names := make([]string, len(s.Tables))
	for i, ts := range s.Tables {
		names[i] = ts.Table
	}
	return names
}

// --- AC-18: lookback run --------------------------------------------------------------------

// AC-18: RunOnce(ctx, now) writes exactly the declared lookback windows per table and nothing
// else — 24 hourly windows ending with the window containing now, and 2 daily windows — using the
// spec's own worked example: Lookback(2026-09-15T00:30Z, Hour, 24) runs 2026-09-14T01:00Z through
// 2026-09-15T00:00Z, and Lookback(same, Day, 2) is [2026-09-14, 2026-09-15] (AC-01).
func TestRunOnce_LookbackWindowSelection_AC18(t *testing.T) {
	root := t.TempDir()
	src := fakeSource()
	fetcher := newFakeFetcher(0)
	now := time.Date(2026, 9, 15, 0, 30, 0, 0, time.UTC)
	r := newTestRunner(fetcher, src, root, fixedClock(now.Add(5*time.Hour)))

	summary, err := r.RunOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(summary.Tables) != 2 {
		t.Fatalf("Summary.Tables has %d entries %v, want 2 (declaration order: events, issues)", len(summary.Tables), tableNames(summary))
	}
	events, issues := summary.Tables[0], summary.Tables[1]
	if events.Table != "events" || events.Windows != 24 {
		t.Errorf("Tables[0] = %+v, want Table=events Windows=24 (declaration order preserved)", events)
	}
	if issues.Table != "issues" || issues.Windows != 2 {
		t.Errorf("Tables[1] = %+v, want Table=issues Windows=2 (declaration order preserved)", issues)
	}

	// The exact windows written: bronze.Path for each of window.Lookback's own 24 hourly and 2
	// daily windows must exist, and nothing else must be in those directories.
	wantHourly, err := window.Lookback(now, window.Hour, 24)
	if err != nil {
		t.Fatalf("window.Lookback (hourly): %v", err)
	}
	wantDaily, err := window.Lookback(now, window.Day, 2)
	if err != nil {
		t.Fatalf("window.Lookback (daily): %v", err)
	}
	if got, want := wantHourly[0].Start, time.Date(2026, 9, 14, 1, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("test setup: window.Lookback's first hourly window = %v, want %v (AC-01's worked example)", got, want)
	}
	if got, want := wantHourly[len(wantHourly)-1].Start, time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("test setup: window.Lookback's last hourly window = %v, want %v (AC-01's worked example)", got, want)
	}

	for _, w := range wantHourly {
		rel, err := bronze.Path(bronze.Raw, "acme/events", w)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("lookback window %s: expected file missing (%s): %v", w, rel, err)
		}
	}
	for _, w := range wantDaily {
		rel, err := bronze.Path(bronze.Raw, "acme/issues", w)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("lookback window %s: expected file missing (%s): %v", w, rel, err)
		}
	}

	if got := parquetFileCount(t, filepath.Join(root, "raw", "acme", "events")); got != 24 {
		t.Errorf("events directory has %d parquet files, want exactly 24 (nothing else written)", got)
	}
	if got := parquetFileCount(t, filepath.Join(root, "raw", "acme", "issues")); got != 2 {
		t.Errorf("issues directory has %d parquet files, want exactly 2 (nothing else written)", got)
	}
}

// --- AC-15: backfill command (counts and summary totals) -----------------------------------

// AC-15: Backfill(from, to) over a 2-day range leaves exactly 48 files under the hourly table's
// directory and 2 under the daily table's, and Summary reports windows, rows and bytes per table
// that match what actually landed on disk.
func TestBackfill_CountsAndSummaryTotals_AC15(t *testing.T) {
	root := t.TempDir()
	src := fakeSource()
	fetcher := newFakeFetcher(0)
	from := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	r := newTestRunner(fetcher, src, root, fixedClock(to.Add(6*time.Hour)))

	summary, err := r.Backfill(context.Background(), from, to)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}

	if len(summary.Tables) != 2 {
		t.Fatalf("Summary.Tables has %d entries, want 2", len(summary.Tables))
	}
	events := tableSummary(t, summary, "events")
	issues := tableSummary(t, summary, "issues")

	if events.Windows != 48 {
		t.Errorf("events.Windows = %d, want 48 (2 days x 24 hourly windows)", events.Windows)
	}
	if issues.Windows != 2 {
		t.Errorf("issues.Windows = %d, want 2 (2 daily windows)", issues.Windows)
	}
	// fakeFetcher returns exactly one row per successful window.
	if events.Rows != 48 {
		t.Errorf("events.Rows = %d, want 48 (1 row per window)", events.Rows)
	}
	if issues.Rows != 2 {
		t.Errorf("issues.Rows = %d, want 2 (1 row per window)", issues.Rows)
	}
	if len(events.Failed) != 0 {
		t.Errorf("events.Failed = %v, want empty", events.Failed)
	}
	if len(issues.Failed) != 0 {
		t.Errorf("issues.Failed = %v, want empty", issues.Failed)
	}

	wantEventsBytes := sumFileSizes(t, filepath.Join(root, "raw", "acme", "events"))
	if events.Bytes != wantEventsBytes {
		t.Errorf("events.Bytes = %d, want %d (sum of the written files' sizes on disk)", events.Bytes, wantEventsBytes)
	}
	wantIssuesBytes := sumFileSizes(t, filepath.Join(root, "raw", "acme", "issues"))
	if issues.Bytes != wantIssuesBytes {
		t.Errorf("issues.Bytes = %d, want %d (sum of the written files' sizes on disk)", issues.Bytes, wantIssuesBytes)
	}
	if events.Bytes <= 0 || issues.Bytes <= 0 {
		t.Errorf("Bytes must be positive for a successful run: events=%d issues=%d", events.Bytes, issues.Bytes)
	}

	if got := parquetFileCount(t, filepath.Join(root, "raw", "acme", "events")); got != 48 {
		t.Errorf("events directory has %d parquet files, want exactly 48 (AC-15's own count)", got)
	}
	if got := parquetFileCount(t, filepath.Join(root, "raw", "acme", "issues")); got != 2 {
		t.Errorf("issues directory has %d parquet files, want exactly 2 (AC-15's own count)", got)
	}
}

// --- AC-19: failure handling -----------------------------------------------------------------

// AC-19: a window whose Fetcher fails on its first two calls and succeeds on the third is retried
// (bounded by Attempts) and ends up written, with the run overall reporting no failure.
func TestBackfill_RetriedWindowEventuallySucceeds_AC19(t *testing.T) {
	root := t.TempDir()
	src := fakeSource()
	fetcher := newFakeFetcher(0)
	day := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	flaky := hourWindow(2026, 9, 14, 5)
	fetcher.failNTimes("events", flaky, 2) // fails attempts 1 and 2, succeeds on attempt 3

	r := newTestRunner(fetcher, src, root, fixedClock(day.Add(12*time.Hour)))
	r.Attempts = 3
	r.Backoff = time.Millisecond

	summary, err := r.Backfill(context.Background(), day, day)
	if err != nil {
		t.Fatalf("Backfill: %v, want nil (the flaky window succeeds within Attempts=3)", err)
	}

	if got := fetcher.callCount("events", flaky); got != 3 {
		t.Errorf("fetcher was called %d times for the flaky window, want exactly 3 (2 failures + 1 success, bounded by Attempts)", got)
	}

	rel, err := bronze.Path(bronze.Raw, "acme/events", flaky)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
		t.Errorf("flaky window's file missing after eventual success: %v", err)
	}

	events := tableSummary(t, summary, "events")
	if len(events.Failed) != 0 {
		t.Errorf("events.Failed = %v, want empty (the retry succeeded)", events.Failed)
	}
}

// AC-19: a window whose Fetcher fails on every call makes the run return a non-nil error naming
// that table and window, while every other window's file is still written (with the second run's
// content, proving the run kept going past the failure), and the failing window's previous file
// from an earlier successful run is left byte-for-byte untouched.
func TestBackfill_PersistentFailureNamesWindowAndLeavesOthersWritten_AC19(t *testing.T) {
	root := t.TempDir()
	src := fakeSource()
	day := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)

	// First run: everything succeeds, establishing a prior file for the window that will later
	// fail persistently, plus a baseline for the window that will keep succeeding.
	fetcher1 := newFakeFetcher(0)
	r := newTestRunner(fetcher1, src, root, fixedClock(day.Add(12*time.Hour)))
	r.Attempts = 3
	r.Backoff = time.Millisecond
	if _, err := r.Backfill(context.Background(), day, day); err != nil {
		t.Fatalf("initial Backfill: %v", err)
	}

	stuck := hourWindow(2026, 9, 14, 5)
	ok := hourWindow(2026, 9, 14, 6)
	stuckRel, err := bronze.Path(bronze.Raw, "acme/events", stuck)
	if err != nil {
		t.Fatal(err)
	}
	okRel, err := bronze.Path(bronze.Raw, "acme/events", ok)
	if err != nil {
		t.Fatal(err)
	}
	stuckBefore := sha256File(t, filepath.Join(root, stuckRel))

	// Second run: `stuck`'s window always fails now; every other window (including `ok`) still
	// succeeds, with a fetcher offset that makes its content provably different from the first
	// run's, so a surviving old file could not be mistaken for a freshly written one.
	fetcher2 := newFakeFetcher(1_000_000)
	fetcher2.alwaysFail("events", stuck)
	r.Fetcher = fetcher2
	r.Now = fixedClock(day.Add(7 * 24 * time.Hour))

	summary, err := r.Backfill(context.Background(), day, day)
	if err == nil {
		t.Fatal("second Backfill returned a nil error, want an error naming the persistently failing window")
	}
	wantSubstr := "events " + stuck.String()
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("Backfill error = %q, want it to contain %q (table and window)", err.Error(), wantSubstr)
	}

	events := tableSummary(t, summary, "events")
	if len(events.Failed) != 1 || !strings.Contains(events.Failed[0], wantSubstr) {
		t.Errorf("events.Failed = %v, want exactly one entry containing %q (\"table window: error\")", events.Failed, wantSubstr)
	}

	// The persistently-failing window's file, from the first run, is untouched.
	stuckAfter := sha256File(t, filepath.Join(root, stuckRel))
	if stuckAfter != stuckBefore {
		t.Errorf("persistently-failing window's file changed: before=%s after=%s, want untouched (its previous file, AC-19)", stuckBefore, stuckAfter)
	}

	// A different, healthy window was still (re)written with the second run's content: the run
	// did not stop when `stuck` failed.
	okRows := readRowsExcludingProvenance(t, filepath.Join(root, okRel))
	wantID := ok.Start.Unix() + 1_000_000
	if len(okRows) != 1 || okRows[0]["id"] != wantID {
		t.Errorf("ok window rows = %v, want [{id: %d}] (the second run's fetcher content, proving it was re-written, not left over)", okRows, wantID)
	}
}

// --- AC-16: idempotent re-run -----------------------------------------------------------------

// AC-16: running the same Backfill twice, with a Fetcher whose content is a pure function of
// (table, window), leaves the same file set both times, and every file's rows — excluding
// _extracted_at — are identical between the two runs, even though the extraction clock advances
// between them.
func TestBackfill_IdempotentRerun_AC16(t *testing.T) {
	root := t.TempDir()
	src := fakeSource()
	fetcher := newFakeFetcher(0)
	day := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)

	r := newTestRunner(fetcher, src, root, fixedClock(day.Add(5*time.Hour)))
	if _, err := r.Backfill(context.Background(), day, day); err != nil {
		t.Fatalf("first Backfill: %v", err)
	}
	before := snapshotContent(t, root)
	if len(before) != 25 { // 24 hourly + 1 daily window for one day
		t.Fatalf("test setup: first Backfill left %d files, want 25 (24 hourly + 1 daily)", len(before))
	}

	// Re-run with a different extraction clock, days later: only _extracted_at should be able to
	// change, and that column is excluded from the comparison below.
	r.Now = fixedClock(day.Add(20 * 24 * time.Hour))
	if _, err := r.Backfill(context.Background(), day, day); err != nil {
		t.Fatalf("second Backfill: %v", err)
	}
	after := snapshotContent(t, root)

	if !reflect.DeepEqual(before, after) {
		t.Errorf("re-run changed the file set and/or row content (excluding _extracted_at):\nbefore: %v\nafter:  %v", before, after)
	}
}

// --- AC-17: replay after loss -----------------------------------------------------------------

// AC-17: after deleting one day's worth of an hourly table's files, a Backfill scoped to just that
// day restores all 24 files, with content (excluding _extracted_at) equal to what was there before
// the deletion.
func TestBackfill_ReplayAfterLoss_AC17(t *testing.T) {
	root := t.TempDir()
	src := fakeSource()
	fetcher := newFakeFetcher(0)
	from := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

	r := newTestRunner(fetcher, src, root, fixedClock(to.Add(5*time.Hour)))
	if _, err := r.Backfill(context.Background(), from, to); err != nil {
		t.Fatalf("initial Backfill: %v", err)
	}

	eventsDir := filepath.Join(root, "raw", "acme", "events")
	before := snapshotContent(t, eventsDir)
	if len(before) != 48 {
		t.Fatalf("test setup: initial Backfill left %d events files, want 48", len(before))
	}

	lostDay := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	lostDir := filepath.Join(eventsDir, "dt=2026-09-15")
	if err := os.RemoveAll(lostDir); err != nil {
		t.Fatalf("RemoveAll(%s): %v", lostDir, err)
	}
	if got := parquetFileCount(t, eventsDir); got != 24 {
		t.Fatalf("test setup: after deleting dt=2026-09-15, events dir has %d files, want 24", got)
	}

	r.Now = fixedClock(to.Add(20 * 24 * time.Hour))
	if _, err := r.Backfill(context.Background(), lostDay, lostDay); err != nil {
		t.Fatalf("replay Backfill: %v", err)
	}

	after := snapshotContent(t, eventsDir)
	if len(after) != 48 {
		t.Fatalf("events dir has %d files after replay, want 48 (restored to the pre-loss count)", len(after))
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("replayed content differs from pre-loss content (excluding _extracted_at):\nbefore: %v\nafter:  %v", before, after)
	}
}
