// Behavioral tests for EmitDbtResults -- AC-39: "run_results.json becomes one span per dbt node,
// named dbt.<resource_type>.<name> with status and timing taken from the file, plus counter
// platform.dbt.nodes by status," and the brief's more detailed pins: timestamps taken from
// execute timing, else compile, else derived from ExecutionTime; attributes dbt.unique_id,
// dbt.status, dbt.selector, dbt.execution_time and dbt.failures (when set); Error status for
// error/fail/runtime error; one child span per node of the span already in ctx.
package telemetry_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/DMokong/data-platform/internal/dbt"
	"github.com/DMokong/data-platform/internal/telemetry"
)

func intPtr(n int) *int { return &n }

// buildSampleRunResults returns a small, hand-built dbt.RunResults exercising every timing
// fallback the brief pins: an execute timing, a compile-only timing, no timing at all, and a
// second success/model node (so the platform.dbt.nodes counter test can tell "always report 1"
// apart from a real per-status sum).
func buildSampleRunResults() (dbt.RunResults, map[string]dbt.NodeResult) {
	execStart := time.Date(2026, 9, 18, 17, 2, 35, 420000000, time.UTC)
	execEnd := execStart.Add(55 * time.Millisecond)
	compileStart := execStart.Add(-2 * time.Millisecond)
	compileEnd := execStart

	testExecStart := time.Date(2026, 9, 18, 17, 2, 35, 500000000, time.UTC)
	testExecEnd := testExecStart.Add(10 * time.Millisecond)

	nodes := []dbt.NodeResult{
		{
			UniqueID: "model.data_platform.dim_issues", ResourceType: "model", Name: "dim_issues",
			Status: "success", ExecutionTime: 0.055,
			Timing: []dbt.Timing{
				{Name: "compile", StartedAt: compileStart, CompletedAt: compileEnd},
				{Name: "execute", StartedAt: execStart, CompletedAt: execEnd},
			},
		},
		{
			UniqueID: "model.data_platform.dim_issues2", ResourceType: "model", Name: "dim_issues2",
			Status: "success", ExecutionTime: 0.01,
			Timing: []dbt.Timing{
				{Name: "execute", StartedAt: execStart, CompletedAt: execEnd},
			},
		},
		{
			UniqueID: "model.data_platform.dim_broken", ResourceType: "model", Name: "dim_broken",
			Status: "error", ExecutionTime: 0.03, Message: "Binder Error: boom",
			Timing: []dbt.Timing{
				{Name: "compile", StartedAt: compileStart, CompletedAt: compileEnd},
			},
		},
		{
			UniqueID: "test.data_platform.unique_dim_issues_id.a1b2c3", ResourceType: "test", Name: "a1b2c3",
			Status: "fail", ExecutionTime: 0.02, Failures: intPtr(4), Message: "Got 4 results, expected 0",
		},
		{
			UniqueID: "test.data_platform.not_null_dim_issues_id.d4e5f6", ResourceType: "test", Name: "d4e5f6",
			Status: "pass", ExecutionTime: 0.01,
			Timing: []dbt.Timing{
				{Name: "execute", StartedAt: testExecStart, CompletedAt: testExecEnd},
			},
		},
	}

	byID := make(map[string]dbt.NodeResult, len(nodes))
	for _, n := range nodes {
		byID[n.UniqueID] = n
	}

	rr := dbt.RunResults{
		InvocationID: "inv-ac39",
		GeneratedAt:  time.Date(2026, 9, 18, 17, 2, 36, 0, time.UTC),
		ElapsedTime:  1.2,
		Results:      nodes,
	}
	return rr, byID
}

func spanByName(spans []sdktrace.ReadOnlySpan, name string) (sdktrace.ReadOnlySpan, bool) {
	for _, s := range spans {
		if s.Name() == name {
			return s, true
		}
	}
	return nil, false
}

func attrValue(s sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.Emit(), true
		}
	}
	return "", false
}

