package orchestrator_test

// Shared test helpers for internal/orchestrator's behavioral tests (AC-33 and AC-34 in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md). See workflow_materialise_test.go and
// activity_ingest_test.go for the tests themselves. Every helper here only touches exported
// orchestrator symbols and public filesystem/parquet state -- never an unexported orchestrator
// field or function -- so these tests stay behavioral, not structural.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/window"
)

// mustWindow builds a well-formed Window of Grain g starting at the given UTC instant, failing
// the test if the result does not validate (guards against a typo in a test's literal date).
func mustWindow(t *testing.T, y int, m time.Month, d, hh int, g window.Grain) window.Window {
	t.Helper()
	start := time.Date(y, m, d, hh, 0, 0, 0, time.UTC)
	w := window.Window{Start: start, End: start.Add(g.Duration()), Grain: g}
	if err := w.Validate(); err != nil {
		t.Fatalf("mustWindow: invalid window: %v", err)
	}
	return w
}

// stubFetchCall records one call a stubFetcher received.
type stubFetchCall struct {
	Table  string
	Window window.Window
}

// stubFetcher is a fake source.Fetcher (github.com/DMokong/data-platform/internal/source.Fetcher):
// it returns a fixed batch (or a fixed error) for any table/window, and records every call it
// received so a test can assert what an activity actually asked it to fetch. It is safe for
// concurrent use, since a workflow-level test may exercise several tables in parallel.
type stubFetcher struct {
	mu    sync.Mutex
	batch record.Batch
	err   error
	calls []stubFetchCall
}

func (f *stubFetcher) Fetch(ctx context.Context, table string, w window.Window) (record.Batch, error) {
	f.mu.Lock()
	f.calls = append(f.calls, stubFetchCall{Table: table, Window: w})
	f.mu.Unlock()
	if f.err != nil {
		return record.Batch{}, f.err
	}
	return f.batch, nil
}

func (f *stubFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// sha256File hashes a file's contents, for byte-identical comparisons across two writes.
func sha256File(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// readParquetRows reads every row of a bronze Parquet file into column-name-keyed maps of
// parquet.Value. This is the same read-back technique internal/bronze's own writer_test.go uses
// (parquet-go's public reader API, keyed by each Value's own Column() index rather than an
// assumed on-disk column order), so these tests never depend on any unexported bronze or
// orchestrator symbol to inspect what WriteWindow actually put on disk.
func readParquetRows(t *testing.T, path string) []map[string]parquet.Value {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { f.Close() })
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	pf, err := parquet.OpenFile(f, info.Size())
	if err != nil {
		t.Fatalf("parquet.OpenFile(%s): %v", path, err)
	}
	reader := parquet.NewReader(pf)
	t.Cleanup(func() { reader.Close() })
	cols := pf.Schema().Columns()

	var out []map[string]parquet.Value
	buf := make([]parquet.Row, 8)
	for {
		n, err := reader.ReadRows(buf)
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
			t.Fatalf("ReadRows(%s): %v", path, err)
		}
		if n == 0 {
			break
		}
	}
	return out
}

// timestampMicros converts a parquet.Value holding a physical INT64 TIMESTAMP(MICROS) column back
// to a UTC time.Time, for comparing against a wall-clock or instant value a test wrote.
func timestampMicros(v parquet.Value) time.Time {
	return time.UnixMicro(v.Int64()).UTC()
}
