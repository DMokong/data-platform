// Behavioral tests for ParseRunResults and RunResults.Failed -- the parsing half of AC-39 in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md ("run_results.json becomes one span per dbt
// node ... with status and timing taken from the file, plus counter platform.dbt.nodes by
// status"): internal/telemetry.EmitDbtResults can only produce correct spans and counters if
// ParseRunResults first recovers the node data faithfully from the real dbt artefact shape.
package dbt_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DMokong/data-platform/internal/dbt"
)

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture %s: %v", name, err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// TestParseRunResults_PassFixture_AC39 checks the fields EmitDbtResults depends on: node counts,
// metadata (invocation id, generated_at, elapsed time), and that ResourceType/Name are derived
// from UniqueID and Timing entries decode to real instants.
func TestParseRunResults_PassFixture_AC39(t *testing.T) {
	rr, err := dbt.ParseRunResults(openFixture(t, "run_results_pass.json"))
	if err != nil {
		t.Fatalf("ParseRunResults: %v", err)
	}

	if got, want := len(rr.Results), 56; got != want {
		t.Fatalf("len(Results) = %d, want %d", got, want)
	}
	if rr.InvocationID != "12b85b89-63e5-41e1-bfe0-e41c25259d7c" {
		t.Errorf("InvocationID = %q", rr.InvocationID)
	}
	wantGenerated := time.Date(2026, 9, 18, 17, 2, 36, 151547000, time.UTC)
	if !rr.GeneratedAt.Equal(wantGenerated) {
		t.Errorf("GeneratedAt = %v, want %v", rr.GeneratedAt, wantGenerated)
	}
	if rr.ElapsedTime <= 0 {
		t.Errorf("ElapsedTime = %v, want > 0", rr.ElapsedTime)
	}

	var found bool
	for _, n := range rr.Results {
		if n.UniqueID != "model.data_platform.stg_beads__dependency_snapshots" {
			continue
		}
		found = true
		if n.ResourceType != "model" {
			t.Errorf("ResourceType = %q, want %q", n.ResourceType, "model")
		}
		if n.Name != "stg_beads__dependency_snapshots" {
			t.Errorf("Name = %q, want %q", n.Name, "stg_beads__dependency_snapshots")
		}
		if n.Status != "success" {
			t.Errorf("Status = %q, want %q", n.Status, "success")
		}
		if len(n.Timing) != 2 {
			t.Fatalf("len(Timing) = %d, want 2 (compile, execute)", len(n.Timing))
		}
		if n.Timing[0].Name != "compile" || n.Timing[1].Name != "execute" {
			t.Fatalf("Timing names = [%q, %q], want [compile, execute]", n.Timing[0].Name, n.Timing[1].Name)
		}
		exec := n.Timing[1]
		if !exec.CompletedAt.After(exec.StartedAt) {
			t.Errorf("execute CompletedAt %v not after StartedAt %v", exec.CompletedAt, exec.StartedAt)
		}
		wantExecStart := time.Date(2026, 9, 18, 17, 2, 35, 437031000, time.UTC)
		if !exec.StartedAt.Equal(wantExecStart) {
			t.Errorf("execute StartedAt = %v, want %v", exec.StartedAt, wantExecStart)
		}
	}
	if !found {
		t.Fatalf("fixture missing expected node model.data_platform.stg_beads__dependency_snapshots")
	}

	if failed := rr.Failed(); len(failed) != 0 {
		names := make([]string, len(failed))
		for i, n := range failed {
			names[i] = n.UniqueID
		}
		t.Errorf("Failed() on the passing fixture = %v, want none", names)
	}
}

// TestParseRunResults_FailFixture_AC39 checks that Failed() surfaces exactly the error/fail
// nodes the fixture was derived to contain, with their Message and Failures fields intact, and
// does not over- or under-report against every other node's status.
func TestParseRunResults_FailFixture_AC39(t *testing.T) {
	rr, err := dbt.ParseRunResults(openFixture(t, "run_results_fail.json"))
	if err != nil {
		t.Fatalf("ParseRunResults: %v", err)
	}

	failed := rr.Failed()
	if len(failed) != 2 {
		names := make([]string, len(failed))
		for i, n := range failed {
			names[i] = n.UniqueID
		}
		t.Fatalf("Failed() = %v (%d nodes), want 2", names, len(failed))
	}

	byID := make(map[string]dbt.NodeResult, len(failed))
	for _, n := range failed {
		byID[n.UniqueID] = n
	}

	const modelID = "model.data_platform.stg_beads__dependency_snapshots"
	model, ok := byID[modelID]
	if !ok {
		t.Fatalf("Failed() missing the errored model %s, got %v", modelID, failed)
	}
	if model.Status != "error" {
		t.Errorf("model Status = %q, want %q", model.Status, "error")
	}
	if model.Message == "" {
		t.Errorf("model Message is empty, want the dbt error text")
	}
	if model.ResourceType != "model" {
		t.Errorf("model ResourceType = %q, want %q", model.ResourceType, "model")
	}

	const testID = "test.data_platform.unique_stg_beads__dependency_snapshots_dependency_id_snapshot_date.67aa220841"
	test, ok := byID[testID]
	if !ok {
		t.Fatalf("Failed() missing the failed test %s, got %v", testID, failed)
	}
	if test.Status != "fail" {
		t.Errorf("test Status = %q, want %q", test.Status, "fail")
	}
	if test.Failures == nil || *test.Failures != 3 {
		t.Errorf("test Failures = %v, want *3", test.Failures)
	}
	if test.ResourceType != "test" {
		t.Errorf("test ResourceType = %q, want %q", test.ResourceType, "test")
	}
	// Name is documented as "last dot-segment of UniqueID"; for a hashed test id that is the
	// hash suffix, not the readable check name -- pin that literally so a future change to a
	// friendlier derivation is a visible, deliberate one.
	if test.Name != "67aa220841" {
		t.Errorf("test Name = %q, want %q (the last dot-segment of UniqueID)", test.Name, "67aa220841")
	}

	failedSet := make(map[string]bool, len(failed))
	for _, n := range failed {
		failedSet[n.UniqueID] = true
	}
	for _, n := range rr.Results {
		if failedSet[n.UniqueID] {
			continue
		}
		if n.Status != "success" && n.Status != "pass" {
			t.Errorf("node %s has status %q but was excluded from Failed()", n.UniqueID, n.Status)
		}
	}
}
