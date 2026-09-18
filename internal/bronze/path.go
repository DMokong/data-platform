package bronze

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/DMokong/data-platform/internal/window"
)

// Zone is a top-level bronze area: raw for what Fetchers extract from sources, derived for what
// Go transforms produce.
type Zone string

const (
	// Raw holds source extracts, dataset "<source>/<table>", written by Fetchers.
	Raw Zone = "raw"
	// Derived holds Go transform outputs, dataset "<transform>".
	Derived Zone = "derived"
)

// datasetSegment is the only shape a "/"-separated dataset segment may take. It excludes ".",
// "..", empty segments (leading, trailing or doubled slashes) and any separator, so a bronze
// path can never escape the bronze root.
var datasetSegment = regexp.MustCompile(`^[a-z0-9_]+$`)

// Path returns the bronze-relative path for a window, e.g. "raw/beads/events/dt=2026-09-15/hh=13.parquet".
//
// Hourly windows map to "<zone>/<dataset>/dt=YYYY-MM-DD/hh=HH.parquet" and daily windows to
// "<zone>/<dataset>/dt=YYYY-MM-DD/part-0.parquet", with dt and hh taken from w.Start in UTC.
// The separator is always "/", whatever the OS. Path is a pure function of its arguments: it
// reads no clock, environment or filesystem. It errors when w fails w.Validate(), when zone is
// not Raw or Derived, and when dataset is empty or any of its segments is outside [a-z0-9_]+.
func Path(zone Zone, dataset string, w window.Window) (string, error) {
	if err := w.Validate(); err != nil {
		return "", fmt.Errorf("bronze: invalid window: %w", err)
	}
	switch zone {
	case Raw, Derived:
	default:
		return "", fmt.Errorf("bronze: unknown zone %q, want %q or %q", zone, Raw, Derived)
	}
	if dataset == "" {
		return "", fmt.Errorf("bronze: empty dataset")
	}
	for _, seg := range strings.Split(dataset, "/") {
		if !datasetSegment.MatchString(seg) {
			return "", fmt.Errorf("bronze: dataset %q has segment %q, want every segment to match %s",
				dataset, seg, datasetSegment)
		}
	}

	start := w.Start.UTC()
	var file string
	switch w.Grain {
	case window.Hour:
		file = "hh=" + start.Format("15") + ".parquet"
	case window.Day:
		file = "part-0.parquet"
	default: // unreachable: Validate accepts only Hour and Day
		return "", fmt.Errorf("bronze: unsupported grain %v", w.Grain)
	}
	return string(zone) + "/" + dataset + "/dt=" + start.Format("2006-01-02") + "/" + file, nil
}
