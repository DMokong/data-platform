package beads_test

// Live tests for beads.Fetcher against the real Dolt server (127.0.0.1:49209, db trk), anchored to
// AC-12 ("Snapshots via time travel") and AC-13 ("Deterministic, untransformed batches") in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md, plus the brief's "Live tests (BEADS_LIVE=1,
// against 127.0.0.1:49209 db trk)" bullets. Every test in this file is named TestLive... and
// skips unless BEADS_LIVE=1, per the brief's file-scope instructions and the task's own Done-check
// (`BEADS_LIVE=1 go test -count=1 -run Live ./internal/source/beads/`).
//
// These tests read real rows through beads.NewFetcher/Fetch and, for the "independent" checks
// AC-13 asks for, through a second, hand-opened *sql.DB that never goes through BuildQuery/Fetch
// at all -- so a bug shared between the window-bounds arithmetic and the per-hour tiling would
// still be caught, rather than cancelling itself out on both sides of the comparison.
//
// Black-box (package beads_test).

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/source/beads"
	"github.com/DMokong/data-platform/internal/window"
)

func skipUnlessLive(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_LIVE") != "1" {
		t.Skip("BEADS_LIVE != 1; skipping live Dolt test")
	}
}

func mustLiveFetcher(t *testing.T) *beads.Fetcher {
	t.Helper()
	cfg, err := beads.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	f, err := beads.NewFetcher(cfg)
	if err != nil {
		t.Fatalf("NewFetcher(%+v): %v", cfg, err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// rawDB opens a second, independent connection to the same server for oracle queries that never
// go through beads.BuildQuery or beads.Fetcher.Fetch.
func rawDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg, err := beads.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DSN())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping Dolt at %s:%d: %v", cfg.Host, cfg.Port, err)
	}
	return db
}

func liveHourWindow(start time.Time) window.Window {
	return window.Window{Start: start, End: start.Add(time.Hour), Grain: window.Hour}
}

func liveDayWindow(start time.Time) window.Window {
	return window.Window{Start: start, End: start.Add(24 * time.Hour), Grain: window.Day}
}

// colIndex returns the index of column name in b, failing the test if it is absent.
func colIndex(t *testing.T, b record.Batch, name string) int {
	t.Helper()
	for i, c := range b.Columns {
		if c.Name == name {
			return i
		}
	}
	t.Fatalf("batch has no column %q (columns: %v)", name, b.Columns)
	return -1
}

// AC-13: "A closed hourly window fetched twice gives equal batches." Uses dolt_history_issues in
// 2026-09-02T16, one of the brief's own suggested non-empty windows.
func TestLiveFetch_ClosedHourlyWindowIdempotent_AC13(t *testing.T) {
	skipUnlessLive(t)
	f := mustLiveFetcher(t)
	w := liveHourWindow(time.Date(2026, 9, 2, 16, 0, 0, 0, time.UTC))

	first, err := f.Fetch(context.Background(), "dolt_history_issues", w)
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("first batch fails Validate(): %v", err)
	}
	second, err := f.Fetch(context.Background(), "dolt_history_issues", w)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two fetches of the same closed window differ:\nfirst=%+v\nsecond=%+v", first, second)
	}
}

// AC-12: "issues snapshot for day 2026-09-10 fetched twice gives equal batches, with row count >
// 0."
func TestLiveFetch_IssuesSnapshotIdempotent_AC12(t *testing.T) {
	skipUnlessLive(t)
	f := mustLiveFetcher(t)
	w := liveDayWindow(time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC))

	first, err := f.Fetch(context.Background(), "issues", w)
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("first batch fails Validate(): %v", err)
	}
	if len(first.Rows) == 0 {
		t.Fatal("issues snapshot for 2026-09-10 has 0 rows, want > 0")
	}
	second, err := f.Fetch(context.Background(), "issues", w)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two snapshot fetches of the same day differ:\nfirst=%+v\nsecond=%+v", first, second)
	}
}

// AC-12: "An issues snapshot for day 2026-08-01 returns an error." F12 establishes 2026-08-22 as
// resolvable history for every daily table, so 2026-08-01 is before the table's first commit and
// Dolt reports "table not found"; the error must name the table and window rather than an empty
// batch being returned.
func TestLiveFetch_SnapshotBeforeFirstCommitErrors_AC12(t *testing.T) {
	skipUnlessLive(t)
	f := mustLiveFetcher(t)
	w := liveDayWindow(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))

	got, err := f.Fetch(context.Background(), "issues", w)
	if err == nil {
		t.Fatalf("Fetch(issues, 2026-08-01) returned a nil error and batch %+v, want an error (never an empty batch)", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, "issues") {
		t.Errorf("error %q does not name the table %q", msg, "issues")
	}
	if !strings.Contains(msg, "2026-08-01") {
		t.Errorf("error %q does not name the window (want it to mention 2026-08-01)", msg)
	}
}

