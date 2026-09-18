package beads

// White-box tests for decodeValue, the unexported per-value decoder behind Fetch. It is tested
// here directly because the live server has no value that reaches its error paths (no unsigned
// value above int64's range, no undecodable text), and because the brief's "Values" and
// "Observed driver behaviour" rules (text-protocol []byte numerics, uint64 overflow errors rather
// than wraps, timestamps in time.UTC carrying the source's wall-clock fields) are only otherwise
// exercised by whatever the live data happens to contain. The BuildQuery window guards and the
// Sydney DST fall-back bounds are pinned here too.

import (
	"math"
	"testing"
	"time"

	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/window"
)

func TestDecodeValue_Decodes(t *testing.T) {
	sydney, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skipf("Australia/Sydney tzdata unavailable: %v", err)
	}
	cases := []struct {
		name string
		kind record.Kind
		in   any
		want any
	}{
		{"null string", record.String, nil, nil},
		{"null int", record.Int64, nil, nil},
		{"null timestamp", record.Timestamp, nil, nil},
		{"text bytes", record.String, []byte(`{"a":1}`), `{"a":1}`},
		{"decimal bytes stay text", record.String, []byte("12.50"), "12.50"},
		{"int64", record.Int64, int64(-7), int64(-7)},
		{"int bytes", record.Int64, []byte("42"), int64(42)},
		{"uint64 in range", record.Int64, uint64(math.MaxInt64), int64(math.MaxInt64)},
		{"uint bytes in range", record.Int64, []byte("9223372036854775807"), int64(math.MaxInt64)},
		{"float64", record.Float64, 1.5, 1.5},
		{"float bytes", record.Float64, []byte("0.1"), 0.1},
		{"float32 keeps its decimal text", record.Float64, float32(0.1), 0.1},
		{
			"timestamp keeps wall clock, moves to UTC",
			record.Timestamp,
			time.Date(2026, 9, 15, 21, 5, 6, 7000, sydney),
			time.Date(2026, 9, 15, 21, 5, 6, 7000, time.UTC),
		},
		{"timestamp bytes", record.Timestamp, []byte("2026-09-15 21:05:06"), time.Date(2026, 9, 15, 21, 5, 6, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decodeValue(c.kind, c.in)
			if err != nil {
				t.Fatalf("decodeValue(%v, %#v) error: %v", c.kind, c.in, err)
			}
			if got != c.want {
				t.Errorf("decodeValue(%v, %#v) = %#v, want %#v", c.kind, c.in, got, c.want)
			}
			if ts, ok := got.(time.Time); ok && ts.Location() != time.UTC {
				t.Errorf("timestamp location = %v, want time.UTC", ts.Location())
			}
		})
	}
}

func TestDecodeValue_Errors(t *testing.T) {
	cases := []struct {
		name string
		kind record.Kind
		in   any
	}{
		{"uint64 overflow errors rather than wraps", record.Int64, uint64(math.MaxInt64) + 1},
		{"uint bytes overflow", record.Int64, []byte("18446744073709551615")},
		{"int bytes not a number", record.Int64, []byte("12a")},
		{"float bytes not a number", record.Float64, []byte("x")},
		{"zero date text", record.Timestamp, []byte("0000-00-00 00:00:00")},
		{"string column given an int", record.String, int64(1)},
		{"int column given a time", record.Int64, time.Unix(0, 0)},
		{"unknown kind", record.Kind(99), "x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, err := decodeValue(c.kind, c.in); err == nil {
				t.Errorf("decodeValue(%v, %#v) = %#v, nil error; want an error", c.kind, c.in, got)
			}
		})
	}
}

func TestBuildQuery_RejectsBadWindows(t *testing.T) {
	start := time.Date(2026, 9, 15, 3, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		table string
		w     window.Window
	}{
		{"day window for an hourly table", "events", window.Window{Start: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC), Grain: window.Day}},
		{"hour window for a daily table", "issues", window.Window{Start: start, End: start.Add(time.Hour), Grain: window.Hour}},
		{"misaligned start", "dolt_log", window.Window{Start: start.Add(time.Minute), End: start.Add(time.Hour + time.Minute), Grain: window.Hour}},
		{"zero window", "events", window.Window{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if q, err := BuildQuery(c.table, c.w, time.UTC); err == nil {
				t.Errorf("BuildQuery(%s, %+v) = %+v, nil error; want an error", c.table, c.w, q)
			}
		})
	}
	if _, err := BuildQuery("events", window.Window{Start: start, End: start.Add(time.Hour), Grain: window.Hour}, nil); err == nil {
		t.Error("BuildQuery(events, ..., nil location) returned nil error; want an error")
	}
}

// At the Sydney DST fall-back (2027-04-04 03:00 AEDT -> 02:00 AEST, i.e. 2027-04-03T16:00Z), the
// wall-clock hour 02:00-03:00 occurs twice. Converting each UTC bound to Sydney wall time gives
// window 15Z the empty range [02:00, 02:00) and window 16Z the range [02:00, 03:00), which holds
// both real hours. Every event is therefore fetched exactly once.
func TestBuildQuery_EventsDSTFallBack(t *testing.T) {
	sydney, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skipf("Australia/Sydney tzdata unavailable: %v", err)
	}
	cases := []struct {
		start      time.Time
		lower, upp string
	}{
		{time.Date(2027, 4, 3, 14, 0, 0, 0, time.UTC), "2027-04-04 01:00:00", "2027-04-04 02:00:00"},
		{time.Date(2027, 4, 3, 15, 0, 0, 0, time.UTC), "2027-04-04 02:00:00", "2027-04-04 02:00:00"},
		{time.Date(2027, 4, 3, 16, 0, 0, 0, time.UTC), "2027-04-04 02:00:00", "2027-04-04 03:00:00"},
	}
	for _, c := range cases {
		w := window.Window{Start: c.start, End: c.start.Add(time.Hour), Grain: window.Hour}
		q, err := BuildQuery("events", w, sydney)
		if err != nil {
			t.Fatalf("BuildQuery(events, %s): %v", w, err)
		}
		if q.Args[0] != c.lower || q.Args[1] != c.upp {
			t.Errorf("window %s: bounds %v, want [%s %s]", w, q.Args, c.lower, c.upp)
		}
	}
}
