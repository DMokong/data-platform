// Behavioral tests for RecordWrite and RecordActivity -- AC-39: "WriteWindow adds counters
// platform.rows_written and platform.bytes_written (attributes source, table, zone). Each
// activity records histogram platform.activity.duration (attributes activity, outcome =
// success|failure)."
package telemetry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DMokong/data-platform/internal/telemetry"
)

// TestRecordWrite_SumsAcrossCalls_AC39 checks that repeated RecordWrite calls for the same
// (source, table, zone) accumulate rather than overwrite -- the "sums" behavior the brief calls
// out explicitly -- while a different table keeps its own independent total.
func TestRecordWrite_SumsAcrossCalls_AC39(t *testing.T) {
	reader := newTestMeterProvider(t)
	ctx := context.Background()

	telemetry.RecordWrite(ctx, "beads", "issues", "bronze", 10, 1000)
	telemetry.RecordWrite(ctx, "beads", "issues", "bronze", 5, 500)
	telemetry.RecordWrite(ctx, "beads", "comments", "bronze", 3, 300)

	rows := collectSumInt64(t, reader, "platform.rows_written")
	bytesWritten := collectSumInt64(t, reader, "platform.bytes_written")

	issuesAttrs := map[string]string{"source": "beads", "table": "issues", "zone": "bronze"}
	if got := findInt64Point(t, rows.DataPoints, issuesAttrs); got != 15 {
		t.Errorf("rows_written(beads,issues,bronze) = %d, want 15 (10 + 5 summed across calls)", got)
	}
	if got := findInt64Point(t, bytesWritten.DataPoints, issuesAttrs); got != 1500 {
		t.Errorf("bytes_written(beads,issues,bronze) = %d, want 1500 (1000 + 500 summed across calls)", got)
	}

	commentsAttrs := map[string]string{"source": "beads", "table": "comments", "zone": "bronze"}
	if got := findInt64Point(t, rows.DataPoints, commentsAttrs); got != 3 {
		t.Errorf("rows_written(beads,comments,bronze) = %d, want 3 (independent of the issues total)", got)
	}
	if got := findInt64Point(t, bytesWritten.DataPoints, commentsAttrs); got != 300 {
		t.Errorf("bytes_written(beads,comments,bronze) = %d, want 300", got)
	}
}

// TestRecordActivity_OutcomeAttributes_AC39 checks that RecordActivity tags success vs. failure
// via the outcome attribute (derived only from whether err is nil), keeps per-activity totals
// separate, and records real elapsed seconds rather than a placeholder value.
func TestRecordActivity_OutcomeAttributes_AC39(t *testing.T) {
	reader := newTestMeterProvider(t)
	ctx := context.Background()

	telemetry.RecordActivity(ctx, "FetchWindow", 150*time.Millisecond, nil)
	telemetry.RecordActivity(ctx, "FetchWindow", 80*time.Millisecond, errors.New("boom"))
	telemetry.RecordActivity(ctx, "WriteWindow", 20*time.Millisecond, nil)

	hist := collectHistogramFloat64(t, reader, "platform.activity.duration")

	fetchSuccess := findHistogramPoint(t, hist.DataPoints, map[string]string{"activity": "FetchWindow", "outcome": "success"})
	if fetchSuccess.Count != 1 {
		t.Errorf("FetchWindow/success Count = %d, want 1", fetchSuccess.Count)
	}
	if delta := fetchSuccess.Sum - 0.150; delta > 1e-6 || delta < -1e-6 {
		t.Errorf("FetchWindow/success Sum = %v seconds, want ~0.150", fetchSuccess.Sum)
	}

	fetchFailure := findHistogramPoint(t, hist.DataPoints, map[string]string{"activity": "FetchWindow", "outcome": "failure"})
	if fetchFailure.Count != 1 {
		t.Errorf("FetchWindow/failure Count = %d, want 1", fetchFailure.Count)
	}
	if delta := fetchFailure.Sum - 0.080; delta > 1e-6 || delta < -1e-6 {
		t.Errorf("FetchWindow/failure Sum = %v seconds, want ~0.080", fetchFailure.Sum)
	}

	writeSuccess := findHistogramPoint(t, hist.DataPoints, map[string]string{"activity": "WriteWindow", "outcome": "success"})
	if writeSuccess.Count != 1 {
		t.Errorf("WriteWindow/success Count = %d, want 1", writeSuccess.Count)
	}
	if delta := writeSuccess.Sum - 0.020; delta > 1e-6 || delta < -1e-6 {
		t.Errorf("WriteWindow/success Sum = %v seconds, want ~0.020", writeSuccess.Sum)
	}
}