// TestEmitDbtResults_SpanNamesStatusesAndTimestamps_AC39 checks span naming, the three documented
// timestamp fallbacks (execute, compile, ExecutionTime-derived), Error status for failing nodes,
// and that each node span is a child of the span already in ctx.
func TestEmitDbtResults_SpanNamesStatusesAndTimestamps_AC39(t *testing.T) {
	recorder := newTestTracerProvider(t)
	_ = newTestMeterProvider(t) // EmitDbtResults also touches the global MeterProvider; give it one.

	rr, nodes := buildSampleRunResults()

	tracer := otel.Tracer("telemetry_dbtspans_test")
	parentCtx, parentSpan := tracer.Start(context.Background(), "parent-build-span")

	telemetry.EmitDbtResults(parentCtx, "tag:pre_go", rr)
	parentSpan.End()

	ended := recorder.Ended()

	cases := []struct {
		spanName      string
		nodeID        string
		wantStart     time.Time
		wantEnd       time.Time
		wantErrStatus bool
	}{
		{"dbt.model.dim_issues", "model.data_platform.dim_issues", nodes["model.data_platform.dim_issues"].Timing[1].StartedAt, nodes["model.data_platform.dim_issues"].Timing[1].CompletedAt, false},
		{"dbt.model.dim_broken", "model.data_platform.dim_broken", nodes["model.data_platform.dim_broken"].Timing[0].StartedAt, nodes["model.data_platform.dim_broken"].Timing[0].CompletedAt, true},
		{"dbt.test.d4e5f6", "test.data_platform.not_null_dim_issues_id.d4e5f6", nodes["test.data_platform.not_null_dim_issues_id.d4e5f6"].Timing[0].StartedAt, nodes["test.data_platform.not_null_dim_issues_id.d4e5f6"].Timing[0].CompletedAt, false},
	}

	for _, c := range cases {
		s, ok := spanByName(ended, c.spanName)
		if !ok {
			t.Fatalf("no span named %q among ended spans", c.spanName)
		}
		node := nodes[c.nodeID]

		if got, ok := attrValue(s, "dbt.unique_id"); !ok || got != node.UniqueID {
			t.Errorf("%s: dbt.unique_id = %q, ok=%v, want %q", c.spanName, got, ok, node.UniqueID)
		}
		if got, ok := attrValue(s, "dbt.status"); !ok || got != node.Status {
			t.Errorf("%s: dbt.status = %q, ok=%v, want %q", c.spanName, got, ok, node.Status)
		}
		if got, ok := attrValue(s, "dbt.selector"); !ok || got != "tag:pre_go" {
			t.Errorf("%s: dbt.selector = %q, ok=%v, want %q", c.spanName, got, ok, "tag:pre_go")
		}
		if _, ok := attrValue(s, "dbt.execution_time"); !ok {
			t.Errorf("%s: missing dbt.execution_time attribute", c.spanName)
		}

		if !s.StartTime().Equal(c.wantStart) {
			t.Errorf("%s: StartTime = %v, want %v", c.spanName, s.StartTime(), c.wantStart)
		}
		if !s.EndTime().Equal(c.wantEnd) {
			t.Errorf("%s: EndTime = %v, want %v", c.spanName, s.EndTime(), c.wantEnd)
		}

		gotErr := s.Status().Code == codes.Error
		if gotErr != c.wantErrStatus {
			t.Errorf("%s: Status().Code == codes.Error is %v, want %v (status=%q)", c.spanName, gotErr, c.wantErrStatus, node.Status)
		}

		if s.Parent().SpanID() != parentSpan.SpanContext().SpanID() {
			t.Errorf("%s: Parent().SpanID() = %v, want the parent-build-span's span id %v", c.spanName, s.Parent().SpanID(), parentSpan.SpanContext().SpanID())
		}
		if s.Parent().TraceID() != parentSpan.SpanContext().TraceID() {
			t.Errorf("%s: Parent().TraceID() = %v, want the parent-build-span's trace id %v", c.spanName, s.Parent().TraceID(), parentSpan.SpanContext().TraceID())
		}
	}

	// The no-Timing-at-all fallback (test.a1b2c3, status "fail"): the brief only pins that it
	// falls back to ExecutionTime, not a specific anchor instant, so assert the derived duration
	// equals ExecutionTime and that the fail status still maps to an Error span status.
	failNode := nodes["test.data_platform.unique_dim_issues_id.a1b2c3"]
	failSpan, ok := spanByName(ended, "dbt.test.a1b2c3")
	if !ok {
		t.Fatalf("no span named %q among ended spans", "dbt.test.a1b2c3")
	}
	gotDur := failSpan.EndTime().Sub(failSpan.StartTime())
	wantDur := time.Duration(failNode.ExecutionTime * float64(time.Second))
	if diff := gotDur - wantDur; diff > time.Millisecond || diff < -time.Millisecond {
		t.Errorf("dbt.test.a1b2c3: EndTime-StartTime = %v, want ~%v (derived from ExecutionTime=%v)", gotDur, wantDur, failNode.ExecutionTime)
	}
	if failSpan.Status().Code != codes.Error {
		t.Errorf("dbt.test.a1b2c3: Status().Code = %v, want codes.Error (status=%q)", failSpan.Status().Code, failNode.Status)
	}
	if got, ok := attrValue(failSpan, "dbt.failures"); !ok || got != "4" {
		t.Errorf("dbt.test.a1b2c3: dbt.failures = %q, ok=%v, want %q", got, ok, "4")
	}

	// dbt.failures must be absent when Failures is nil (e.g. the passing model node) -- it is
	// not a "0" placeholder.
	passSpan, ok := spanByName(ended, "dbt.model.dim_issues")
	if !ok {
		t.Fatalf("no span named %q among ended spans", "dbt.model.dim_issues")
	}
	if _, ok := attrValue(passSpan, "dbt.failures"); ok {
		t.Errorf("dbt.model.dim_issues: dbt.failures attribute present, want absent (Failures is nil)")
	}
}

// TestEmitDbtResults_NodeCounterByStatus_AC39 checks that platform.dbt.nodes increments once per
// node under (status, resource_type), including a genuine sum (two success/model nodes) rather
// than a hardcoded count.
func TestEmitDbtResults_NodeCounterByStatus_AC39(t *testing.T) {
	_ = newTestTracerProvider(t)
	reader := newTestMeterProvider(t)

	rr, _ := buildSampleRunResults()
	telemetry.EmitDbtResults(context.Background(), "tag:pre_go", rr)

	counter := collectSumInt64(t, reader, "platform.dbt.nodes")

	cases := []struct {
		status, resourceType string
		want                 int64
	}{
		{"success", "model", 2},
		{"error", "model", 1},
		{"fail", "test", 1},
		{"pass", "test", 1},
	}
	for _, c := range cases {
		got := findInt64Point(t, counter.DataPoints, map[string]string{"status": c.status, "resource_type": c.resourceType})
		if got != c.want {
			t.Errorf("platform.dbt.nodes(status=%s,resource_type=%s) = %d, want %d", c.status, c.resourceType, got, c.want)
		}
	}
}
