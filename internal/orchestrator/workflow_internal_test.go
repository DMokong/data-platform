package orchestrator

// White-box tests of the pure helpers MaterialiseSource plans its children with (AC-34): the
// grain order and lookback come from the declaration's table order, never from a map.

import (
	"reflect"
	"testing"
	"time"

	"github.com/DMokong/data-platform/internal/source"
	"github.com/DMokong/data-platform/internal/source/beads"
	"github.com/DMokong/data-platform/internal/window"
)

func TestGrainLookbacks_BeadsDeclarationOrder(t *testing.T) {
	got := grainLookbacks(beads.Declaration())
	want := []grainLookback{{grain: window.Hour, lookback: 24}, {grain: window.Day, lookback: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("grainLookbacks(beads) = %+v, want %+v", got, want)
	}
}

// The first table's grain comes first, and tables of one grain that disagree get the largest
// lookback.
func TestGrainLookbacks_OrderAndMaxLookback(t *testing.T) {
	src := source.Source{Name: "s", Cadence: time.Minute, Tables: []source.Table{
		{Name: "a", Grain: window.Day, Lookback: 2},
		{Name: "b", Grain: window.Hour, Lookback: 3},
		{Name: "c", Grain: window.Day, Lookback: 7},
		{Name: "d", Grain: window.Hour, Lookback: 1},
	}}
	want := []grainLookback{{grain: window.Day, lookback: 7}, {grain: window.Hour, lookback: 3}}
	for run := 0; run < 20; run++ {
		if got := grainLookbacks(src); !reflect.DeepEqual(got, want) {
			t.Fatalf("grainLookbacks run %d = %+v, want %+v", run, got, want)
		}
	}
}

func TestChildWorkflowID(t *testing.T) {
	start := time.Date(2026, 9, 15, 13, 0, 0, 0, time.UTC)
	hour := window.Window{Start: start, End: start.Add(time.Hour), Grain: window.Hour}
	day := window.Containing(start, window.Day)
	if got := childWorkflowID("beads", hour); got != "materialise/beads/hour/2026-09-15T13" {
		t.Errorf("hourly child id = %q", got)
	}
	if got := childWorkflowID("beads", day); got != "materialise/beads/day/2026-09-15" {
		t.Errorf("daily child id = %q", got)
	}
}
