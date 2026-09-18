package source

import (
	"context"
	"time"

	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/window"
)

// Shape names how a table is extracted for one window.
type Shape int

const (
	// EventLog selects the rows whose time column lies inside the window.
	EventLog Shape = iota + 1
	// Snapshot reads the whole table as of the window end (time travel).
	Snapshot
)

// Table declares how one source table is extracted.
type Table struct {
	Name     string
	Grain    window.Grain // the unit of one bronze file
	Lookback int          // trailing windows re-extracted on every run
	Shape    Shape
}

// Source declares one source: its name, how often the Runner fires, and its tables in a fixed
// order.
type Source struct {
	Name    string
	Cadence time.Duration
	Tables  []Table
}

// Table returns the declared table called name, and whether it exists. A missing name yields the
// zero Table and false.
func (s Source) Table(name string) (Table, bool) {
	for _, t := range s.Tables {
		if t.Name == name {
			return t, true
		}
	}
	return Table{}, false
}

// Fetcher reads one declared table for one window and returns the rows exactly as the source
// served them. It never writes files, picks windows or cleans data.
type Fetcher interface {
	Fetch(ctx context.Context, table string, w window.Window) (record.Batch, error)
}
