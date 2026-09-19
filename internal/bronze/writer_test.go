package bronze

// Behavioral tests for bronze.Writer.Write, anchored to AC-04 through AC-07 in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md and the "Write rules" / "Required exported
// API" sections of tasks/02-writer/brief.md. Every test is commented with the AC it proves.
//
// This file is a white-box test (package bronze, not bronze_test): one test (AC-05's injected
// encode failure) needs an unexported hook, so the whole file lives inside the package rather
// than splitting the seam test into a third file. All other tests here only use Write's exported
// return values (Result) and the bytes it puts on disk, read back through parquet-go's public
// reader API — never internal Writer state.
//
// IMPLEMENTATION HOOK: TestWrite_EncodeFailureLeavesPriorFileIntact_AC05 sets the package-level
// variable forceEncodeErr, declared in writer.go. When non-nil it is returned in place of a
// successful encode, as if the Parquet encoder had failed after Batch validation; Write must still
// remove its temp file and leave any existing target untouched (AC-05). It is always nil in
// production; only this package's tests set it, and they reset it afterwards.
// No other test in this file depends on any unexported symbol.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"

	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/window"
)

// --- test helpers -----------------------------------------------------------------------------

func mustHourWindow(y int, m time.Month, d, hh int) window.Window {
	start := time.Date(y, m, d, hh, 0, 0, 0, time.UTC)
	return window.Window{Start: start, End: start.Add(time.Hour), Grain: window.Hour}
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// assertNoTempFiles fails the test if dir contains any file matching the ".tmp-*" pattern the
// brief mandates for the Writer's temp-file staging (AC-05).
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %q in %s", e.Name(), dir)
		}
	}
}

// parquetFile wraps an opened, written bronze file for read-back assertions.
type parquetFile struct {
	schema *parquet.Schema
	reader *parquet.Reader
}

func openWritten(t *testing.T, root, relPath string) *parquetFile {
	t.Helper()
	abs := filepath.Join(root, relPath)
	f, err := os.Open(abs)
	if err != nil {
		t.Fatalf("open %s: %v", abs, err)
	}
	t.Cleanup(func() { f.Close() })
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat %s: %v", abs, err)
	}
	pf, err := parquet.OpenFile(f, info.Size())
	if err != nil {
		t.Fatalf("parquet.OpenFile(%s): %v", abs, err)
	}
	reader := parquet.NewReader(pf)
	t.Cleanup(func() { reader.Close() })
	return &parquetFile{schema: pf.Schema(), reader: reader}
}

// readRows reads every row of the file into column-name-keyed maps, using each Value's own
// column index (Value.Column()) rather than assuming any particular on-disk column order, so
// this helper stays correct regardless of how the schema orders its leaf columns.
func (pfl *parquetFile) readRows(t *testing.T) []map[string]parquet.Value {
	t.Helper()
	cols := pfl.schema.Columns()
	var out []map[string]parquet.Value
	buf := make([]parquet.Row, 8)
	for {
		n, err := pfl.reader.ReadRows(buf)
		for i := 0; i < n; i++ {
			row := buf[i]
			m := make(map[string]parquet.Value, len(cols))
			for _, v := range row {
				m[cols[v.Column()][0]] = v
			}
			out = append(out, m)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("ReadRows: %v", err)
		}
		if n == 0 {
			break
		}
	}
	return out
}

func assertStringLogicalType(t *testing.T, schema *parquet.Schema, name string) {
	t.Helper()
	lc, ok := schema.Lookup(name)
	if !ok {
		t.Fatalf("schema has no column %q", name)
	}
	lt := lc.Node.Type().LogicalType()
	if lt == nil {
		t.Errorf("column %q has no logical type, want String", name)
		return
	}
	if _, ok := lt.Value.(*format.StringType); !ok {
		t.Errorf("column %q logical type = %v, want String", name, lt)
	}
}

func assertTimestampLogicalType(t *testing.T, schema *parquet.Schema, name string, wantAdjustedToUTC bool) {
	t.Helper()
	lc, ok := schema.Lookup(name)
	if !ok {
		t.Fatalf("schema has no column %q", name)
	}
	lt := lc.Node.Type().LogicalType()
	if lt == nil {
		t.Fatalf("column %q has no logical type, want TIMESTAMP(MICROS, isAdjustedToUTC=%v)", name, wantAdjustedToUTC)
	}
	ts, ok := lt.Value.(*format.TimestampType)
	if !ok {
		t.Fatalf("column %q logical type = %v (%T), want *format.TimestampType", name, lt, lt.Value)
	}
	if ts.IsAdjustedToUTC != wantAdjustedToUTC {
		t.Errorf("column %q IsAdjustedToUTC = %v, want %v", name, ts.IsAdjustedToUTC, wantAdjustedToUTC)
	}
	if _, ok := ts.Unit.Value.(*format.MicroSeconds); !ok {
		t.Errorf("column %q time unit = %v, want microseconds", name, ts.Unit)
	}
}

func timestampValue(v parquet.Value) time.Time {
	return time.UnixMicro(v.Int64()).UTC()
}

// --- AC-04: byte-identical re-runs ----------------------------------------------------------

