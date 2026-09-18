package transform

// Behavioral tests for transform.Materialise, anchored to AC-30 in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md ("Parquet in, bronze/derived out": a
// transform's output goes through the shared Writer, provenance included, and a re-run overwrites
// that file) and the "internal/transform" bullet of tasks/07-go-transform/brief.md's Required
// content, which pins Materialise's exact signature:
//
//	func Materialise(ctx context.Context, t Transform, env Env, wr *bronze.Writer, w window.Window, extractedAt time.Time) (bronze.Result, error)
//	// Materialise runs t for window w and writes its output to the derived zone through the shared
//	// Writer: Writer.Write(ctx, bronze.Derived, t.Contract().Output, w, batch, extractedAt).
//
// This is a white-box test (package transform, not transform_test) only because it is the natural
// home for a stub Transform used purely as a test double for Materialise's own wiring; every
// assertion below reads Materialise's *return value* (bronze.Result) and the bytes it actually put
// on disk through parquet-go's public reader API, never any unexported Materialise state, and the
// stub's own output is what the file's bytes are checked against -- not whether the stub was
// "called" in some abstract sense.
//
// Contract, Env and the Transform interface are exactly as specified by the brief; this file
// requires no additional implementation hooks beyond that exact API (Contract.Name/Inputs/
// Output/Grain/Tests, Env.WarehouseRoot, Transform.Contract()/Run(), and Materialise itself). Until
// transform.go declares them, every test below fails to build with "undefined: Contract" (or Env,
// Transform, Materialise) -- the expected Mode-A result for a brand-new package with no
// implementation yet.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/window"
)

// stubTransform is a Transform test double whose Run delegates to an injected function, so each
// test controls exactly what "the transform produced" without depending on any real transform's
// business logic (mentions, or any future one).
type stubTransform struct {
	contract Contract
	run      func(ctx context.Context, env Env, w window.Window) (record.Batch, error)
}

func (s stubTransform) Contract() Contract { return s.contract }

func (s stubTransform) Run(ctx context.Context, env Env, w window.Window) (record.Batch, error) {
	return s.run(ctx, env, w)
}

// readSingleStringColumn opens the bronze file at abs and returns the value of column name from
// its one and only data row, plus the file's own _extracted_at and _source_file provenance values,
// so tests can confirm Materialise really did write through the shared Writer (not some parallel
// path) with the batch and extractedAt it was given.
func readSingleStringColumn(t *testing.T, abs, name string) (value string, extractedAt time.Time, sourceFile string) {
	t.Helper()
	f, err := os.Open(abs)
	if err != nil {
		t.Fatalf("open %s: %v", abs, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat %s: %v", abs, err)
	}
	pf, err := parquet.OpenFile(f, info.Size())
	if err != nil {
		t.Fatalf("parquet.OpenFile(%s): %v", abs, err)
	}
	cols := pf.Schema().Columns()
	reader := parquet.NewReader(pf)
	defer reader.Close()
	buf := make([]parquet.Row, 1)
	n, err := reader.ReadRows(buf)
	if n != 1 {
		t.Fatalf("ReadRows(%s) returned %d rows (err=%v), want 1", abs, n, err)
	}
	row := buf[0]
	m := make(map[string]parquet.Value, len(cols))
	for _, v := range row {
		m[cols[v.Column()][0]] = v
	}
	v, ok := m[name]
	if !ok || v.IsNull() {
		t.Fatalf("row has no non-null column %q; row=%v", name, m)
	}
	extracted, ok := m[bronze.ColExtractedAt]
	if !ok || extracted.IsNull() {
		t.Fatalf("row has no %s provenance column", bronze.ColExtractedAt)
	}
	src, ok := m[bronze.ColSourceFile]
	if !ok || src.IsNull() {
		t.Fatalf("row has no %s provenance column", bronze.ColSourceFile)
	}
	return string(v.ByteArray()), time.UnixMicro(extracted.Int64()).UTC(), string(src.ByteArray())
}

// dirEntries lists the base names of every entry directly under dir (empty slice, not an error,
// if dir does not exist).
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names
}

