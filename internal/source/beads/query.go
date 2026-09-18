package beads

import (
	"fmt"
	"time"

	"github.com/DMokong/data-platform/internal/source"
	"github.com/DMokong/data-platform/internal/window"
)

// Query is one parameterised statement: SQL with ? placeholders and the values that fill them.
type Query struct {
	SQL  string
	Args []any
}

// boundLayout formats a window bound the way the source stores DATETIME values.
const boundLayout = "2006-01-02 15:04:05"

// statement is the fixed SQL for one declared table.
type statement struct {
	sql string
	// eventsTZ marks a time column stored as EventsTZ wall time (spec F1): its window bounds are
	// converted to that zone. Every other time column is naive UTC and takes UTC bounds.
	eventsTZ bool
}

// statements is the complete set of SQL this package can run, one literal per declared table.
// Table names therefore never come from caller input. Each ORDER BY is the table's unique key,
// so a batch's row order is deterministic (AC-13). Snapshots use AS OF TIMESTAMP(?), because Dolt
// rejects a bare string literal after AS OF (spec F2).
var statements = map[string]statement{
	"events": {
		sql:      "SELECT * FROM events WHERE created_at >= ? AND created_at < ? ORDER BY created_at, id",
		eventsTZ: true,
	},
	"dolt_history_issues": {
		sql: "SELECT * FROM dolt_history_issues WHERE commit_date >= ? AND commit_date < ? ORDER BY commit_date, commit_hash, id",
	},
	"dolt_log": {
		sql: "SELECT * FROM dolt_log WHERE date >= ? AND date < ? ORDER BY date, commit_hash",
	},
	"issues":       {sql: "SELECT * FROM issues AS OF TIMESTAMP(?) ORDER BY id"},
	"comments":     {sql: "SELECT * FROM comments AS OF TIMESTAMP(?) ORDER BY id"},
	"dependencies": {sql: "SELECT * FROM dependencies AS OF TIMESTAMP(?) ORDER BY id"},
	"labels":       {sql: "SELECT * FROM labels AS OF TIMESTAMP(?) ORDER BY issue_id, label"},
}

// BuildQuery returns the statement that extracts table for window w. It is pure: no clock, no
// database. An event-log table gets the bounds [w.Start, w.End) as wall-clock strings, converted
// to eventsLoc for events (spec F1) and left in UTC otherwise. A snapshot table gets w.End in UTC
// as its AS OF instant. A table outside the declaration, an invalid window, or a window whose
// grain differs from the table's declared grain is an error.
func BuildQuery(table string, w window.Window, eventsLoc *time.Location) (Query, error) {
	t, ok := Declaration().Table(table)
	if !ok {
		return Query{}, fmt.Errorf("beads: unknown table %q", table)
	}
	stmt, ok := statements[t.Name]
	if !ok {
		return Query{}, fmt.Errorf("beads: declared table %q has no statement", t.Name)
	}
	if err := w.Validate(); err != nil {
		return Query{}, fmt.Errorf("beads: table %s: %w", t.Name, err)
	}
	if w.Grain != t.Grain {
		return Query{}, fmt.Errorf("beads: table %s is declared with grain %v, got a %v window %s",
			t.Name, t.Grain, w.Grain, w)
	}

	switch t.Shape {
	case source.EventLog:
		loc := time.UTC
		if stmt.eventsTZ {
			if eventsLoc == nil {
				return Query{}, fmt.Errorf("beads: table %s needs the events time zone, got nil", t.Name)
			}
			loc = eventsLoc
		}
		return Query{SQL: stmt.sql, Args: []any{wallClock(w.Start, loc), wallClock(w.End, loc)}}, nil
	case source.Snapshot:
		return Query{SQL: stmt.sql, Args: []any{wallClock(w.End, time.UTC)}}, nil
	default:
		return Query{}, fmt.Errorf("beads: table %s has unknown shape %d", t.Name, int(t.Shape))
	}
}

// wallClock renders instant t as wall-clock time in loc.
func wallClock(t time.Time, loc *time.Location) string {
	return t.In(loc).Format(boundLayout)
}