// AC-04: two writes with identical batch, window and extractedAt (into different roots, so no
// filesystem state is shared) produce files with equal SHA-256; changing only extractedAt changes
// the hash, so the equality above is not vacuous.
func TestWrite_DeterministicBytes_AC04(t *testing.T) {
	root1, root2 := t.TempDir(), t.TempDir()
	w := mustHourWindow(2026, 9, 15, 13)
	batch := record.Batch{
		Columns: []record.Column{
			{Name: "id", Kind: record.Int64},
			{Name: "title", Kind: record.String},
		},
		Rows: [][]any{
			{int64(1), "hello"},
			{int64(2), nil},
		},
	}
	extractedAt := time.Date(2026, 9, 15, 13, 5, 0, 0, time.UTC)

	wr1 := &Writer{Root: root1}
	res1, err := wr1.Write(context.Background(), Raw, "beads/events", w, batch, extractedAt)
	if err != nil {
		t.Fatalf("first Write: %v", err)
	}
	wr2 := &Writer{Root: root2}
	res2, err := wr2.Write(context.Background(), Raw, "beads/events", w, batch, extractedAt)
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if res1.Path != res2.Path {
		t.Fatalf("Path differs between roots: %q vs %q", res1.Path, res2.Path)
	}

	h1 := sha256File(t, filepath.Join(root1, res1.Path))
	h2 := sha256File(t, filepath.Join(root2, res2.Path))
	if h1 != h2 {
		t.Errorf("same batch/window/extractedAt into different roots produced different bytes: %s vs %s", h1, h2)
	}

	// Not vacuous: a different extractedAt must change the hash.
	res3, err := wr1.Write(context.Background(), Raw, "beads/events", w, batch, extractedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("third Write (different extractedAt): %v", err)
	}
	h3 := sha256File(t, filepath.Join(root1, res3.Path))
	if h3 == h1 {
		t.Errorf("changing only extractedAt did not change the file's bytes (both hashed %s)", h1)
	}
}

// goldenSHA256 is the SHA-256 of the file TestWrite_GoldenBytes_AC04 writes, with the module set
// pinned in go.mod (parquet-go v0.32.0, klauspost/compress v1.17.9) and the Writer's fixed codec
// and options. It was recorded identically from darwin/arm64 and darwin/amd64 test binaries.
const goldenSHA256 = "ad8783003f5ee7e32770227a360fb1c5d9c6de5e9bd90d3e4503df9a8715e8c6"

// AC-04 (and AC-08's "nondeterministic output"): the bytes for fixed inputs equal a recorded
// hash. Two writes in quick succession (TestWrite_DeterministicBytes_AC04) cannot catch output
// that depends on a coarse clock (a date, or whole seconds, in the footer metadata), or on the
// binary or host that wrote it; a hash recorded in an earlier run can. If this fails after a
// deliberate change to the codec, the writer options or a pinned module version, re-record the
// hash, and note that existing bronze files will no longer be byte-identical to re-runs.
// Otherwise the output has picked up an input other than batch, window and extractedAt.
func TestWrite_GoldenBytes_AC04(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 9, 15, 13, 0, 0, 0, time.UTC)
	w := window.Window{Start: start, End: start.Add(time.Hour), Grain: window.Hour}
	batch := record.Batch{
		Columns: []record.Column{
			{Name: "id", Kind: record.Int64},
			{Name: "title", Kind: record.String},
			{Name: "score", Kind: record.Float64},
			{Name: "open", Kind: record.Bool},
			{Name: "created_at", Kind: record.Timestamp},
		},
		Rows: [][]any{
			{int64(1), "first", 1.5, true, time.Date(2026, 9, 15, 23, 1, 2, 3000, time.UTC)},
			{int64(2), nil, nil, nil, nil},
			{int64(3), "third", -0.25, false, time.Date(2026, 9, 15, 23, 59, 59, 999999000, time.UTC)},
		},
	}
	extractedAt := time.Date(2026, 9, 15, 13, 7, 8, 9000, time.UTC)

	res, err := (&Writer{Root: root}).Write(context.Background(), Raw, "beads/events", w, batch, extractedAt)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := sha256File(t, filepath.Join(root, res.Path)); got != goldenSHA256 {
		t.Errorf("fixed inputs hashed to %s, want the recorded %s (see this test's comment)", got, goldenSHA256)
	}
}

// AC-03 / AC-06: Write rejects a batch that fails Validate, and every input Path rejects, before
// touching the filesystem: it returns an error and creates nothing under Root.
func TestWrite_RejectsInvalidInputsWithoutWriting(t *testing.T) {
	good := mustHourWindow(2026, 9, 15, 13)
	misaligned := window.Window{Start: good.Start.Add(time.Minute), End: good.End.Add(time.Minute), Grain: window.Hour}
	idCol := []record.Column{{Name: "id", Kind: record.Int64}}
	okBatch := record.Batch{Columns: idCol, Rows: [][]any{{int64(1)}}}
	cases := []struct {
		name    string
		zone    Zone
		dataset string
		w       window.Window
		b       record.Batch
	}{
		{"ragged row", Raw, "beads/events", good, record.Batch{Columns: idCol, Rows: [][]any{{int64(1), "extra"}}}},
		{"value does not match its kind", Raw, "beads/events", good, record.Batch{Columns: idCol, Rows: [][]any{{"1"}}}},
		{"duplicate column name", Raw, "beads/events", good, record.Batch{Columns: append(idCol, idCol[0]), Rows: [][]any{{int64(1), int64(2)}}}},
		{"unknown zone", Zone("gold"), "beads/events", good, okBatch},
		{"dataset escapes the root", Raw, "../events", good, okBatch},
		{"misaligned window", Raw, "beads/events", misaligned, okBatch},
		// AC-03 (test-breaker round 1 / breaker-0 V1): a dataset segment outside ^[a-z0-9_]+$ by
		// character class, not just the ".." escape above. The ".." case alone cannot catch a regex
		// that lost its anchors (e.g. `^[a-z0-9_]+$` -> `[a-z0-9_]+`), since "." is outside [a-z0-9_]
		// either way; these three cases each contain a valid lowercase substring ("beads"/"events")
		// so only a correctly anchored, whole-segment match rejects them. Because this table drives
		// Write (white-box, package bronze) rather than the exported Path directly, it exercises
		// whichever Path implementation actually lives in this package, unlike a black-box test that
		// imports bronze by its fixed module path.
		{"dataset segment has uppercase", Raw, "Beads/events", good, okBatch},
		{"dataset segment has a hyphen", Raw, "beads/ev-ents", good, okBatch},
		{"dataset segment has a space", Raw, "beads/ev ents", good, okBatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			if _, err := (&Writer{Root: root}).Write(context.Background(), c.zone, c.dataset, c.w, c.b, good.Start); err == nil {
				t.Fatal("Write returned a nil error, want rejection")
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			if len(entries) != 0 {
				t.Errorf("rejected Write created %d entries under Root, want none", len(entries))
			}
		})
	}

	t.Run("empty Root", func(t *testing.T) {
		cwd := t.TempDir()
		t.Chdir(cwd) // without the guard, an empty Root would write relative to the working directory
		if _, err := (&Writer{}).Write(context.Background(), Raw, "beads/events", good, okBatch, good.Start); err == nil {
			t.Fatal("Write with an empty Root returned a nil error")
		}
		if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
			t.Errorf("Write with an empty Root created %d entries in the working directory", len(entries))
		}
	})
}

