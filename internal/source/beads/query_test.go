package beads_test

// Behavioral tests for beads.BuildQuery, anchored to AC-11 ("Event-log window predicates"),
// AC-12 ("Snapshots via time travel") and AC-13 ("Deterministic, untransformed batches") in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md, plus the brief's "Queries" section, which
// gives the exact SQL text each table must produce. These tests assert on that exact text (and
// the exact Args) rather than loosely matching substrings, so they also prove SQL keywords are
// uppercase and each table's ORDER BY names its documented unique key -- a looser assertion could
// pass with lowercase keywords or a missing tie-breaker column.
//
// BuildQuery is documented as pure (no DB, no clock), so every case here is a plain table-driven
// unit test. Black-box (package beads_test).

import (
	"strings"
	"testing"
	"time"

	"github.com/DMokong/data-platform/internal/source/beads"
	"github.com/DMokong/data-platform/internal/window"
)

func mustSydney(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skipf("Australia/Sydney tzdata unavailable in this environment: %v", err)
	}
	return loc
}

func utc(y int, m time.Month, d, hh, mm, ss int) time.Time {
	return time.Date(y, m, d, hh, mm, ss, 0, time.UTC)
}

func hourWindow(start time.Time) window.Window {
	return window.Window{Start: start, End: start.Add(time.Hour), Grain: window.Hour}
}

func dayWindow(start time.Time) window.Window {
	return window.Window{Start: start, End: start.Add(24 * time.Hour), Grain: window.Day}
}

// AC-11 examples: the two windows the AC spells out, for the events table (Sydney wall-time
// bounds, F1).
func TestBuildQuery_EventsBounds_AC11(t *testing.T) {
	loc := mustSydney(t)
	cases := []struct {
		name      string
		w         window.Window
		wantLower string
		wantUpper string
	}{
		{
			name:      "2026-09-15T03:00Z (pre-DST)",
			w:         hourWindow(utc(2026, 9, 15, 3, 0, 0)),
			wantLower: "2026-09-15 13:00:00",
			wantUpper: "2026-09-15 14:00:00",
		},
		{
			name:      "2026-10-03T16:00Z (Sydney DST start)",
			w:         hourWindow(utc(2026, 10, 3, 16, 0, 0)),
			wantLower: "2026-10-04 03:00:00",
			wantUpper: "2026-10-04 04:00:00",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q, err := beads.BuildQuery("events", c.w, loc)
			if err != nil {
				t.Fatalf("BuildQuery(events, ...) error: %v", err)
			}
			wantSQL := "SELECT * FROM events WHERE created_at >= ? AND created_at < ? ORDER BY created_at, id"
			if q.SQL != wantSQL {
				t.Errorf("SQL = %q, want %q", q.SQL, wantSQL)
			}
			if len(q.Args) != 2 {
				t.Fatalf("len(Args) = %d, want 2 (got %v)", len(q.Args), q.Args)
			}
			if q.Args[0] != c.wantLower {
				t.Errorf("Args[0] = %v, want %q", q.Args[0], c.wantLower)
			}
			if q.Args[1] != c.wantUpper {
				t.Errorf("Args[1] = %v, want %q", q.Args[1], c.wantUpper)
			}
		})
	}
}

// AC-11 example: window 2026-09-15T03:00Z gives dolt_history_issues UTC bounds
// "2026-09-15 03:00:00" / "2026-09-15 04:00:00" -- unlike events, unconverted.
func TestBuildQuery_DoltHistoryIssuesBounds_AC11(t *testing.T) {
	loc := mustSydney(t)
	w := hourWindow(utc(2026, 9, 15, 3, 0, 0))

	q, err := beads.BuildQuery("dolt_history_issues", w, loc)
	if err != nil {
		t.Fatalf("BuildQuery(dolt_history_issues, ...) error: %v", err)
	}
	wantSQL := "SELECT * FROM dolt_history_issues WHERE commit_date >= ? AND commit_date < ? ORDER BY commit_date, commit_hash, id"
	if q.SQL != wantSQL {
		t.Errorf("SQL = %q, want %q", q.SQL, wantSQL)
	}
	if len(q.Args) != 2 || q.Args[0] != "2026-09-15 03:00:00" || q.Args[1] != "2026-09-15 04:00:00" {
		t.Errorf("Args = %v, want [\"2026-09-15 03:00:00\" \"2026-09-15 04:00:00\"] (UTC, unconverted)", q.Args)
	}
}