// AC-30: Materialise runs t.Run with the given env and w, then writes the resulting batch to the
// derived zone through the shared Writer, at the exact path bronze.Path(bronze.Derived,
// t.Contract().Output, w) derives, with the given extractedAt as provenance. The stub's own output
// encodes both env.WarehouseRoot and w.String(), so the written file's content proves Materialise
// actually threads its env and w arguments into Run -- not merely that some batch was written.
func TestMaterialise_WritesThroughSharedWriter_AC30(t *testing.T) {
	bronzeRoot := t.TempDir()
	warehouseRoot := t.TempDir()
	wr := &bronze.Writer{Root: bronzeRoot}
	env := Env{WarehouseRoot: warehouseRoot}
	w := window.Containing(time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC), window.Day)
	extractedAt := time.Date(2026, 9, 18, 12, 30, 0, 123456789, time.UTC) // sub-microsecond remainder

	contract := Contract{Name: "stub_transform", Inputs: []string{"dim_issues"}, Output: "stub_output", Grain: window.Day}
	stub := stubTransform{
		contract: contract,
		run: func(_ context.Context, env Env, w window.Window) (record.Batch, error) {
			return record.Batch{
				Columns: []record.Column{{Name: "marker", Kind: record.String}},
				Rows:    [][]any{{env.WarehouseRoot + "|" + w.String()}},
			}, nil
		},
	}

	res, err := Materialise(context.Background(), stub, env, wr, w, extractedAt)
	if err != nil {
		t.Fatalf("Materialise: %v", err)
	}

	wantPath, err := bronze.Path(bronze.Derived, "stub_output", w)
	if err != nil {
		t.Fatalf("bronze.Path: %v", err)
	}
	if res.Path != wantPath {
		t.Errorf("Result.Path = %q, want %q (Writer.Write(ctx, bronze.Derived, t.Contract().Output, w, ...))", res.Path, wantPath)
	}
	if res.Rows != 1 {
		t.Errorf("Result.Rows = %d, want 1", res.Rows)
	}

	marker, gotExtractedAt, sourceFile := readSingleStringColumn(t, filepath.Join(bronzeRoot, res.Path), "marker")
	wantMarker := warehouseRoot + "|" + w.String()
	if marker != wantMarker {
		t.Errorf("written marker = %q, want %q (Materialise must pass its own env and w through to Run)", marker, wantMarker)
	}
	wantExtractedAt := time.Date(2026, 9, 18, 12, 30, 0, 123456000, time.UTC) // truncated to microseconds by the Writer
	if !gotExtractedAt.Equal(wantExtractedAt) {
		t.Errorf("written _extracted_at = %v, want %v (the extractedAt Materialise was given)", gotExtractedAt, wantExtractedAt)
	}
	if sourceFile != res.Path {
		t.Errorf("written _source_file = %q, want %q", sourceFile, res.Path)
	}
}

// AC-30: "A re-run overwrites that file." Materialising the same transform/window twice leaves
// exactly one file at the derived path, holding only the second run's batch.
func TestMaterialise_RerunOverwrites_AC30(t *testing.T) {
	bronzeRoot := t.TempDir()
	wr := &bronze.Writer{Root: bronzeRoot}
	env := Env{WarehouseRoot: t.TempDir()}
	w := window.Containing(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), window.Day)
	contract := Contract{Name: "stub_rerun", Inputs: []string{"dim_issues"}, Output: "stub_rerun_output", Grain: window.Day}

	makeStub := func(marker string) stubTransform {
		return stubTransform{
			contract: contract,
			run: func(context.Context, Env, window.Window) (record.Batch, error) {
				return record.Batch{
					Columns: []record.Column{{Name: "marker", Kind: record.String}},
					Rows:    [][]any{{marker}},
				}, nil
			},
		}
	}

	res1, err := Materialise(context.Background(), makeStub("first"), env, wr, w, time.Date(2026, 9, 18, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("first Materialise: %v", err)
	}
	res2, err := Materialise(context.Background(), makeStub("second"), env, wr, w, time.Date(2026, 9, 18, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("second Materialise: %v", err)
	}
	if res1.Path != res2.Path {
		t.Fatalf("second Materialise targeted a different path: %q vs %q", res2.Path, res1.Path)
	}

	dir := filepath.Join(bronzeRoot, filepath.Dir(res2.Path))
	entries := dirEntries(t, dir)
	if len(entries) != 1 {
		t.Fatalf("directory %s holds %v, want exactly one file (overwrite, not a sibling)", dir, entries)
	}

	marker, _, _ := readSingleStringColumn(t, filepath.Join(bronzeRoot, res2.Path), "marker")
	if marker != "second" {
		t.Errorf("surviving marker = %q, want %q (the second run's batch, not the first's)", marker, "second")
	}
}

// AC-30 (reliability the overwrite guarantee depends on): when a Transform's Run itself fails,
// Materialise must return that error and must not write a partial or stale-looking derived file --
// otherwise a downstream dbt build would treat the failed run's leftover file as valid data.
func TestMaterialise_RunErrorLeavesNoFile_AC30(t *testing.T) {
	bronzeRoot := t.TempDir()
	wr := &bronze.Writer{Root: bronzeRoot}
	env := Env{WarehouseRoot: t.TempDir()}
	w := window.Containing(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), window.Day)
	contract := Contract{Name: "stub_fail", Inputs: []string{"dim_issues"}, Output: "stub_fail_output", Grain: window.Day}
	wantErr := errors.New("transform_test: injected Run failure")
	stub := stubTransform{
		contract: contract,
		run: func(context.Context, Env, window.Window) (record.Batch, error) {
			return record.Batch{}, wantErr
		},
	}

	_, err := Materialise(context.Background(), stub, env, wr, w, time.Now())
	if err == nil {
		t.Fatal("Materialise returned a nil error when Run failed, want the Run error propagated")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Materialise error = %v, want it to wrap %v (errors.Is)", err, wantErr)
	}

	wantPath, err2 := bronze.Path(bronze.Derived, "stub_fail_output", w)
	if err2 != nil {
		t.Fatalf("bronze.Path: %v", err2)
	}
	if _, statErr := os.Stat(filepath.Join(bronzeRoot, wantPath)); !os.IsNotExist(statErr) {
		t.Errorf("a failed Run left a file at %s (stat err = %v), want no file", wantPath, statErr)
	}
}