// --- AC-05: overwrite, never append, atomically ----------------------------------------------

// AC-05: writing a window twice leaves exactly one file, holding only the second batch's rows,
// and no ".tmp-*" file remains in the target directory afterwards.
func TestWrite_OverwriteIsAtomicNotAppend_AC05(t *testing.T) {
	root := t.TempDir()
	wr := &Writer{Root: root}
	w := mustHourWindow(2026, 9, 15, 15)
	col := []record.Column{{Name: "id", Kind: record.Int64}}

	three := record.Batch{Columns: col, Rows: [][]any{{int64(1)}, {int64(2)}, {int64(3)}}}
	res1, err := wr.Write(context.Background(), Raw, "beads/events", w, three, time.Date(2026, 9, 15, 15, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("first Write (3 rows): %v", err)
	}
	if res1.Rows != 3 {
		t.Fatalf("first Write Result.Rows = %d, want 3", res1.Rows)
	}

	one := record.Batch{Columns: col, Rows: [][]any{{int64(99)}}}
	res2, err := wr.Write(context.Background(), Raw, "beads/events", w, one, time.Date(2026, 9, 15, 15, 6, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("second Write (1 row): %v", err)
	}
	if res1.Path != res2.Path {
		t.Fatalf("second Write targeted a different path: %q vs %q", res2.Path, res1.Path)
	}
	if res2.Rows != 1 {
		t.Fatalf("second Write Result.Rows = %d, want 1", res2.Rows)
	}

	pf := openWritten(t, root, res2.Path)
	rows := pf.readRows(t)
	if len(rows) != 1 {
		t.Fatalf("file has %d rows after overwrite, want 1 (no append)", len(rows))
	}
	if rows[0]["id"].IsNull() || rows[0]["id"].Int64() != 99 {
		t.Errorf("surviving row id = %v, want 99 (the second batch's row, not the first's)", rows[0]["id"])
	}

	assertNoTempFiles(t, filepath.Join(root, filepath.Dir(res2.Path)))

	// Exactly one file for the window: the overwrite replaced the old file in place rather than
	// adding a sibling.
	entries, err := os.ReadDir(filepath.Join(root, filepath.Dir(res2.Path)))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(res2.Path) {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("window directory holds %v, want exactly [%s]", names, filepath.Base(res2.Path))
	}
}

// assertNoFileForWindow fails the test unless a failed first write of a window left neither the
// target file nor a temp file behind (AC-05, and AC-19's "a failed window leaves no partial file").
func assertNoFileForWindow(t *testing.T, root string, w window.Window) {
	t.Helper()
	rel, err := Path(Raw, "beads/events", w)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	abs := filepath.Join(root, rel)
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Errorf("failed first write of %s left %s behind (Stat err = %v), want no file", w, abs, err)
	}
	assertNoTempFiles(t, filepath.Dir(abs))
}

// AC-05: an encode failure injected through the forceEncodeErr test seam leaves the prior file
// byte-identical and no temp file behind.
func TestWrite_EncodeFailureLeavesPriorFileIntact_AC05(t *testing.T) {
	root := t.TempDir()
	wr := &Writer{Root: root}
	w := mustHourWindow(2026, 9, 15, 16)
	col := []record.Column{{Name: "id", Kind: record.Int64}}

	good := record.Batch{Columns: col, Rows: [][]any{{int64(1)}}}
	res, err := wr.Write(context.Background(), Raw, "beads/events", w, good, time.Date(2026, 9, 15, 16, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("initial (control) Write: %v", err)
	}
	abs := filepath.Join(root, res.Path)
	before := sha256File(t, abs)

	old := forceEncodeErr
	forceEncodeErr = errors.New("bronze_test: injected encode failure")
	t.Cleanup(func() { forceEncodeErr = old })

	changed := record.Batch{Columns: col, Rows: [][]any{{int64(2)}, {int64(3)}}}
	_, err = wr.Write(context.Background(), Raw, "beads/events", w, changed, time.Date(2026, 9, 15, 16, 6, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("Write with forceEncodeErr set returned a nil error, want the injected failure")
	}

	after := sha256File(t, abs)
	if after != before {
		t.Errorf("target file changed after a failed write: before=%s after=%s", before, after)
	}
	assertNoTempFiles(t, filepath.Dir(abs))

	// The same failure on a window that has no file yet leaves no file at all.
	fresh := mustHourWindow(2026, 9, 15, 20)
	if _, err := wr.Write(context.Background(), Raw, "beads/events", fresh, changed, time.Date(2026, 9, 15, 20, 6, 0, 0, time.UTC)); err == nil {
		t.Fatal("first Write of a fresh window with forceEncodeErr set returned a nil error")
	}
	assertNoFileForWindow(t, root, fresh)
}

// AC-05: a context cancelled before Write reaches its rename step leaves the prior file
// byte-identical and no temp file behind. AC-05 pins the check to "immediately before
// os.Rename", so a context that is already cancelled when Write is called must still be
// honoured there (Write is not required to bail out earlier).
func TestWrite_ContextCancelledBeforeRename_AC05(t *testing.T) {
	root := t.TempDir()
	wr := &Writer{Root: root}
	w := mustHourWindow(2026, 9, 15, 17)
	col := []record.Column{{Name: "id", Kind: record.Int64}}

	good := record.Batch{Columns: col, Rows: [][]any{{int64(1)}}}
	res, err := wr.Write(context.Background(), Raw, "beads/events", w, good, time.Date(2026, 9, 15, 17, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("initial (control) Write: %v", err)
	}
	abs := filepath.Join(root, res.Path)
	before := sha256File(t, abs)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	changed := record.Batch{Columns: col, Rows: [][]any{{int64(2)}}}
	_, err = wr.Write(ctx, Raw, "beads/events", w, changed, time.Date(2026, 9, 15, 17, 6, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("Write with an already-cancelled context returned a nil error")
	}

	after := sha256File(t, abs)
	if after != before {
		t.Errorf("target file changed after a cancelled-context write: before=%s after=%s", before, after)
	}
	assertNoTempFiles(t, filepath.Dir(abs))

	// The same cancellation on a window that has no file yet leaves no file at all.
	fresh := mustHourWindow(2026, 9, 15, 21)
	if _, err := wr.Write(ctx, Raw, "beads/events", fresh, changed, time.Date(2026, 9, 15, 21, 6, 0, 0, time.UTC)); err == nil {
		t.Fatal("first Write of a fresh window with a cancelled context returned a nil error")
	}
	assertNoFileForWindow(t, root, fresh)
}

// --- AC-06: provenance columns -----------------------------------------------------------------

// AC-06: every row carries _extracted_at (truncated to microseconds, UTC), _window_start,
// _window_end and _source_file (equal to the file's own AC-03 path), appended after the batch
// columns.
func TestWrite_ProvenanceColumns_AC06(t *testing.T) {
	root := t.TempDir()
	wr := &Writer{Root: root}
	w := mustHourWindow(2026, 9, 15, 13)
	dataset := "beads/events"
	batch := record.Batch{
		Columns: []record.Column{{Name: "id", Kind: record.Int64}},
		Rows:    [][]any{{int64(7)}},
	}
	// Sub-microsecond nanos (...789) so the read-back proves truncation, not a lucky coincidence.
	extractedAt := time.Date(2026, 9, 15, 13, 5, 30, 123456789, time.UTC)

	res, err := wr.Write(context.Background(), Raw, dataset, w, batch, extractedAt)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	wantPath, err := Path(Raw, dataset, w)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if res.Path != wantPath {
		t.Errorf("Result.Path = %q, want %q (the same path Path() derives)", res.Path, wantPath)
	}
	if res.Rows != 1 {
		t.Errorf("Result.Rows = %d, want 1", res.Rows)
	}
	if info, err := os.Stat(filepath.Join(root, res.Path)); err != nil {
		t.Fatalf("Stat: %v", err)
	} else if res.Bytes != info.Size() {
		t.Errorf("Result.Bytes = %d, want the file's size %d", res.Bytes, info.Size())
	}

	pf := openWritten(t, root, res.Path)
	rows := pf.readRows(t)
	if len(rows) != 1 {
		t.Fatalf("file has %d rows, want 1", len(rows))
	}
	row := rows[0]

	wantExtractedAt := time.Date(2026, 9, 15, 13, 5, 30, 123456000, time.UTC) // truncated to us
	if row[ColExtractedAt].IsNull() || !timestampValue(row[ColExtractedAt]).Equal(wantExtractedAt) {
		t.Errorf("%s = %v, want %v (extractedAt truncated to microseconds)", ColExtractedAt, timestampValue(row[ColExtractedAt]), wantExtractedAt)
	}
	if row[ColWindowStart].IsNull() || !timestampValue(row[ColWindowStart]).Equal(w.Start) {
		t.Errorf("%s = %v, want %v (window.Start)", ColWindowStart, timestampValue(row[ColWindowStart]), w.Start)
	}
	if row[ColWindowEnd].IsNull() || !timestampValue(row[ColWindowEnd]).Equal(w.End) {
		t.Errorf("%s = %v, want %v (window.End)", ColWindowEnd, timestampValue(row[ColWindowEnd]), w.End)
	}
	if row[ColSourceFile].IsNull() || string(row[ColSourceFile].ByteArray()) != res.Path {
		t.Errorf("%s = %q, want %q (the file's own bronze-relative path)", ColSourceFile, string(row[ColSourceFile].ByteArray()), res.Path)
	}
}

// AC-06: a batch that already carries a column named like a provenance column is rejected, for
// every one of the four provenance names, in any letter case, and nothing is written. Case matters
// because DuckDB resolves column names case-insensitively: a batch column "_EXTRACTED_AT" would
// make DuckDB rename the real provenance column to "_extracted_at_1", so `select _extracted_at`
// would return the batch's value instead of the provenance.
func TestWrite_RejectsProvenanceNamedColumn_AC06(t *testing.T) {
	w := mustHourWindow(2026, 9, 15, 13)

	var names []string
	for _, p := range []string{ColExtractedAt, ColWindowStart, ColWindowEnd, ColSourceFile} {
		names = append(names, p, strings.ToUpper(p), strings.ToUpper(p[:2])+p[2:])
	}
	for _, bad := range names {
		t.Run(bad, func(t *testing.T) {
			root := t.TempDir()
			batch := record.Batch{
				Columns: []record.Column{
					{Name: "id", Kind: record.Int64},
					{Name: bad, Kind: record.String},
				},
				Rows: [][]any{{int64(1), "x"}},
			}
			_, err := (&Writer{Root: root}).Write(context.Background(), Raw, "beads/events", w, batch, time.Date(2026, 9, 15, 13, 0, 0, 0, time.UTC))
			if err == nil {
				t.Errorf("Write with an input column named %q returned a nil error, want rejection", bad)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			if len(entries) != 0 {
				t.Errorf("rejected Write created %d entries under Root, want none", len(entries))
			}
		})
	}
}

// --- AC-07: types round-trip untransformed ------------------------------------------------------

// AC-07: String, Int64, Float64, Bool and Timestamp map to their matching Parquet types
// (BYTE_ARRAY+String, INT64, DOUBLE, BOOLEAN, INT64 TIMESTAMP(MICROS, isAdjustedToUTC=false)
// respectively); provenance timestamps carry isAdjustedToUTC=true; NULLs stay NULL; batch columns
// precede the four provenance columns, in the batch's own order (AC-06's ordering promise).
func TestWrite_TypeRoundTrip_AC07(t *testing.T) {
	root := t.TempDir()
	wr := &Writer{Root: root}
	w := mustHourWindow(2026, 9, 15, 13)
	ts := time.Date(2026, 9, 15, 13, 45, 30, 123456000, time.UTC) // exact microsecond precision
	batch := record.Batch{
		Columns: []record.Column{
			{Name: "s", Kind: record.String},
			{Name: "i", Kind: record.Int64},
			{Name: "f", Kind: record.Float64},
			{Name: "b", Kind: record.Bool},
			{Name: "ts", Kind: record.Timestamp},
		},
		Rows: [][]any{
			{"hello", int64(42), 3.5, true, ts},
			{nil, nil, nil, nil, nil},
		},
	}
	res, err := wr.Write(context.Background(), Raw, "sample/kinds", w, batch, time.Date(2026, 9, 15, 13, 50, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	pf := openWritten(t, root, res.Path)

	// Column order: batch columns first in batch order, then the four provenance columns.
	wantOrder := []string{"s", "i", "f", "b", "ts", ColExtractedAt, ColWindowStart, ColWindowEnd, ColSourceFile}
	gotOrder := pf.schema.Columns()
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("schema has %d columns %v, want %d %v", len(gotOrder), gotOrder, len(wantOrder), wantOrder)
	}
	for i, want := range wantOrder {
		if gotOrder[i][0] != want {
			t.Errorf("column %d = %q, want %q (batch columns first in batch order, then provenance)", i, gotOrder[i][0], want)
		}
	}

	// Physical kind and optional/required.
	type expect struct {
		name     string
		kind     parquet.Kind
		optional bool
	}
	for _, e := range []expect{
		{"s", parquet.ByteArray, true},
		{"i", parquet.Int64, true},
		{"f", parquet.Double, true},
		{"b", parquet.Boolean, true},
		{"ts", parquet.Int64, true},
		{ColExtractedAt, parquet.Int64, false},
		{ColWindowStart, parquet.Int64, false},
		{ColWindowEnd, parquet.Int64, false},
		{ColSourceFile, parquet.ByteArray, false},
	} {
		lc, ok := pf.schema.Lookup(e.name)
		if !ok {
			t.Fatalf("schema has no column %q", e.name)
		}
		if lc.Node.Type().Kind() != e.kind {
			t.Errorf("column %q physical Kind = %v, want %v", e.name, lc.Node.Type().Kind(), e.kind)
		}
		if lc.Node.Optional() != e.optional {
			t.Errorf("column %q Optional() = %v, want %v", e.name, lc.Node.Optional(), e.optional)
		}
		if lc.Node.Required() == e.optional {
			t.Errorf("column %q Required() = %v, want %v (complement of Optional)", e.name, lc.Node.Required(), !e.optional)
		}
	}

	// Logical types called out by AC-07: String's String logical type, Timestamp's
	// isAdjustedToUTC=false, provenance timestamps' isAdjustedToUTC=true, _source_file's String
	// logical type.
	assertStringLogicalType(t, pf.schema, "s")
	assertStringLogicalType(t, pf.schema, ColSourceFile)
	assertTimestampLogicalType(t, pf.schema, "ts", false)
	assertTimestampLogicalType(t, pf.schema, ColExtractedAt, true)
	assertTimestampLogicalType(t, pf.schema, ColWindowStart, true)
	assertTimestampLogicalType(t, pf.schema, ColWindowEnd, true)

	// Values.
	rows := pf.readRows(t)
	if len(rows) != 2 {
		t.Fatalf("file has %d rows, want 2", len(rows))
	}

	r0 := rows[0]
	if r0["s"].IsNull() || string(r0["s"].ByteArray()) != "hello" {
		t.Errorf(`row 0 "s" = %v, want "hello"`, r0["s"])
	}
	if r0["i"].IsNull() || r0["i"].Int64() != 42 {
		t.Errorf(`row 0 "i" = %v, want 42`, r0["i"])
	}
	if r0["f"].IsNull() || r0["f"].Double() != 3.5 {
		t.Errorf(`row 0 "f" = %v, want 3.5`, r0["f"])
	}
	if r0["b"].IsNull() || !r0["b"].Boolean() {
		t.Errorf(`row 0 "b" = %v, want true`, r0["b"])
	}
	if r0["ts"].IsNull() || !timestampValue(r0["ts"]).Equal(ts) {
		t.Errorf(`row 0 "ts" = %v, want %v`, timestampValue(r0["ts"]), ts)
	}

	r1 := rows[1]
	for _, name := range []string{"s", "i", "f", "b", "ts"} {
		if !r1[name].IsNull() {
			t.Errorf("row 1 column %q = %v, want NULL", name, r1[name])
		}
	}
	// Provenance is never NULL, even when every batch column in the row is NULL.
	for _, name := range []string{ColExtractedAt, ColWindowStart, ColWindowEnd, ColSourceFile} {
		if r1[name].IsNull() {
			t.Errorf("row 1 provenance column %q is NULL, want a value", name)
		}
	}
}

// AC-07: a zero-row batch writes a valid, readable file carrying the full schema (batch columns
// plus all four provenance columns) — the schema is not degenerate just because there are no
// rows to infer it from.
func TestWrite_ZeroRowBatchWritesFullSchema_AC07(t *testing.T) {
	root := t.TempDir()
	wr := &Writer{Root: root}
	w := mustHourWindow(2026, 9, 15, 13)
	batch := record.Batch{
		Columns: []record.Column{
			{Name: "s", Kind: record.String},
			{Name: "i", Kind: record.Int64},
		},
		Rows: nil,
	}
	res, err := wr.Write(context.Background(), Raw, "sample/kinds", w, batch, time.Date(2026, 9, 15, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.Rows != 0 {
		t.Errorf("Result.Rows = %d, want 0", res.Rows)
	}

	pf := openWritten(t, root, res.Path)
	rows := pf.readRows(t)
	if len(rows) != 0 {
		t.Errorf("read back %d rows from a zero-row write, want 0", len(rows))
	}
	for _, name := range []string{"s", "i", ColExtractedAt, ColWindowStart, ColWindowEnd, ColSourceFile} {
		if _, ok := pf.schema.Lookup(name); !ok {
			t.Errorf("zero-row file's schema is missing column %q", name)
		}
	}
}

// --- Hardening (round 2): test-breaker round-1 survivors -------------------------------------
//
// Each test below targets one specific survivor from
// docs/fable-streams/2026-09-18-phase-1-build/tasks/02-writer/report.md's "## test-breaker —
// round 1" sections (three concurrent breaker runs, breaker-0/1/2). Every one of these tests
// passes against the accepted Writer and was verified, in a throwaway scratch copy under
// local/_testadv/ (never committed), to fail against the exact mutation the named survivor
// describes — except TestWrite_LargeBatchGoldenBytes_AC04, whose own doc comment explains why its
// named survivor (breaker-1 v3) is disputed rather than closed.

// largeBatchGoldenSHA256 is the SHA-256 of TestWrite_LargeBatchGoldenBytes_AC04's 60,000-row file,
// recorded from the accepted Writer (parquet-go v0.32.0, zstd.Codec{Level: SpeedDefault,
// Concurrency: 1}).
const largeBatchGoldenSHA256 = "ecc4ddda5b9efcd8737562b7305a3b2512fa3997ad9cb8ef467a5b30461906f8"

// AC-04 ("fixed writer options"): a large batch's bytes must still match a recorded hash, the same
// property TestWrite_GoldenBytes_AC04 checks at a handful of rows, but at a scale (60,000 rows,
// several hundred KB of page data) that crosses klauspost/zstd's internal block-splitting threshold
// (128KB) and the parquet-go page buffer's default flush size (256KB) — so this test is strictly
// more sensitive than the small golden test to any writer option that only diverges once the
// encoder actually has to split its input into multiple blocks.
//
// NOTE on test-breaker round 1 / breaker-1 v3 ("ZSTD Concurrency tied to runtime.NumCPU() instead
// of pinned at 1"): this test does NOT close that survivor, and no behavioral test of Write's
// output bytes can, for the dependency versions pinned in this repo's go.mod. See this round's
// report for the reproduction that shows why (a dispute, not a fix).
func TestWrite_LargeBatchGoldenBytes_AC04(t *testing.T) {
	root := t.TempDir()
	w := mustHourWindow(2026, 9, 15, 19)
	const n = 60000
	col := []record.Column{{Name: "id", Kind: record.Int64}}
	rows := make([][]any, n)
	for i := 0; i < n; i++ {
		rows[i] = []any{int64(i)}
	}
	batch := record.Batch{Columns: col, Rows: rows}
	extractedAt := time.Date(2026, 9, 15, 19, 5, 0, 0, time.UTC)

	res, err := (&Writer{Root: root}).Write(context.Background(), Raw, "beads/events", w, batch, extractedAt)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := sha256File(t, filepath.Join(root, res.Path)); got != largeBatchGoldenSHA256 {
		t.Errorf("60,000-row batch hashed to %s, want the recorded %s (see this test's comment)", got, largeBatchGoldenSHA256)
	}
}

// AC-04 ("keep the ... row order exactly as given" / breaker-1 v2 — rows silently reordered once a
// batch exceeds 3 rows). Every existing row-order-sensitive fixture (TestWrite_DeterministicBytes_AC04,
// TestWrite_GoldenBytes_AC04, TestWrite_OverwriteIsAtomicNotAppend_AC05, TestWrite_ProvenanceColumns_AC06,
// TestWrite_TypeRoundTrip_AC07) tops out at 3 rows, so a reordering bug gated on "len(rows) > 3" never
// fires against them. This test writes 7 rows with deliberately non-monotonic, non-palindromic
// (id, label) pairs, so a reversal, a sort, or any other reordering is directly observable by comparing
// read-back position against input position, not just by hashing the whole file.
func TestWrite_RowOrderPreservedAboveSmallBatches_AC04(t *testing.T) {
	root := t.TempDir()
	wr := &Writer{Root: root}
	w := mustHourWindow(2026, 9, 15, 18)
	col := []record.Column{{Name: "id", Kind: record.Int64}, {Name: "label", Kind: record.String}}
	want := []struct {
		id    int64
		label string
	}{
		{5, "e"}, {1, "a"}, {4, "d"}, {2, "b"}, {7, "g"}, {3, "c"}, {6, "f"},
	}
	rows := make([][]any, len(want))
	for i, r := range want {
		rows[i] = []any{r.id, r.label}
	}
	batch := record.Batch{Columns: col, Rows: rows}

	res, err := wr.Write(context.Background(), Raw, "beads/events", w, batch, time.Date(2026, 9, 15, 18, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.Rows != len(want) {
		t.Fatalf("Result.Rows = %d, want %d", res.Rows, len(want))
	}

	pf := openWritten(t, root, res.Path)
	got := pf.readRows(t)
	if len(got) != len(want) {
		t.Fatalf("file has %d rows, want %d", len(got), len(want))
	}
	for i, w := range want {
		gid, glabel := got[i]["id"], got[i]["label"]
		if gid.IsNull() || gid.Int64() != w.id || glabel.IsNull() || string(glabel.ByteArray()) != w.label {
			t.Errorf("row %d = (id=%v, label=%v), want (id=%d, label=%q) (rows must keep the batch's given order)",
				i, gid, glabel, w.id, w.label)
		}
	}
}

// AC-07 ("NULLs stay NULL" / breaker-1 v4 — explicit empty string "" collapsed into NULL, and v5 —
// explicit int64(0) collapsed into NULL). TestWrite_TypeRoundTrip_AC07's only String values are
// "hello" and nil, and its only numeric values are non-zero, so a definition-level check that treats
// a "falsy" value as absent (v == "" or v == int64(0), alongside the nil check) slips past every
// existing fixture. This test supplies the explicit falsy value for every kind in one row and asserts
// each comes back as its own value, not NULL.
func TestWrite_FalsyValuesRoundTripNotNull_AC07(t *testing.T) {
	root := t.TempDir()
	wr := &Writer{Root: root}
	w := mustHourWindow(2026, 9, 15, 22)
	batch := record.Batch{
		Columns: []record.Column{
			{Name: "s", Kind: record.String},
			{Name: "i", Kind: record.Int64},
			{Name: "f", Kind: record.Float64},
			{Name: "b", Kind: record.Bool},
		},
		Rows: [][]any{
			{"", int64(0), 0.0, false},
		},
	}
	res, err := wr.Write(context.Background(), Raw, "sample/kinds", w, batch, time.Date(2026, 9, 15, 22, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	pf := openWritten(t, root, res.Path)
	rows := pf.readRows(t)
	if len(rows) != 1 {
		t.Fatalf("file has %d rows, want 1", len(rows))
	}
	row := rows[0]

	if row["s"].IsNull() {
		t.Error(`column "s" = NULL, want the empty string "" (breaker-1 v4: an explicit "" must not collapse to NULL)`)
	} else if got := string(row["s"].ByteArray()); got != "" {
		t.Errorf(`column "s" = %q, want ""`, got)
	}
	if row["i"].IsNull() {
		t.Error(`column "i" = NULL, want 0 (breaker-1 v5: an explicit int64(0) must not collapse to NULL)`)
	} else if got := row["i"].Int64(); got != 0 {
		t.Errorf(`column "i" = %d, want 0`, got)
	}
	if row["f"].IsNull() {
		t.Error(`column "f" = NULL, want 0 (the same falsy-value class as breaker-1 v4/v5, for Float64)`)
	} else if got := row["f"].Double(); got != 0 {
		t.Errorf(`column "f" = %v, want 0`, got)
	}
	if row["b"].IsNull() {
		t.Error(`column "b" = NULL, want false (the same falsy-value class as breaker-1 v4/v5, for Bool)`)
	} else if got := row["b"].Boolean(); got {
		t.Errorf(`column "b" = %v, want false`, got)
	}
}

// AC-07 ("written from the value's wall-clock fields, unchanged" / breaker-1 v6 — wallClockMicros
// rounds a sub-microsecond nanosecond remainder instead of truncating it). Every batch Timestamp
// value in the existing suite already lands exactly on a microsecond boundary, so rounding and
// truncating agree on every fixture. This test uses a genuine 789ns sub-microsecond remainder, where
// truncation gives .123456 and rounding gives .123457 — a real, one-microsecond-observable difference.
func TestWrite_TimestampTruncatesNotRounds_AC07(t *testing.T) {
	root := t.TempDir()
	wr := &Writer{Root: root}
	w := mustHourWindow(2026, 9, 15, 23)
	ts := time.Date(2026, 9, 15, 13, 45, 30, 123456789, time.UTC)
	batch := record.Batch{
		Columns: []record.Column{{Name: "ts", Kind: record.Timestamp}},
		Rows:    [][]any{{ts}},
	}
	res, err := wr.Write(context.Background(), Raw, "sample/kinds", w, batch, time.Date(2026, 9, 15, 23, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	pf := openWritten(t, root, res.Path)
	rows := pf.readRows(t)
	if len(rows) != 1 {
		t.Fatalf("file has %d rows, want 1", len(rows))
	}
	want := time.Date(2026, 9, 15, 13, 45, 30, 123456000, time.UTC) // truncated; rounding would give ...457000
	got := timestampValue(rows[0]["ts"])
	if !got.Equal(want) {
		t.Errorf("ts = %v, want %v (truncated to microseconds, not rounded)", got, want)
	}
}

// --- AC-05 durability: fsync before rename (breaker-2 v5) --------------------------------------
//
// IMPLEMENTATION HOOK: TestWrite_SyncFailureLeavesPriorFileIntact_AC05 sets the package-level
// variable forceSyncErr, declared in writer.go beside forceEncodeErr. When non-nil, replaceFile
// takes it as the result of the durability sync and does not call tmp.Sync() at all, so the test
// proves the sync step's call site is reached and its failure is handled like any other
// pre-rename failure (AC-05). It cannot prove that a build which deletes the tmp.Sync() call
// loses durability: that mutant is an accepted blind spot of this suite (escalations.md,
// 02-writer). Always nil in production; only this package's tests set it, and they reset it.
//
// Why this test is needed: test-breaker round 1 / breaker-2 v5 removed the writer's single
// tmp.Sync() call. Every existing AC-05 test (Overwrite, EncodeFailure, ContextCancelled) only
// inspects the *final* filesystem state after Write returns — final bytes, row counts, presence of
// ".tmp-*" files, error/non-error outcomes — none of which a missing fsync changes in a normal,
// non-crashing `go test` run (the OS page cache already serves the unsynced bytes, and os.Rename
// still atomically swaps the unsynced temp file over the target). No test in this suite can observe
// the durability gap itself without either crash injection or a seam at the sync call site; this is
// the latter.
func TestWrite_SyncFailureLeavesPriorFileIntact_AC05(t *testing.T) {
	root := t.TempDir()
	wr := &Writer{Root: root}
	w := mustHourWindow(2026, 9, 15, 21)
	col := []record.Column{{Name: "id", Kind: record.Int64}}

	good := record.Batch{Columns: col, Rows: [][]any{{int64(1)}}}
	res, err := wr.Write(context.Background(), Raw, "beads/events", w, good, time.Date(2026, 9, 15, 21, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("initial (control) Write: %v", err)
	}
	abs := filepath.Join(root, res.Path)
	before := sha256File(t, abs)

	old := forceSyncErr
	forceSyncErr = errors.New("bronze_test: injected sync failure")
	t.Cleanup(func() { forceSyncErr = old })

	changed := record.Batch{Columns: col, Rows: [][]any{{int64(2)}, {int64(3)}}}
	_, err = wr.Write(context.Background(), Raw, "beads/events", w, changed, time.Date(2026, 9, 15, 21, 6, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("Write with forceSyncErr set returned a nil error, want the injected failure")
	}

	after := sha256File(t, abs)
	if after != before {
		t.Errorf("target file changed after a failed-sync write: before=%s after=%s", before, after)
	}
	assertNoTempFiles(t, filepath.Dir(abs))

	// The same failure on a window that has no file yet leaves no file at all.
	fresh := mustHourWindow(2026, 9, 15, 22)
	if _, err := wr.Write(context.Background(), Raw, "beads/events", fresh, changed, time.Date(2026, 9, 15, 22, 6, 0, 0, time.UTC)); err == nil {
		t.Fatal("first Write of a fresh window with forceSyncErr set returned a nil error")
	}
	assertNoFileForWindow(t, root, fresh)
}

// --- Required content: sample file for manual DuckDB inspection --------------------------------

// TestWriteSampleForInspection is required content (brief's "Required content" section, exercised
// by the task's DuckDB verification command), not itself an AC-numbered behavioral test: when
// BRONZE_SAMPLE_OUT is set, it writes one sample hourly-window file — zone raw, dataset
// "sample/kinds", one column of each Kind, one row that is all NULLs — under that directory as
// the bronze root, and logs the absolute path so the verification command can `duckdb -c
// "describe ..."` / `select *` on it. It is skipped otherwise so it never runs in ordinary `go
// test` invocations.
func TestWriteSampleForInspection(t *testing.T) {
	dir := os.Getenv("BRONZE_SAMPLE_OUT")
	if dir == "" {
		t.Skip("BRONZE_SAMPLE_OUT not set; skipping (this test only produces a manual-inspection sample)")
	}
	w := mustHourWindow(2026, 9, 15, 13)
	batch := record.Batch{
		Columns: []record.Column{
			{Name: "a_string", Kind: record.String},
			{Name: "an_int64", Kind: record.Int64},
			{Name: "a_float64", Kind: record.Float64},
			{Name: "a_bool", Kind: record.Bool},
			{Name: "a_timestamp", Kind: record.Timestamp},
		},
		Rows: [][]any{
			{nil, nil, nil, nil, nil},
		},
	}
	wr := &Writer{Root: dir}
	res, err := wr.Write(context.Background(), Raw, "sample/kinds", w, batch, time.Date(2026, 9, 15, 13, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.Rows != 1 {
		t.Fatalf("Result.Rows = %d, want 1", res.Rows)
	}
	abs := filepath.Join(dir, res.Path)
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("sample file not found at %s: %v", abs, err)
	}
	t.Logf("sample bronze file written to %s", abs)
}
