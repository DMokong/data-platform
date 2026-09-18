// Behavioral tests for Runner.Build against testdata/fake-dbt.sh, a test double for the real dbt
// binary (task 09 reuses this exact script path). These pin AC-37's shape at the package level:
// the orchestrator's DbtBuild activity is only as correct as this Runner -- the exact command
// line, that warehouse/marts exists before dbt runs, the parsed pass result, that a dbt failure
// surfaces the failing node names, and that a stale run_results.json is never mistaken for a
// fresh one.
package dbt_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DMokong/data-platform/internal/dbt"
)

func fakeDBTPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "fake-dbt.sh"))
	if err != nil {
		t.Fatalf("abs path to fake-dbt.sh: %v", err)
	}
	if info, err := os.Stat(abs); err != nil {
		t.Fatalf("fake-dbt.sh not found at %s: %v", abs, err)
	} else if info.Mode()&0o111 == 0 {
		t.Fatalf("fake-dbt.sh at %s is not executable", abs)
	}
	return abs
}

func fixtureAbsPath(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("abs path to %s: %v", name, err)
	}
	return abs
}

// TestRunnerBuild_Success_AC39 pins the exact command Runner.Build shells out to, that it
// prepares <WorkDir>/warehouse/marts before running it, and that a clean exit produces the
// parsed pass fixture's results.
func TestRunnerBuild_Success_AC39(t *testing.T) {
	work := t.TempDir()
	argvFile := filepath.Join(work, "argv.txt")
	t.Setenv("FAKE_DBT_ARGV_FILE", argvFile)
	t.Setenv("FAKE_DBT_FIXTURE", fixtureAbsPath(t, "run_results_pass.json"))
	t.Setenv("FAKE_DBT_EXIT_CODE", "0")
	os.Unsetenv("FAKE_DBT_NO_WRITE")

	r := dbt.Runner{
		Bin:         fakeDBTPath(t),
		WorkDir:     work,
		ProjectDir:  "dbt",
		ProfilesDir: "dbt",
	}

	rr, err := r.Build(context.Background(), "tag:pre_go")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	gotArgv := strings.Split(strings.TrimRight(string(argvBytes), "\n"), "\n")
	wantArgv := []string{"build", "--project-dir", "dbt", "--profiles-dir", "dbt", "--select", "tag:pre_go"}
	if len(gotArgv) != len(wantArgv) {
		t.Fatalf("argv = %q, want %q", gotArgv, wantArgv)
	}
	for i := range wantArgv {
		if gotArgv[i] != wantArgv[i] {
			t.Errorf("argv[%d] = %q, want %q (full argv %q)", i, gotArgv[i], wantArgv[i], gotArgv)
		}
	}

	martsDir := filepath.Join(work, "warehouse", "marts")
	info, err := os.Stat(martsDir)
	if err != nil {
		t.Fatalf("warehouse/marts was not created under WorkDir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("warehouse/marts exists but is not a directory")
	}

	if got, want := len(rr.Results), 56; got != want {
		t.Errorf("len(Results) = %d, want %d", got, want)
	}
	if rr.InvocationID != "12b85b89-63e5-41e1-bfe0-e41c25259d7c" {
		t.Errorf("InvocationID = %q, want the pass fixture's invocation id", rr.InvocationID)
	}
	if len(rr.Failed()) != 0 {
		t.Errorf("Failed() = %v, want none for the pass fixture", rr.Failed())
	}
}

// TestRunnerBuild_FailureListsFailedNodes_AC39 checks that a non-zero dbt exit returns both the
// parsed results and a non-retryable-shaped error naming the failed nodes -- the orchestrator's
// DbtBuild activity (task 09, AC-37) needs the names to report a useful failure.
func TestRunnerBuild_FailureListsFailedNodes_AC39(t *testing.T) {
	work := t.TempDir()
	argvFile := filepath.Join(work, "argv.txt")
	t.Setenv("FAKE_DBT_ARGV_FILE", argvFile)
	t.Setenv("FAKE_DBT_FIXTURE", fixtureAbsPath(t, "run_results_fail.json"))
	t.Setenv("FAKE_DBT_EXIT_CODE", "1")
	os.Unsetenv("FAKE_DBT_NO_WRITE")

	r := dbt.Runner{
		Bin:         fakeDBTPath(t),
		WorkDir:     work,
		ProjectDir:  "dbt",
		ProfilesDir: "dbt",
	}

	rr, err := r.Build(context.Background(), "tag:pre_go")
	if err == nil {
		t.Fatalf("Build: got nil error for a non-zero dbt exit, want an error naming the failed nodes")
	}

	const wantModel = "model.data_platform.stg_beads__dependency_snapshots"
	const wantTest = "test.data_platform.unique_stg_beads__dependency_snapshots_dependency_id_snapshot_date.67aa220841"
	if !strings.Contains(err.Error(), wantModel) {
		t.Errorf("error %q does not name the failed model %s", err.Error(), wantModel)
	}
	if !strings.Contains(err.Error(), wantTest) {
		t.Errorf("error %q does not name the failed test %s", err.Error(), wantTest)
	}

	// "returns the parsed results together with an error" -- the caller can still inspect rr.
	if got, want := len(rr.Results), 56; got != want {
		t.Errorf("len(Results) = %d, want %d (parsed results should still be returned alongside the error)", got, want)
	}
	if got := len(rr.Failed()); got != 2 {
		t.Errorf("len(Failed()) = %d, want 2", got)
	}
}

// TestRunnerBuild_StaleRunResultsBeforeStart_AC39 covers "a run where the script writes no file
// while a stale one from before the start time exists: Build must error" -- the run_results.json
// left over from an earlier invocation must never be mistaken for this run's output.
func TestRunnerBuild_StaleRunResultsBeforeStart_AC39(t *testing.T) {
	work := t.TempDir()
	targetDir := filepath.Join(work, "dbt", "target")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	stale, err := os.ReadFile(fixtureAbsPath(t, "run_results_pass.json"))
	if err != nil {
		t.Fatalf("read pass fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "run_results.json"), stale, 0o644); err != nil {
		t.Fatalf("write stale run_results.json: %v", err)
	}
	// The fixture's generated_at (2026-09-18T17:02:36Z) predates any time this test can run.

	argvFile := filepath.Join(work, "argv.txt")
	t.Setenv("FAKE_DBT_ARGV_FILE", argvFile)
	t.Setenv("FAKE_DBT_NO_WRITE", "1")
	t.Setenv("FAKE_DBT_EXIT_CODE", "0")

	r := dbt.Runner{
		Bin:         fakeDBTPath(t),
		WorkDir:     work,
		ProjectDir:  "dbt",
		ProfilesDir: "dbt",
	}

	rr, err := r.Build(context.Background(), "tag:pre_go")
	if err == nil {
		t.Fatalf("Build: got nil error with only a stale run_results.json present, want an error")
	}
	if len(rr.Results) != 0 {
		t.Errorf("Build returned %d stale nodes despite erroring; want none -- \"never a stale file's nodes\"", len(rr.Results))
	}
}

// TestRunnerBuild_MissingRunResults_AC39 covers the other half of the same contract: no
// run_results.json at all (script wrote nothing, and none pre-existed).
func TestRunnerBuild_MissingRunResults_AC39(t *testing.T) {
	work := t.TempDir()
	argvFile := filepath.Join(work, "argv.txt")
	t.Setenv("FAKE_DBT_ARGV_FILE", argvFile)
	t.Setenv("FAKE_DBT_NO_WRITE", "1")
	t.Setenv("FAKE_DBT_EXIT_CODE", "0")

	r := dbt.Runner{
		Bin:         fakeDBTPath(t),
		WorkDir:     work,
		ProjectDir:  "dbt",
		ProfilesDir: "dbt",
	}

	rr, err := r.Build(context.Background(), "tag:pre_go")
	if err == nil {
		t.Fatalf("Build: got nil error with no run_results.json produced, want an error")
	}
	if len(rr.Results) != 0 {
		t.Errorf("Build returned %d nodes despite erroring; want none", len(rr.Results))
	}
}

// TestRunnerBuild_ContextRespected_AC39 is a light sanity check that Build takes ctx seriously:
// an already-cancelled context must not silently run the (fake) dbt binary and report success.
func TestRunnerBuild_ContextRespected_AC39(t *testing.T) {
	work := t.TempDir()
	argvFile := filepath.Join(work, "argv.txt")
	t.Setenv("FAKE_DBT_ARGV_FILE", argvFile)
	t.Setenv("FAKE_DBT_FIXTURE", fixtureAbsPath(t, "run_results_pass.json"))
	t.Setenv("FAKE_DBT_EXIT_CODE", "0")

	r := dbt.Runner{
		Bin:         fakeDBTPath(t),
		WorkDir:     work,
		ProjectDir:  "dbt",
		ProfilesDir: "dbt",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := r.Build(ctx, "tag:pre_go")
	if err == nil {
		t.Fatalf("Build with an already-cancelled context returned nil error, want one")
	}
}
