package orchestrator

// White-box tests of the spool codec (claim-check storage, AC-33): lossless for every value a
// record.Batch can hold, atomic, and strict about what it reads back.

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/window"
)

func testWindow() window.Window {
	start := time.Date(2026, 9, 15, 13, 0, 0, 0, time.UTC)
	return window.Window{Start: start, End: start.Add(time.Hour), Grain: window.Hour}
}

// sameValue compares two batch values exactly: same Go type, same bits for floats (so -0 and NaN
// count), same instant, wall fields and location for times.
func sameValue(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case float64:
		y, ok := b.(float64)
		return ok && math.Float64bits(x) == math.Float64bits(y)
	case time.Time:
		y, ok := b.(time.Time)
		return ok && x.Equal(y) && x.Location() == y.Location() &&
			x.Format(time.RFC3339Nano) == y.Format(time.RFC3339Nano)
	default:
		return a == b
	}
}

func TestSpoolCodec_LosslessEdgeValues(t *testing.T) {
	cols := []record.Column{
		{Name: "s", Kind: record.String},
		{Name: "i", Kind: record.Int64},
		{Name: "f", Kind: record.Float64},
		{Name: "b", Kind: record.Bool},
		{Name: "ts", Kind: record.Timestamp},
	}
	rows := [][]any{
		{"", int64(0), 0.0, false, time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"héllo\x00wörld ✓", int64(math.MaxInt64), math.Copysign(0, -1), true, time.Date(2026, 9, 15, 13, 45, 30, 123456789, time.UTC)},
		{strings.Repeat("x", 1<<16), int64(math.MinInt64), math.NaN(), true, time.Date(1, 1, 1, 0, 0, 0, 1, time.UTC)},
		{"{\"k\": [1, 2]}", int64(-1), math.Inf(-1), false, time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)},
		{nil, nil, nil, nil, nil},
		{"after-null", nil, math.SmallestNonzeroFloat64, nil, nil},
	}
	fetchedAt := time.Date(2026, 9, 15, 13, 5, 30, 123456000, time.UTC)
	in := spoolFile{Version: spoolVersion, Source: "beads", Table: "dolt_log", Window: testWindow(),
		FetchedAt: fetchedAt, Columns: cols, Rows: rows}

	path := filepath.Join(t.TempDir(), "beads", "dolt_log", "x.gob")
	if err := writeSpool(path, in); err != nil {
		t.Fatalf("writeSpool: %v", err)
	}
	out, err := readSpool(path)
	if err != nil {
		t.Fatalf("readSpool: %v", err)
	}

	if out.Source != in.Source || out.Table != in.Table || !out.FetchedAt.Equal(fetchedAt) || out.Window != in.Window {
		t.Errorf("header = %s/%s %v %v, want %s/%s %v %v", out.Source, out.Table, out.Window, out.FetchedAt,
			in.Source, in.Table, in.Window, in.FetchedAt)
	}
	if len(out.Columns) != len(cols) {
		t.Fatalf("columns = %v, want %v", out.Columns, cols)
	}
	for i := range cols {
		if out.Columns[i] != cols[i] {
			t.Errorf("column %d = %v, want %v", i, out.Columns[i], cols[i])
		}
	}
	if len(out.Rows) != len(rows) {
		t.Fatalf("rows = %d, want %d", len(out.Rows), len(rows))
	}
	for r := range rows {
		for c := range cols {
			if !sameValue(rows[r][c], out.Rows[r][c]) {
				t.Errorf("row %d col %s = %#v (%T), want %#v (%T)", r, cols[c].Name, out.Rows[r][c], out.Rows[r][c], rows[r][c], rows[r][c])
			}
		}
	}
	if err := out.batch().Validate(); err != nil {
		t.Errorf("decoded batch does not validate: %v", err)
	}
}

