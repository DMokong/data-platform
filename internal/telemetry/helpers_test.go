package telemetry_test

// Shared test scaffolding for internal/telemetry's behavioral tests (AC-38, AC-39 in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md). Every test in this package installs its
// own in-memory tracer/meter provider globally -- per the brief, telemetry's exported functions
// (RecordWrite, RecordActivity, EmitDbtResults) read the global providers at call time so tests
// can substitute an in-memory one -- and restores the previous global provider afterwards so
// tests don't leak state into each other.

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// newTestTracerProvider installs a TracerProvider backed by a tracetest.SpanRecorder as the
// global provider, restoring the previous global provider on test cleanup, and returns the
// recorder so the test can inspect ended spans.
func newTestTracerProvider(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})
	return rec
}

// newTestMeterProvider installs a MeterProvider backed by a sdkmetric.ManualReader as the global
// provider, restoring the previous global provider on test cleanup, and returns the reader so the
// test can Collect() aggregated metrics on demand.
func newTestMeterProvider(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		otel.SetMeterProvider(prev)
	})
	return reader
}

// collectSumInt64 collects the reader's current data and returns the Sum[int64] aggregation for
// the named instrument, failing the test if the instrument is absent or of a different shape.
func collectSumInt64(t *testing.T, reader *sdkmetric.ManualReader, name string) metricdata.Sum[int64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %s data is %T, want metricdata.Sum[int64]", name, m.Data)
			}
			return sum
		}
	}
	t.Fatalf("metric %q not found among collected instruments", name)
	return metricdata.Sum[int64]{}
}

// collectHistogramFloat64 is collectSumInt64's counterpart for Histogram[float64] instruments
// (platform.activity.duration).
func collectHistogramFloat64(t *testing.T, reader *sdkmetric.ManualReader, name string) metricdata.Histogram[float64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("metric %s data is %T, want metricdata.Histogram[float64]", name, m.Data)
			}
			return hist
		}
	}
	t.Fatalf("metric %q not found among collected instruments", name)
	return metricdata.Histogram[float64]{}
}

// findInt64Point returns the Value of the one DataPoint whose attribute set matches want exactly
// on the given keys (extra attributes on the point are ignored), failing the test if zero or more
// than one point matches.
func findInt64Point(t *testing.T, points []metricdata.DataPoint[int64], want map[string]string) int64 {
	t.Helper()
	var matches []metricdata.DataPoint[int64]
	for _, p := range points {
		if attrsMatch(p.Attributes, want) {
			matches = append(matches, p)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("found %d data points matching %v, want exactly 1 (all points: %v)", len(matches), want, describeInt64Points(points))
	}
	return matches[0].Value
}

// findHistogramPoint is findInt64Point's counterpart for histogram data points.
func findHistogramPoint(t *testing.T, points []metricdata.HistogramDataPoint[float64], want map[string]string) metricdata.HistogramDataPoint[float64] {
	t.Helper()
	var matches []metricdata.HistogramDataPoint[float64]
	for _, p := range points {
		if attrsMatch(p.Attributes, want) {
			matches = append(matches, p)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("found %d histogram points matching %v, want exactly 1", len(matches), want)
	}
	return matches[0]
}

func attrsMatch(set attribute.Set, want map[string]string) bool {
	for k, v := range want {
		got, ok := set.Value(attribute.Key(k))
		if !ok || got.AsString() != v {
			return false
		}
	}
	return true
}

func describeInt64Points(points []metricdata.DataPoint[int64]) []string {
	out := make([]string, len(points))
	for i, p := range points {
		out[i] = p.Attributes.Encoded(attribute.DefaultEncoder())
	}
	return out
}
