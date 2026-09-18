package window_test

// Behavioral tests for internal/window, anchored to AC-01 ("Window arithmetic") in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md and the exact-API "Required content"
// section of tasks/01-foundation/brief.md. Every test below is commented with the AC it proves;
// this package only imports the exported surface of internal/window (black-box).

import (
	"strconv"
	"testing"
	"time"

	"github.com/DMokong/data-platform/internal/window"
)

func utc(y int, m time.Month, d, hh, mm, ss int) time.Time {
	return time.Date(y, m, d, hh, mm, ss, 0, time.UTC)
}

// assertWindow checks the observable contract that every returned Window carries time.UTC
// locations (AC-01: "All returned Start / End values carry time.UTC as their location").
func assertWindow(t *testing.T, got window.Window, wantStart, wantEnd time.Time, wantGrain window.Grain) {
	t.Helper()
	if !got.Start.Equal(wantStart) {
		t.Errorf("Start = %v, want %v", got.Start, wantStart)
	}
	if got.Start.Location() != time.UTC {
		t.Errorf("Start.Location() = %v, want time.UTC", got.Start.Location())
	}
	if !got.End.Equal(wantEnd) {
		t.Errorf("End = %v, want %v", got.End, wantEnd)
	}
	if got.End.Location() != time.UTC {
		t.Errorf("End.Location() = %v, want time.UTC", got.End.Location())
	}
	if got.Grain != wantGrain {
		t.Errorf("Grain = %v, want %v", got.Grain, wantGrain)
	}
}

// AC-01: Grain.Duration() reports 1h for Hour and 24h for Day.
func TestGrainDuration_AC01(t *testing.T) {
	cases := []struct {
		name string
		g    window.Grain
		want time.Duration
	}{
		{"hour", window.Hour, time.Hour},
		{"day", window.Day, 24 * time.Hour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.g.Duration(); got != c.want {
				t.Errorf("%v.Duration() = %v, want %v", c.g, got, c.want)
			}
		})
	}
}

// AC-01: Grain.String() renders "hour" / "day" (used by Window.String() and logs).
func TestGrainString_AC01(t *testing.T) {
	cases := []struct {
		g    window.Grain
		want string
	}{
		{window.Hour, "hour"},
		{window.Day, "day"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := c.g.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}

// AC-01: Containing(t, g) returns the aligned window holding instant t.
func TestContaining_AC01(t *testing.T) {
	cases := []struct {
		name      string
		instant   time.Time
		grain     window.Grain
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "hour mid-window",
			instant:   utc(2026, 9, 15, 13, 47, 22),
			grain:     window.Hour,
			wantStart: utc(2026, 9, 15, 13, 0, 0),
			wantEnd:   utc(2026, 9, 15, 14, 0, 0),
		},
		{
			name:      "hour exact boundary is start of that hour (half-open)",
			instant:   utc(2026, 9, 15, 13, 0, 0),
			grain:     window.Hour,
			wantStart: utc(2026, 9, 15, 13, 0, 0),
			wantEnd:   utc(2026, 9, 15, 14, 0, 0),
		},
		{
			name:      "day mid-window",
			instant:   utc(2026, 9, 15, 13, 47, 22),
			grain:     window.Day,
			wantStart: utc(2026, 9, 15, 0, 0, 0),
			wantEnd:   utc(2026, 9, 16, 0, 0, 0),
		},
		{
			name:      "day exact midnight boundary",
			instant:   utc(2026, 9, 15, 0, 0, 0),
			grain:     window.Day,
			wantStart: utc(2026, 9, 15, 0, 0, 0),
			wantEnd:   utc(2026, 9, 16, 0, 0, 0),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := window.Containing(c.instant, c.grain)
			assertWindow(t, got, c.wantStart, c.wantEnd, c.grain)
		})
	}
}

// AC-01: "an input in a non-UTC location yields the same window as its UTC instant."
func TestContaining_NonUTCLocationSameInstant_AC01(t *testing.T) {
	sydney, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skipf("Australia/Sydney tzdata unavailable in this environment: %v", err)
	}
	utcInstant := utc(2026, 9, 15, 13, 47, 22)
	sydneyInstant := utcInstant.In(sydney) // same instant, different Location wrapper

	for _, g := range []window.Grain{window.Hour, window.Day} {
		t.Run(g.String(), func(t *testing.T) {
			want := window.Containing(utcInstant, g)
			got := window.Containing(sydneyInstant, g)
			if !got.Start.Equal(want.Start) || !got.End.Equal(want.End) || got.Grain != want.Grain {
				t.Errorf("Containing(sydneyInstant, %v) = %+v, want %+v", g, got, want)
			}
			if got.Start.Location() != time.UTC || got.End.Location() != time.UTC {
				t.Errorf("Containing(sydneyInstant, %v) did not normalise to UTC: Start.Loc=%v End.Loc=%v",
					g, got.Start.Location(), got.End.Location())
			}
		})
	}
}