// AC-13: "the summed events row count over the 24 windows of UTC day 2026-09-15 equals an
// independent SELECT COUNT(*) FROM events WHERE created_at >= '2026-09-15 10:00:00' AND
// created_at < '2026-09-16 10:00:00'." That literal query and its literal bounds are the brief's
// own text, run here through a hand-opened connection that never touches beads.BuildQuery.
func TestLiveFetch_EventsRowCountMatchesIndependentCount_AC13(t *testing.T) {
	skipUnlessLive(t)
	f := mustLiveFetcher(t)

	dayStart := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	windows, err := window.Range(dayStart, dayStart, window.Hour)
	if err != nil {
		t.Fatalf("window.Range: %v", err)
	}
	if len(windows) != 24 {
		t.Fatalf("window.Range(2026-09-15, 2026-09-15, Hour) produced %d windows, want 24", len(windows))
	}

	var summed int
	for _, w := range windows {
		batch, err := f.Fetch(context.Background(), "events", w)
		if err != nil {
			t.Fatalf("Fetch(events, %s): %v", w, err)
		}
		summed += len(batch.Rows)
	}

	db := rawDB(t)
	var independent int
	oracle := "SELECT COUNT(*) FROM events WHERE created_at >= '2026-09-15 10:00:00' AND created_at < '2026-09-16 10:00:00'"
	if err := db.QueryRowContext(context.Background(), oracle).Scan(&independent); err != nil {
		t.Fatalf("independent oracle query: %v", err)
	}

	if summed == 0 {
		t.Fatal("summed events row count over 24 hourly windows is 0, want > 0 (test would be vacuous)")
	}
	if summed != independent {
		t.Errorf("summed hourly Fetch row count = %d, independent COUNT(*) = %d, want equal", summed, independent)
	}
}

// AC-13: "events.created_at of a known row equals the raw source value; do not shift it to UTC."
// Fetches a small events window, takes its first row's id and created_at, then independently
// re-reads that same row's created_at by primary key (bypassing BuildQuery entirely) and checks
// the two values carry identical wall-clock digits.
//
// The window is 2026-09-15T11Z (Sydney wall time 21:00-22:00), which holds 2 events. The brief's
// example hour 2026-09-15T03Z maps to Sydney 13:00-14:00, which holds none (implementer round 1
// probe, recorded in report.md).
func TestLiveFetch_EventsCreatedAtStaysSourceWallTime_AC13(t *testing.T) {
	skipUnlessLive(t)
	f := mustLiveFetcher(t)
	w := liveHourWindow(time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC))

	batch, err := f.Fetch(context.Background(), "events", w)
	if err != nil {
		t.Fatalf("Fetch(events, %s): %v", w, err)
	}
	if len(batch.Rows) == 0 {
		t.Fatalf("events window %s has 0 rows; pick a different known-nonempty window", w)
	}
	idIdx := colIndex(t, batch, "id")
	createdIdx := colIndex(t, batch, "created_at")

	row := batch.Rows[0]
	// events.id is CHAR (a UUID string), so the batch carries it as a string.
	gotID, ok := row[idIdx].(string)
	if !ok {
		t.Fatalf("row[0] id = %v (%T), want string", row[idIdx], row[idIdx])
	}
	gotCreatedAt, ok := row[createdIdx].(time.Time)
	if !ok {
		t.Fatalf("row[0] created_at = %v (%T), want time.Time", row[createdIdx], row[createdIdx])
	}
	if gotCreatedAt.Location() != time.UTC {
		t.Errorf("row[0] created_at Location = %v, want time.UTC (record.Timestamp always carries time.UTC; F1 keeps the wall-clock digits Sydney-shaped, it never reassigns the Location object)", gotCreatedAt.Location())
	}

	db := rawDB(t)
	var oracleCreatedAt time.Time
	if err := db.QueryRowContext(context.Background(), "SELECT created_at FROM events WHERE id = ?", gotID).Scan(&oracleCreatedAt); err != nil {
		t.Fatalf("independent oracle query for events.id=%s: %v", gotID, err)
	}

	const layout = "2006-01-02 15:04:05"
	if gotCreatedAt.Format(layout) != oracleCreatedAt.Format(layout) {
		t.Errorf("Fetch's created_at = %s, independent read = %s; want identical wall-clock digits (no UTC shift)",
			gotCreatedAt.Format(layout), oracleCreatedAt.Format(layout))
	}
}
