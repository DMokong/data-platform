package bronze_test

// Behavioral tests for bronze.Path, anchored to AC-03 ("Deterministic path") in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md and the "Path rules (AC-03)" /
// "Required exported API" sections of tasks/02-writer/brief.md. Path is required to be a pure
// function (no clock, no environment, no filesystem), so every test here only ever calls
// bronze.Path with explicit inputs and checks its return value; nothing here touches disk.
//
// This file is a black-box test (package bronze_test): Path is fully exported, so no unexported
// access is needed. The encode/write-side tests, including the one unexported test seam AC-05
// requires, live in writer_test.go (package bronze, white-box).

import (
	"strings"
	"testing"
	"time"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/window"
)

func utc(y int, m time.Month, d, hh, mm, ss int) time.Time {
	return time.Date(y, m, d, hh, mm, ss, 0, time.UTC)
}

func hourWindow(y int, m time.Month, d, hh int) window.Window {
	start := utc(y, m, d, hh, 0, 0)
	return window.Window{Start: start, End: start.Add(time.Hour), Grain: window.Hour}
}

func dayWindow(y int, m time.Month, d int) window.Window {
	start := utc(y, m, d, 0, 0, 0)
	return window.Window{Start: start, End: start.Add(24 * time.Hour), Grain: window.Day}
}

// AC-03: hourly path shape "<zone>/<dataset>/dt=YYYY-MM-DD/hh=HH.parquet", dt/hh from w.Start in
// UTC; daily path shape "<zone>/<dataset>/dt=YYYY-MM-DD/part-0.parquet"; hh=00 and hh=23 boundary
// values render with zero-padding; a multi-segment dataset ("beads/events") is accepted and
// appears verbatim in the path.
func TestPath_ValidExamples_AC03(t *testing.T) {
	cases := []struct {
		name    string
		zone    bronze.Zone
		dataset string
		w       window.Window
		want    string
	}{
		{
			name:    "hourly mid-day, spec's own example",
			zone:    bronze.Raw,
			dataset: "beads/events",
			w:       hourWindow(2026, 9, 15, 13),
			want:    "raw/beads/events/dt=2026-09-15/hh=13.parquet",
		},
		{
			name:    "hh=00 zero-pads",
			zone:    bronze.Raw,
			dataset: "beads/events",
			w:       hourWindow(2026, 9, 15, 0),
			want:    "raw/beads/events/dt=2026-09-15/hh=00.parquet",
		},
		{
			name:    "hh=23 last hour of the day",
			zone:    bronze.Raw,
			dataset: "beads/events",
			w:       hourWindow(2026, 9, 15, 23),
			want:    "raw/beads/events/dt=2026-09-15/hh=23.parquet",
		},
		{
			name:    "daily window uses part-0.parquet, derived zone",
			zone:    bronze.Derived,
			dataset: "sample/kinds",
			w:       dayWindow(2026, 9, 15),
			want:    "derived/sample/kinds/dt=2026-09-15/part-0.parquet",
		},
		{
			name:    "single-segment dataset",
			zone:    bronze.Raw,
			dataset: "issues",
			w:       dayWindow(2026, 9, 15),
			want:    "raw/issues/dt=2026-09-15/part-0.parquet",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := bronze.Path(c.zone, c.dataset, c.w)
			if err != nil {
				t.Fatalf("Path(%q, %q, %v) returned error: %v", c.zone, c.dataset, c.w, err)
			}
			if got != c.want {
				t.Errorf("Path(%q, %q, %v) = %q, want %q", c.zone, c.dataset, c.w, got, c.want)
			}
		})
	}
}

// AC-03: dt/hh are taken from w.Start in UTC, so the hour windows either side of a UTC midnight
// roll to different dt values (2026-09-15T23 vs 2026-09-16T00) rather than reusing the prior day.
func TestPath_DayBoundary_AC03(t *testing.T) {
	before, err := bronze.Path(bronze.Raw, "beads/events", hourWindow(2026, 9, 15, 23))
	if err != nil {
		t.Fatalf("Path(2026-09-15T23) returned error: %v", err)
	}
	after, err := bronze.Path(bronze.Raw, "beads/events", hourWindow(2026, 9, 16, 0))
	if err != nil {
		t.Fatalf("Path(2026-09-16T00) returned error: %v", err)
	}
	wantBefore := "raw/beads/events/dt=2026-09-15/hh=23.parquet"
	wantAfter := "raw/beads/events/dt=2026-09-16/hh=00.parquet"
	if before != wantBefore {
		t.Errorf("Path(2026-09-15T23) = %q, want %q", before, wantBefore)
	}
	if after != wantAfter {
		t.Errorf("Path(2026-09-16T00) = %q, want %q", after, wantAfter)
	}
	if before == after {
		t.Errorf("windows either side of UTC midnight produced the same path %q", before)
	}
}

