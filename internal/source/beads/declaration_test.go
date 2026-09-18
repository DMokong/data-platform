package beads_test

// Behavioral tests for beads.Declaration, anchored to AC-10 ("Declared extraction parameters") in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md: source "beads", cadence 15 minutes, and
// exactly seven tables in a fixed order with the grain/lookback/shape the brief's Goal section
// assigns to each ("Hourly event-log windows cover events, dolt_history_issues and dolt_log ...
// Daily snapshots ... through Dolt time travel").
//
// Black-box (package beads_test).

import (
	"testing"
	"time"

	"github.com/DMokong/data-platform/internal/source"
	"github.com/DMokong/data-platform/internal/source/beads"
	"github.com/DMokong/data-platform/internal/window"
)

// wantTables is the AC-10 declaration, in the exact order the AC names: "events,
// dolt_history_issues, dolt_log ... and issues, comments, dependencies, labels".
var wantTables = []source.Table{
	{Name: "events", Grain: window.Hour, Lookback: 24, Shape: source.EventLog},
	{Name: "dolt_history_issues", Grain: window.Hour, Lookback: 24, Shape: source.EventLog},
	{Name: "dolt_log", Grain: window.Hour, Lookback: 24, Shape: source.EventLog},
	{Name: "issues", Grain: window.Day, Lookback: 2, Shape: source.Snapshot},
	{Name: "comments", Grain: window.Day, Lookback: 2, Shape: source.Snapshot},
	{Name: "dependencies", Grain: window.Day, Lookback: 2, Shape: source.Snapshot},
	{Name: "labels", Grain: window.Day, Lookback: 2, Shape: source.Snapshot},
}

// AC-10: Declaration() returns source "beads", cadence 15*time.Minute, and exactly the seven
// tables above, in that order, each with its documented grain, lookback and shape.
func TestDeclaration_AC10(t *testing.T) {
	got := beads.Declaration()

	if got.Name != "beads" {
		t.Errorf("Name = %q, want %q", got.Name, "beads")
	}
	if got.Cadence != 15*time.Minute {
		t.Errorf("Cadence = %v, want %v", got.Cadence, 15*time.Minute)
	}
	if len(got.Tables) != len(wantTables) {
		t.Fatalf("len(Tables) = %d, want %d (got %+v)", len(got.Tables), len(wantTables), got.Tables)
	}
	for i, want := range wantTables {
		if got.Tables[i] != want {
			t.Errorf("Tables[%d] = %+v, want %+v", i, got.Tables[i], want)
		}
	}
}

// AC-10: every declared table is reachable through Source.Table by name, returning exactly the
// entry from Declaration's own list -- the "inspectable data" AC-10 requires, exercised through
// beads's real declaration rather than a hand-built source.Source (as in internal/source's own
// generic test).
func TestDeclaration_TableLookup_AC10(t *testing.T) {
	decl := beads.Declaration()
	for _, want := range wantTables {
		t.Run(want.Name, func(t *testing.T) {
			got, ok := decl.Table(want.Name)
			if !ok {
				t.Fatalf("Table(%q) ok = false, want true", want.Name)
			}
			if got != want {
				t.Errorf("Table(%q) = %+v, want %+v", want.Name, got, want)
			}
		})
	}

	if _, ok := decl.Table("not_a_real_table"); ok {
		t.Error(`Table("not_a_real_table") ok = true, want false`)
	}
}

// AC-10: Declaration() is a pure, repeatable description -- calling it twice returns two
// declarations with identical content (not, say, a slice that has been mutated by a prior
// caller, or a value that varies with wall-clock time).
func TestDeclaration_Idempotent_AC10(t *testing.T) {
	first := beads.Declaration()
	second := beads.Declaration()
	if first.Name != second.Name || first.Cadence != second.Cadence {
		t.Fatalf("Declaration() varied between calls: %+v vs %+v", first, second)
	}
	if len(first.Tables) != len(second.Tables) {
		t.Fatalf("Tables length varied between calls: %d vs %d", len(first.Tables), len(second.Tables))
	}
	for i := range first.Tables {
		if first.Tables[i] != second.Tables[i] {
			t.Errorf("Tables[%d] varied between calls: %+v vs %+v", i, first.Tables[i], second.Tables[i])
		}
	}

	// Mutating the slice returned by the first call must not corrupt a later call's result: if
	// Declaration() shares backing storage across calls this would flip second.Tables[0] too.
	if len(first.Tables) == 0 {
		t.Fatalf("Declaration().Tables is empty, want %d tables (see TestDeclaration_AC10)", len(wantTables))
	}
	first.Tables[0].Name = "MUTATED"
	third := beads.Declaration()
	if len(third.Tables) == 0 {
		t.Fatalf("Declaration().Tables is empty on a later call, want %d tables", len(wantTables))
	}
	if third.Tables[0].Name != wantTables[0].Name {
		t.Errorf("mutating a previous Declaration() result corrupted a later call: Tables[0].Name = %q, want %q",
			third.Tables[0].Name, wantTables[0].Name)
	}
}