// AC-11: dolt_log also uses UTC bounds (same rule as dolt_history_issues, spec §4.C), with its
// own two-column ORDER BY.
func TestBuildQuery_DoltLogBounds_AC11(t *testing.T) {
	loc := mustSydney(t)
	w := hourWindow(utc(2026, 9, 15, 3, 0, 0))

	q, err := beads.BuildQuery("dolt_log", w, loc)
	if err != nil {
		t.Fatalf("BuildQuery(dolt_log, ...) error: %v", err)
	}
	wantSQL := "SELECT * FROM dolt_log WHERE date >= ? AND date < ? ORDER BY date, commit_hash"
	if q.SQL != wantSQL {
		t.Errorf("SQL = %q, want %q", q.SQL, wantSQL)
	}
	if len(q.Args) != 2 || q.Args[0] != "2026-09-15 03:00:00" || q.Args[1] != "2026-09-15 04:00:00" {
		t.Errorf("Args = %v, want [\"2026-09-15 03:00:00\" \"2026-09-15 04:00:00\"] (UTC, unconverted)", q.Args)
	}
}

// AC-12 example: a snapshot for day 2026-09-10 gets the AS OF arg "2026-09-11 00:00:00" (the
// window's End, in UTC) -- for every one of the four daily tables, each with its own ORDER BY key
// (AC-13: issues/comments/dependencies use id; labels uses issue_id, label). F2: the query must
// use TIMESTAMP(?), never a bare string literal after AS OF.
func TestBuildQuery_SnapshotASOF_AC12(t *testing.T) {
	loc := mustSydney(t)
	w := dayWindow(utc(2026, 9, 10, 0, 0, 0))

	cases := []struct {
		table   string
		wantSQL string
	}{
		{"issues", "SELECT * FROM issues AS OF TIMESTAMP(?) ORDER BY id"},
		{"comments", "SELECT * FROM comments AS OF TIMESTAMP(?) ORDER BY id"},
		{"dependencies", "SELECT * FROM dependencies AS OF TIMESTAMP(?) ORDER BY id"},
		{"labels", "SELECT * FROM labels AS OF TIMESTAMP(?) ORDER BY issue_id, label"},
	}
	for _, c := range cases {
		t.Run(c.table, func(t *testing.T) {
			q, err := beads.BuildQuery(c.table, w, loc)
			if err != nil {
				t.Fatalf("BuildQuery(%s, ...) error: %v", c.table, err)
			}
			if q.SQL != c.wantSQL {
				t.Errorf("SQL = %q, want %q", q.SQL, c.wantSQL)
			}
			if len(q.Args) != 1 {
				t.Fatalf("len(Args) = %d, want 1 (got %v)", len(q.Args), q.Args)
			}
			if q.Args[0] != "2026-09-11 00:00:00" {
				t.Errorf("Args[0] = %v, want %q (window End, UTC)", q.Args[0], "2026-09-11 00:00:00")
			}
			// F2: a bare string literal after AS OF is rejected by Dolt; the query must call
			// TIMESTAMP(...) around the placeholder.
			if !strings.Contains(q.SQL, "AS OF TIMESTAMP(?)") {
				t.Errorf("SQL %q does not contain \"AS OF TIMESTAMP(?)\" (F2: bare string literal is rejected)", q.SQL)
			}
		})
	}
}

// "unknown table -> error" (brief's Required-content BuildQuery signature comment): a table name
// outside the fixed seven-table declaration must be rejected, not turned into a query that names
// an arbitrary (and therefore possibly attacker- or caller-controlled) table.
func TestBuildQuery_UnknownTable_Error(t *testing.T) {
	loc := mustSydney(t)
	w := hourWindow(utc(2026, 9, 15, 3, 0, 0))
	for _, bad := range []string{"", "not_a_table", "events; DROP TABLE events", "Events"} {
		t.Run(bad, func(t *testing.T) {
			if q, err := beads.BuildQuery(bad, w, loc); err == nil {
				t.Errorf("BuildQuery(%q, ...) = %+v, nil error; want a rejection", bad, q)
			}
		})
	}
}

// AC-11 / AC-12: BuildQuery is pure -- it reads no clock and touches no database, so calling it
// twice with the same arguments returns byte-identical output.
func TestBuildQuery_Pure_AC11(t *testing.T) {
	loc := mustSydney(t)
	w := hourWindow(utc(2026, 9, 15, 3, 0, 0))

	first, err := beads.BuildQuery("events", w, loc)
	if err != nil {
		t.Fatalf("first BuildQuery: %v", err)
	}
	second, err := beads.BuildQuery("events", w, loc)
	if err != nil {
		t.Fatalf("second BuildQuery: %v", err)
	}
	if first.SQL != second.SQL {
		t.Errorf("SQL varied between calls: %q vs %q", first.SQL, second.SQL)
	}
	if len(first.Args) != len(second.Args) {
		t.Fatalf("Args length varied between calls: %v vs %v", first.Args, second.Args)
	}
	for i := range first.Args {
		if first.Args[i] != second.Args[i] {
			t.Errorf("Args[%d] varied between calls: %v vs %v", i, first.Args[i], second.Args[i])
		}
	}
}