func TestSpoolCodec_ZeroRowBatch(t *testing.T) {
	in := spoolFile{Version: spoolVersion, Source: "beads", Table: "events", Window: testWindow(),
		FetchedAt: time.Date(2026, 9, 15, 13, 0, 0, 0, time.UTC),
		Columns:   []record.Column{{Name: "id", Kind: record.String}}}
	path := filepath.Join(t.TempDir(), "empty.gob")
	if err := writeSpool(path, in); err != nil {
		t.Fatalf("writeSpool: %v", err)
	}
	out, err := readSpool(path)
	if err != nil {
		t.Fatalf("readSpool: %v", err)
	}
	if len(out.Rows) != 0 || len(out.Columns) != 1 {
		t.Errorf("got %d rows / %d columns, want 0 / 1", len(out.Rows), len(out.Columns))
	}
	if err := out.matches(SpoolRef{Source: "beads", Table: "events", Window: in.Window, FetchedAt: in.FetchedAt}); err != nil {
		t.Errorf("matches: %v", err)
	}
}

// writeSpool replaces an existing spool atomically and leaves no temp file behind.
func TestWriteSpool_AtomicReplaceNoTempLeft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b.gob")
	first := spoolFile{Version: spoolVersion, Source: "beads", Table: "events", Window: testWindow(),
		Columns: []record.Column{{Name: "n", Kind: record.Int64}}, Rows: [][]any{{int64(1)}}}
	second := first
	second.Rows = [][]any{{int64(2)}, {int64(3)}}
	if err := writeSpool(path, first); err != nil {
		t.Fatalf("first writeSpool: %v", err)
	}
	if err := writeSpool(path, second); err != nil {
		t.Fatalf("second writeSpool: %v", err)
	}
	out, err := readSpool(path)
	if err != nil {
		t.Fatalf("readSpool: %v", err)
	}
	if len(out.Rows) != 2 {
		t.Errorf("spool holds %d rows, want the second write's 2", len(out.Rows))
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "b.gob" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("spool dir holds %v, want only b.gob", names)
	}
}

func TestReadSpool_RejectsOtherVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.gob")
	if err := writeSpool(path, spoolFile{Version: spoolVersion + 1}); err != nil {
		t.Fatalf("writeSpool: %v", err)
	}
	if _, err := readSpool(path); err == nil {
		t.Fatalf("readSpool accepted a spool of another version")
	}
}

func TestSpoolFile_MatchesRejectsOtherFetch(t *testing.T) {
	w := testWindow()
	at := time.Date(2026, 9, 15, 13, 5, 0, 0, time.UTC)
	sf := spoolFile{Version: spoolVersion, Source: "beads", Table: "events", Window: w, FetchedAt: at,
		Rows: [][]any{{int64(1)}}}
	ok := SpoolRef{Source: "beads", Table: "events", Window: w, FetchedAt: at, Rows: 1}
	if err := sf.matches(ok); err != nil {
		t.Fatalf("matches(own ref): %v", err)
	}
	next := w
	next.Start, next.End = w.End, w.End.Add(time.Hour)
	for name, ref := range map[string]SpoolRef{
		"table":     {Source: "beads", Table: "dolt_log", Window: w, FetchedAt: at, Rows: 1},
		"window":    {Source: "beads", Table: "events", Window: next, FetchedAt: at, Rows: 1},
		"fetchedAt": {Source: "beads", Table: "events", Window: w, FetchedAt: at.Add(time.Microsecond), Rows: 1},
		"rows":      {Source: "beads", Table: "events", Window: w, FetchedAt: at, Rows: 2},
	} {
		if err := sf.matches(ref); err == nil {
			t.Errorf("matches accepted a ref with a different %s", name)
		}
	}
}

func TestSpoolPath_ShapeAndSanitising(t *testing.T) {
	got := spoolPath("/r/local/spool", "beads", "events", testWindow(), "0f1e-run", "12")
	if want := "/r/local/spool/beads/events/2026-09-15T13-0f1e-run-12.gob"; got != want {
		t.Errorf("spoolPath = %q, want %q", got, want)
	}
	got = spoolPath("/r/local/spool", "beads", "events", testWindow(), "../../etc", "a/b")
	if filepath.Dir(got) != "/r/local/spool/beads/events" {
		t.Errorf("spoolPath with hostile ids = %q, escapes its table directory", got)
	}
	if pathSafe("") != "_" {
		t.Errorf("pathSafe(\"\") = %q, want \"_\"", pathSafe(""))
	}
}
