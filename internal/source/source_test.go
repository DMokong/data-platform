package source_test

// Behavioral tests for internal/source, anchored to AC-10 ("Declared extraction parameters") in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md and the "Required content" exact-API block
// of tasks/03-beads-fetcher/brief.md. internal/source's own job is to hold the declaration types
// (Shape, Table, Source) that a concrete Fetcher package (beads.Declaration()) fills in, plus the
// lookup helper AC-10 calls "inspectable data". These tests only exercise that generic contract;
// beads.Declaration()'s actual seven-table content is covered by beads' own declaration_test.go.
//
// This file only imports internal/source's exported surface (black-box): package source_test.

import (
	"testing"
	"time"

	"github.com/DMokong/data-platform/internal/source"
	"github.com/DMokong/data-platform/internal/window"
)

// AC-10: Source.Table(name) finds a declared table by name and reports it, unmodified, alongside
// a true "found" flag — this is the "inspectable data" AC-10 requires of a declaration.
func TestSourceTable_Found_AC10(t *testing.T) {
	want := source.Table{Name: "events", Grain: window.Hour, Lookback: 24, Shape: source.EventLog}
	s := source.Source{
		Name:    "beads",
		Cadence: 15 * time.Minute,
		Tables: []source.Table{
			want,
			{Name: "issues", Grain: window.Day, Lookback: 2, Shape: source.Snapshot},
		},
	}

	got, ok := s.Table("events")
	if !ok {
		t.Fatalf("Table(%q) ok = false, want true", "events")
	}
	if got != want {
		t.Errorf("Table(%q) = %+v, want %+v", "events", got, want)
	}
}

// AC-10: Source.Table(name) reports not-found (ok=false) for a name absent from Tables, rather
// than panicking or silently matching the wrong entry.
func TestSourceTable_NotFound_AC10(t *testing.T) {
	s := source.Source{
		Name:    "beads",
		Cadence: 15 * time.Minute,
		Tables: []source.Table{
			{Name: "events", Grain: window.Hour, Lookback: 24, Shape: source.EventLog},
		},
	}

	got, ok := s.Table("nonexistent_table")
	if ok {
		t.Fatalf("Table(%q) ok = true, want false (got %+v)", "nonexistent_table", got)
	}
	if got != (source.Table{}) {
		t.Errorf("Table(%q) with ok=false returned %+v, want the zero Table", "nonexistent_table", got)
	}
}

// AC-10: Source.Table on an empty declaration always reports not-found, never a spurious match.
func TestSourceTable_EmptyDeclaration_AC10(t *testing.T) {
	var s source.Source
	if _, ok := s.Table("events"); ok {
		t.Errorf("Table(%q) on a zero-value Source reported ok=true, want false", "events")
	}
}

// AC-10 (via the Required-content struct block): EventLog and Snapshot are distinct Shape
// values, so a Table's Shape field can actually distinguish the two extraction strategies
// ("Hourly event-log windows" vs "Daily snapshots ... through Dolt time travel", brief Goal).
func TestShape_EventLogAndSnapshotAreDistinct_AC10(t *testing.T) {
	if source.EventLog == source.Snapshot {
		t.Fatalf("EventLog (%v) == Snapshot (%v), want distinct Shape values", source.EventLog, source.Snapshot)
	}
}