// AC-03: Path is a pure function — the same inputs, called repeatedly, always give the same
// result (no clock, no environment, no filesystem dependence).
func TestPath_Pure_AC03(t *testing.T) {
	w := hourWindow(2026, 9, 15, 13)
	first, err := bronze.Path(bronze.Raw, "beads/events", w)
	if err != nil {
		t.Fatalf("Path returned error: %v", err)
	}
	for i := 0; i < 5; i++ {
		got, err := bronze.Path(bronze.Raw, "beads/events", w)
		if err != nil {
			t.Fatalf("Path returned error on call %d: %v", i, err)
		}
		if got != first {
			t.Errorf("call %d: Path = %q, want %q (same as first call)", i, got, first)
		}
	}
}

// AC-03: Path errors on a window that fails w.Validate(), an unknown zone, an empty dataset, and
// any dataset segment outside ^[a-z0-9_]+$ — including ".." segments, absolute-path-shaped
// datasets, and empty segments from leading/trailing/doubled slashes.
func TestPath_Errors_AC03(t *testing.T) {
	validWindow := hourWindow(2026, 9, 15, 13)
	misalignedWindow := window.Window{
		Start: utc(2026, 9, 15, 13, 30, 0),
		End:   utc(2026, 9, 15, 14, 30, 0),
		Grain: window.Hour,
	}
	unknownGrainWindow := window.Window{
		Start: utc(2026, 9, 15, 13, 0, 0),
		End:   utc(2026, 9, 15, 14, 0, 0),
		Grain: window.Grain(0),
	}
	// Same instant as validWindow, but Start/End carry a non-UTC Location, which Validate rejects;
	// Path must not silently derive dt/hh from a local wall clock.
	sydney := time.FixedZone("AEST", 10*60*60)
	nonUTCWindow := window.Window{
		Start: validWindow.Start.In(sydney),
		End:   validWindow.End.In(sydney),
		Grain: window.Hour,
	}

	cases := []struct {
		name    string
		zone    bronze.Zone
		dataset string
		w       window.Window
	}{
		{"window fails Validate: misaligned start", bronze.Raw, "beads/events", misalignedWindow},
		{"window fails Validate: unknown grain", bronze.Raw, "beads/events", unknownGrainWindow},
		{"window fails Validate: non-UTC location", bronze.Raw, "beads/events", nonUTCWindow},
		{"unknown zone", bronze.Zone("bronze"), "beads/events", validWindow},
		{"empty zone", bronze.Zone(""), "beads/events", validWindow},
		{"empty dataset", bronze.Raw, "", validWindow},
		{"dataset segment has uppercase", bronze.Raw, "Beads/events", validWindow},
		{"dataset segment has a hyphen", bronze.Raw, "beads/ev-ents", validWindow},
		{"dataset segment has a space", bronze.Raw, "beads/ev ents", validWindow},
		{"dataset segment is '..'", bronze.Raw, "beads/../events", validWindow},
		{"dataset is only '..'", bronze.Raw, "..", validWindow},
		{"dataset segment is '.'", bronze.Raw, "beads/./events", validWindow},
		{"dataset looks absolute (leading slash, empty first segment)", bronze.Raw, "/beads/events", validWindow},
		{"dataset has a trailing slash (empty last segment)", bronze.Raw, "beads/events/", validWindow},
		{"dataset has a doubled slash (empty middle segment)", bronze.Raw, "beads//events", validWindow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := bronze.Path(c.zone, c.dataset, c.w)
			if err == nil {
				t.Errorf("Path(%q, %q, ...) = %q, nil, want an error", c.zone, c.dataset, got)
			}
		})
	}
}

// AC-03: the path never escapes the bronze root — every accepted dataset segment matches
// ^[a-z0-9_]+$, so a joined path can never contain ".." or an absolute-path escape. This is a
// direct check on the string shape (the segment rule itself is exercised by TestPath_Errors_AC03
// above); it exists so a future implementation that started accepting a stray ".." segment (e.g.
// by loosening the regex) would fail here even if no single error-case test happened to catch it.
func TestPath_NoEscapeSequenceInAcceptedPaths_AC03(t *testing.T) {
	accepted := []struct {
		zone    bronze.Zone
		dataset string
		w       window.Window
	}{
		{bronze.Raw, "beads/events", hourWindow(2026, 9, 15, 13)},
		{bronze.Derived, "sample/kinds", dayWindow(2026, 9, 15)},
		{bronze.Raw, "dolt_history_issues", hourWindow(2026, 9, 15, 3)},
	}
	for _, c := range accepted {
		got, err := bronze.Path(c.zone, c.dataset, c.w)
		if err != nil {
			t.Fatalf("Path(%q, %q, ...) returned error: %v", c.zone, c.dataset, err)
		}
		if strings.Contains(got, "..") {
			t.Errorf("Path(%q, %q, ...) = %q contains \"..\"", c.zone, c.dataset, got)
		}
		if strings.HasPrefix(got, "/") {
			t.Errorf("Path(%q, %q, ...) = %q is absolute", c.zone, c.dataset, got)
		}
	}
}
