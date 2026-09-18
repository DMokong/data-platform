package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/DMokong/data-platform/internal/dbt"
)

// EmitDbtResults turns a dbt run into the same telemetry stream as the rest of the orchestrator:
// one child span per node (of the span already in ctx), named dbt.<resource_type>.<name>, plus a
// platform.dbt.nodes counter incremented once per node under (status, resource_type).
func EmitDbtResults(ctx context.Context, selector string, rr dbt.RunResults) {
	tracer := otel.Tracer(instrumentationName)

	nodesCounter, counterErr := meter().Int64Counter("platform.dbt.nodes",
		metric.WithDescription("dbt nodes run, by status and resource type."),
		metric.WithUnit("{node}"),
	)

	for _, n := range rr.Results {
		start, end := nodeTiming(n)
		spanName := fmt.Sprintf("dbt.%s.%s", n.ResourceType, n.Name)

		_, span := tracer.Start(ctx, spanName, trace.WithTimestamp(start))
		span.SetAttributes(
			attribute.String("dbt.unique_id", n.UniqueID),
			attribute.String("dbt.status", n.Status),
			attribute.String("dbt.selector", selector),
			attribute.Float64("dbt.execution_time", n.ExecutionTime),
		)
		if n.Failures != nil {
			span.SetAttributes(attribute.Int("dbt.failures", *n.Failures))
		}
		if isDbtErrorStatus(n.Status) {
			span.SetStatus(codes.Error, n.Message)
		}
		span.End(trace.WithTimestamp(end))

		if counterErr == nil {
			nodesCounter.Add(ctx, 1, metric.WithAttributes(
				attribute.String("status", n.Status),
				attribute.String("resource_type", n.ResourceType),
			))
		}
	}
}

// nodeTiming picks the span's start and end instants: the node's "execute" timing if present,
// else its "compile" timing, else an instant derived from ExecutionTime alone (no timing entries
// at all -- the brief pins only that the derived duration equals ExecutionTime, not a specific
// anchor instant).
func nodeTiming(n dbt.NodeResult) (start, end time.Time) {
	for _, t := range n.Timing {
		if t.Name == "execute" {
			return t.StartedAt, t.CompletedAt
		}
	}
	for _, t := range n.Timing {
		if t.Name == "compile" {
			return t.StartedAt, t.CompletedAt
		}
	}
	start = time.Now()
	end = start.Add(time.Duration(n.ExecutionTime * float64(time.Second)))
	return start, end
}

// isDbtErrorStatus reports whether a node's dbt status should map to an Error span status:
// error, fail or runtime error -- the same set RunResults.Failed selects on.
func isDbtErrorStatus(status string) bool {
	switch status {
	case "error", "fail", "runtime error":
		return true
	default:
		return false
	}
}
