package window

import (
	"fmt"
	"time"
)

// Grain names the unit of one window.
type Grain int

const (
	// Hour is a one-hour window, the shape for event-style sources.
	Hour Grain = iota + 1
	// Day is a one-day window, the shape for snapshot-style sources.
	Day
)

// Duration returns the length of one window of this Grain: 1h for Hour, 24h for Day.
func (g Grain) Duration() time.Duration {
	switch g {
	case Hour:
		return time.Hour
	case Day:
		return 24 * time.Hour
	default:
		return 0
	}
}

// String renders the Grain as "hour" or "day", for use in file paths, logs and Window.String().
func (g Grain) String() string {
	switch g {
	case Hour:
		return "hour"
	case Day:
		return "day"
	default:
		return fmt.Sprintf("unknown grain(%d)", int(g))
	}
}

// Window is a half-open, UTC-aligned time span [Start, End) of one Grain.
type Window struct {
	Start time.Time `json:"start"` // UTC, aligned to Grain
	End   time.Time `json:"end"`   // Start + Grain.Duration(); half-open [Start, End)
	Grain Grain     `json:"grain"`
}

// Containing returns the aligned Window of Grain g holding instant t. t may carry any Location;
// the instant is normalised to UTC before alignment, so a non-UTC input and its UTC equivalent
// always yield the same Window.
func Containing(t time.Time, g Grain) Window {
	start := t.UTC().Truncate(g.Duration())
	return Window{Start: start, End: start.Add(g.Duration()), Grain: g}
}

// Lookback returns n windows of Grain g, oldest first, whose last window contains now. n must be
// at least 1.
func Lookback(now time.Time, g Grain, n int) ([]Window, error) {
	if n < 1 {
		return nil, fmt.Errorf("window: Lookback: n must be >= 1, got %d", n)
	}
	d := g.Duration()
	windows := make([]Window, n)
	windows[n-1] = Containing(now, g)
	for i := n - 2; i >= 0; i-- {
		start := windows[i+1].Start.Add(-d)
		windows[i] = Window{Start: start, End: start.Add(d), Grain: g}
	}
	return windows, nil
}

// Range returns every window of Grain g with Start in [from's UTC calendar date 00:00Z, (to's
// UTC calendar date + 1 day) 00:00Z). Only the UTC calendar date of from and to is used; the time
// of day is ignored. It is an error for from's date to be after to's date, or for g to be an
// unknown Grain (Duration() == 0 would otherwise loop forever without advancing start).
func Range(from, to time.Time, g Grain) ([]Window, error) {
	d := g.Duration()
	if d <= 0 {
		return nil, fmt.Errorf("window: Range: unknown grain %d", int(g))
	}
	fromDate := from.UTC().Truncate(24 * time.Hour)
	toDate := to.UTC().Truncate(24 * time.Hour)
	if fromDate.After(toDate) {
		return nil, fmt.Errorf("window: Range: from's date (%s) is after to's date (%s)",
			fromDate.Format("2006-01-02"), toDate.Format("2006-01-02"))
	}
	end := toDate.Add(24 * time.Hour)
	var windows []Window
	for start := fromDate; start.Before(end); start = start.Add(d) {
		windows = append(windows, Window{Start: start, End: start.Add(d), Grain: g})
	}
	return windows, nil
}

// Validate reports whether w is well-formed: a known Grain, Start and End both located in
// time.UTC, Start aligned to the Grain, and End exactly Start + Grain.Duration().
func (w Window) Validate() error {
	switch w.Grain {
	case Hour, Day:
	default:
		return fmt.Errorf("window: unknown grain %d", int(w.Grain))
	}
	if w.Start.Location() != time.UTC {
		return fmt.Errorf("window: Start location is %v, want time.UTC", w.Start.Location())
	}
	if w.End.Location() != time.UTC {
		return fmt.Errorf("window: End location is %v, want time.UTC", w.End.Location())
	}
	d := w.Grain.Duration()
	if aligned := w.Start.Truncate(d); !w.Start.Equal(aligned) {
		return fmt.Errorf("window: Start %v is not aligned to %v", w.Start, w.Grain)
	}
	if want := w.Start.Add(d); !w.End.Equal(want) {
		return fmt.Errorf("window: End %v != Start + %v (%v)", w.End, w.Grain, want)
	}
	return nil
}

// String renders w as "2026-09-15T13" for Hour or "2026-09-15" for Day.
func (w Window) String() string {
	switch w.Grain {
	case Hour:
		return w.Start.Format("2006-01-02T15")
	case Day:
		return w.Start.Format("2006-01-02")
	default:
		return w.Start.Format(time.RFC3339)
	}
}
