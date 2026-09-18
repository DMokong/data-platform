package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// instrumentationName identifies this package as the source of its spans and metrics.
const instrumentationName = "github.com/DMokong/data-platform/internal/telemetry"

// meter returns a Meter from the current global MeterProvider. It is looked up per call (rather
// than cached) so tests can install an in-memory MeterProvider before calling any of this
// package's recording functions.
func meter() metric.Meter {
	return otel.Meter(instrumentationName)
}

// RecordWrite adds rows and bytes written for one (source, table, zone) to the
// platform.rows_written and platform.bytes_written counters.
func RecordWrite(ctx context.Context, source, table, zone string, rows int, bytes int64) {
	m := meter()
	attrs := metric.WithAttributes(
		attribute.String("source", source),
		attribute.String("table", table),
		attribute.String("zone", zone),
	)

	rowsCounter, err := m.Int64Counter("platform.rows_written",
		metric.WithDescription("Rows written to a bronze or mart table."),
		metric.WithUnit("{row}"),
	)
	if err == nil {
		rowsCounter.Add(ctx, int64(rows), attrs)
	}

	bytesCounter, err := m.Int64Counter("platform.bytes_written",
		metric.WithDescription("Bytes written to a bronze or mart table."),
		metric.WithUnit("By"),
	)
	if err == nil {
		bytesCounter.Add(ctx, bytes, attrs)
	}
}

// RecordActivity records one activity invocation's duration, tagged with its outcome
// (success|failure, derived only from whether err is nil), on the platform.activity.duration
// histogram.
func RecordActivity(ctx context.Context, activity string, d time.Duration, err error) {
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}

	hist, herr := meter().Float64Histogram("platform.activity.duration",
		metric.WithDescription("Duration of one orchestrator activity invocation."),
		metric.WithUnit("s"),
	)
	if herr != nil {
		return
	}
	hist.Record(ctx, d.Seconds(), metric.WithAttributes(
		attribute.String("activity", activity),
		attribute.String("outcome", outcome),
	))
}