// AC-01 example: Lookback(2026-09-15T00:30Z, Hour, 24) runs 2026-09-14T01:00Z -> 2026-09-15T00:00Z,
// the last window contains "now", and windows are oldest-first with no gaps.
func TestLookback_HourExample_AC01(t *testing.T) {
	now := utc(2026, 9, 15, 0, 30, 0)
	got, err := window.Lookback(now, window.Hour, 24)
	if err != nil {
		t.Fatalf("Lookback returned error: %v", err)
	}
	if len(got) != 24 {
		t.Fatalf("len(windows) = %d, want 24", len(got))
	}
	assertWindow(t, got[0], utc(2026, 9, 14, 1, 0, 0), utc(2026, 9, 14, 2, 0, 0), window.Hour)
	last := got[len(got)-1]
	assertWindow(t, last, utc(2026, 9, 15, 0, 0, 0), utc(2026, 9, 15, 1, 0, 0), window.Hour)
	if now.Before(last.Start) || !now.Before(last.End) {
		t.Errorf("last window %+v does not contain now=%v", last, now)
	}
	for i := 1; i < len(got); i++ {
		if !got[i-1].End.Equal(got[i].Start) {
			t.Errorf("gap/overlap between window %d (End=%v) and window %d (Start=%v)",
				i-1, got[i-1].End, i, got[i].Start)
		}
	}
}

// AC-01 example: Lookback(same instant, Day, 2) = [2026-09-14, 2026-09-15].
func TestLookback_DayExample_AC01(t *testing.T) {
	now := utc(2026, 9, 15, 0, 30, 0)
	got, err := window.Lookback(now, window.Day, 2)
	if err != nil {
		t.Fatalf("Lookback returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(windows) = %d, want 2", len(got))
	}
	assertWindow(t, got[0], utc(2026, 9, 14, 0, 0, 0), utc(2026, 9, 15, 0, 0, 0), window.Day)
	assertWindow(t, got[1], utc(2026, 9, 15, 0, 0, 0), utc(2026, 9, 16, 0, 0, 0), window.Day)
	for _, wStr := range []string{"2026-09-14", "2026-09-15"} {
		found := false
		for _, w := range got {
			if w.String() == wStr {
				found = true
			}
		}
		if !found {
			t.Errorf("expected window %s in result, got %v", wStr, got)
		}
	}
}

