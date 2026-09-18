package beads

import (
	"time"

	"github.com/DMokong/data-platform/internal/source"
	"github.com/DMokong/data-platform/internal/window"
)

// Declaration returns the beads source contract (AC-10): the Runner fires every 15 minutes; the
// three log tables are extracted in hourly windows with a 24-window lookback, and the four
// current-state tables as daily snapshots with a 2-window lookback. Each call returns a fresh
// value, so a caller that mutates its copy cannot affect another.
func Declaration() source.Source {
	return source.Source{
		Name:    "beads",
		Cadence: 15 * time.Minute,
		Tables: []source.Table{
			{Name: "events", Grain: window.Hour, Lookback: 24, Shape: source.EventLog},
			{Name: "dolt_history_issues", Grain: window.Hour, Lookback: 24, Shape: source.EventLog},
			{Name: "dolt_log", Grain: window.Hour, Lookback: 24, Shape: source.EventLog},
			{Name: "issues", Grain: window.Day, Lookback: 2, Shape: source.Snapshot},
			{Name: "comments", Grain: window.Day, Lookback: 2, Shape: source.Snapshot},
			{Name: "dependencies", Grain: window.Day, Lookback: 2, Shape: source.Snapshot},
			{Name: "labels", Grain: window.Day, Lookback: 2, Shape: source.Snapshot},
		},
	}
}