// AC-01: a non-UTC "now" yields the same Lookback result as its UTC-instant equivalent.
func TestLookback_NonUTCNow_AC01(t *testing.T) {
	sydney, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skipf("Australia/Sydney tzdata unavailable in this environment: %v", err)
	}
	utcNow := utc(2026, 9, 15, 0, 30, 0)
	sydneyNow := utcNow.In(sydney)

	want, err := window.Lookback(utcNow, window.Hour, 24)
	if err != nil {
		t.Fatalf("Lookback(utcNow) error: %v", err)
	}
	got, err := window.Lookback(sydneyNow, window.Hour, 24)
	if err != nil {
		t.Fatalf("Lookback(sydneyNow) error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len(got)=%d, len(want)=%d", len(got), len(want))
	}
	for i := range want {
		if !got[i].Start.Equal(want[i].Start) || !got[i].End.Equal(want[i].End) || got[i].Grain != want[i].Grain {
			t.Errorf("window %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// AC-01: Lookback errors for n < 1.
func TestLookback_InvalidN_AC01(t *testing.T) {
	now := utc(2026, 9, 15, 0, 30, 0)
	for _, n := range []int{0, -1, -100} {
		t.Run(("n=" + strconv.Itoa(n)), func(t *testing.T) {
			got, err := window.Lookback(now, window.Hour, n)
			if err == nil {
				t.Errorf("Lookback(now, Hour, %d) returned nil error, windows=%v", n, got)
			}
		})
	}
}

// AC-01 example: Range(2026-09-14, 2026-09-15, Hour) has 48 windows and Day has 2.
func TestRange_Examples_AC01(t *testing.T) {
	from := utc(2026, 9, 14, 0, 0, 0)
	to := utc(2026, 9, 15, 0, 0, 0)

	hourWindows, err := window.Range(from, to, window.Hour)
	if err != nil {
		t.Fatalf("Range(..., Hour) error: %v", err)
	}
	if len(hourWindows) != 48 {
		t.Fatalf("len(hourWindows) = %d, want 48", len(hourWindows))
	}
	assertWindow(t, hourWindows[0], utc(2026, 9, 14, 0, 0, 0), utc(2026, 9, 14, 1, 0, 0), window.Hour)
	assertWindow(t, hourWindows[47], utc(2026, 9, 15, 23, 0, 0), utc(2026, 9, 16, 0, 0, 0), window.Hour)

	dayWindows, err := window.Range(from, to, window.Day)
	if err != nil {
		t.Fatalf("Range(..., Day) error: %v", err)
	}
	if len(dayWindows) != 2 {
		t.Fatalf("len(dayWindows) = %d, want 2", len(dayWindows))
	}
	assertWindow(t, dayWindows[0], utc(2026, 9, 14, 0, 0, 0), utc(2026, 9, 15, 0, 0, 0), window.Day)
	assertWindow(t, dayWindows[1], utc(2026, 9, 15, 0, 0, 0), utc(2026, 9, 16, 0, 0, 0), window.Day)
}

// AC-01: "Range uses only the UTC calendar date of from and to (time of day is ignored)."
func TestRange_IgnoresTimeOfDay_AC01(t *testing.T) {
	// Same calendar date, different times of day; must not error and must cover the whole day.
	from := utc(2026, 9, 14, 15, 0, 0)
	to := utc(2026, 9, 14, 5, 0, 0)

	got, err := window.Range(from, to, window.Hour)
	if err != nil {
		t.Fatalf("Range same-date-different-time returned error: %v", err)
	}
	if len(got) != 24 {
		t.Fatalf("len(windows) = %d, want 24 (single day)", len(got))
	}
	assertWindow(t, got[0], utc(2026, 9, 14, 0, 0, 0), utc(2026, 9, 14, 1, 0, 0), window.Hour)
	assertWindow(t, got[23], utc(2026, 9, 14, 23, 0, 0), utc(2026, 9, 15, 0, 0, 0), window.Hour)
}

// AC-01: Range errors when from's date > to's date.
func TestRange_FromAfterTo_AC01(t *testing.T) {
	cases := []struct {
		name string
		from time.Time
		to   time.Time
	}{
		{"plain dates reversed", utc(2026, 9, 15, 0, 0, 0), utc(2026, 9, 14, 0, 0, 0)},
		{"reversed by date despite close instants", utc(2026, 9, 15, 0, 0, 1), utc(2026, 9, 14, 23, 59, 59)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := window.Range(c.from, c.to, window.Hour)
			if err == nil {
				t.Errorf("Range(from, to, Hour) returned nil error, windows=%v", got)
			}
		})
	}
}

// AC-01: Window.Validate() accepts well-formed windows and rejects malformed ones.
func TestWindowValidate_AC01(t *testing.T) {
	sydney, sydErr := time.LoadLocation("Australia/Sydney")

	cases := []struct {
		name    string
		w       window.Window
		wantErr bool
	}{
		{
			name:    "valid hour window",
			w:       window.Window{Start: utc(2026, 9, 15, 13, 0, 0), End: utc(2026, 9, 15, 14, 0, 0), Grain: window.Hour},
			wantErr: false,
		},
		{
			name:    "valid day window",
			w:       window.Window{Start: utc(2026, 9, 15, 0, 0, 0), End: utc(2026, 9, 16, 0, 0, 0), Grain: window.Day},
			wantErr: false,
		},
		{
			name:    "misaligned hour start (non-zero minutes)",
			w:       window.Window{Start: utc(2026, 9, 15, 13, 30, 0), End: utc(2026, 9, 15, 14, 30, 0), Grain: window.Hour},
			wantErr: true,
		},
		{
			name:    "misaligned day start (not midnight)",
			w:       window.Window{Start: utc(2026, 9, 15, 5, 0, 0), End: utc(2026, 9, 16, 5, 0, 0), Grain: window.Day},
			wantErr: true,
		},
		{
			name:    "end does not equal start + grain duration",
			w:       window.Window{Start: utc(2026, 9, 15, 13, 0, 0), End: utc(2026, 9, 15, 15, 0, 0), Grain: window.Hour},
			wantErr: true,
		},
		{
			name:    "unknown grain",
			w:       window.Window{Start: utc(2026, 9, 15, 13, 0, 0), End: utc(2026, 9, 15, 14, 0, 0), Grain: window.Grain(0)},
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.w.Validate()
			if c.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !c.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}

	if sydErr == nil {
		t.Run("start not in UTC location", func(t *testing.T) {
			start := time.Date(2026, 9, 15, 23, 0, 0, 0, sydney) // aligned wall-clock, wrong location
			w := window.Window{Start: start, End: start.Add(time.Hour), Grain: window.Hour}
			if err := w.Validate(); err == nil {
				t.Errorf("Validate() = nil, want error for non-UTC Start")
			}
		})
		t.Run("end not in UTC location", func(t *testing.T) {
			start := utc(2026, 9, 15, 13, 0, 0)
			end := time.Date(2026, 9, 15, 23, 0, 0, 0, sydney) // same instant window would need, but wrong tz object
			w := window.Window{Start: start, End: end, Grain: window.Hour}
			if err := w.Validate(); err == nil {
				t.Errorf("Validate() = nil, want error for non-UTC End")
			}
		})
	}
}

// AC-01: Window.String() renders "2026-09-15T13" for Hour and "2026-09-15" for Day.
func TestWindowString_AC01(t *testing.T) {
	hourWindow := window.Window{Start: utc(2026, 9, 15, 13, 0, 0), End: utc(2026, 9, 15, 14, 0, 0), Grain: window.Hour}
	if got, want := hourWindow.String(), "2026-09-15T13"; got != want {
		t.Errorf("hour Window.String() = %q, want %q", got, want)
	}

	dayWindow := window.Window{Start: utc(2026, 9, 15, 0, 0, 0), End: utc(2026, 9, 16, 0, 0, 0), Grain: window.Day}
	if got, want := dayWindow.String(), "2026-09-15"; got != want {
		t.Errorf("day Window.String() = %q, want %q", got, want)
	}
}
