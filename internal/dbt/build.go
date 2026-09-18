package dbt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Runner shells out to the dbt CLI and parses the run_results.json artefact it leaves behind.
type Runner struct {
	// Bin is the dbt executable to run, resolved via exec.LookPath semantics (PATH lookup for a
	// bare name). Empty means "dbt".
	Bin string
	// WorkDir is dbt's working directory: normally the repository root, so the --project-dir and
	// --profiles-dir arguments (both relative) resolve exactly as they would for a human running
	// dbt from the repo root.
	WorkDir string
	// ProjectDir is passed to --project-dir, relative to WorkDir. Empty means "dbt".
	ProjectDir string
	// ProfilesDir is passed to --profiles-dir, relative to WorkDir. Empty means "dbt".
	ProfilesDir string
}

// Build ensures <WorkDir>/warehouse/marts exists (the directory dbt-duckdb's `external`
// materialisation writes Parquet into, which must pre-exist per spec F5), runs
// `<Bin> build --project-dir <ProjectDir> --profiles-dir <ProfilesDir> --select <selector>` with
// WorkDir as the process's working directory, and parses
// <WorkDir>/<ProjectDir>/target/run_results.json.
//
// If dbt exits non-zero, Build returns the parsed results together with an error naming the
// failed nodes, so a caller can still inspect what ran. If run_results.json is missing, or it was
// not rewritten by this invocation, Build returns an error and never reports a stale file's
// nodes: "rewritten by this invocation" is judged by the file's on-disk modification time against
// a start instant recorded before the dbt process runs, not by the artefact's own
// self-reported metadata.generated_at -- a crashed or skipped dbt run must not leave an old
// run_results.json that this check mistakes for a fresh one.
func (r Runner) Build(ctx context.Context, selector string) (RunResults, error) {
	bin := r.Bin
	if bin == "" {
		bin = "dbt"
	}
	projectDir := r.ProjectDir
	if projectDir == "" {
		projectDir = "dbt"
	}
	profilesDir := r.ProfilesDir
	if profilesDir == "" {
		profilesDir = "dbt"
	}

	martsDir := filepath.Join(r.WorkDir, "warehouse", "marts")
	if err := os.MkdirAll(martsDir, 0o755); err != nil {
		return RunResults{}, fmt.Errorf("dbt: create %s: %w", martsDir, err)
	}

	start := time.Now()

	cmd := exec.CommandContext(ctx, bin, "build",
		"--project-dir", projectDir,
		"--profiles-dir", profilesDir,
		"--select", selector,
	)
	cmd.Dir = r.WorkDir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	resultsPath := filepath.Join(r.WorkDir, projectDir, "target", "run_results.json")
	info, statErr := os.Stat(resultsPath)
	if statErr != nil {
		if runErr != nil {
			return RunResults{}, fmt.Errorf("dbt build --select %s: %w; dbt produced no run results (stderr: %s)",
				selector, runErr, strings.TrimSpace(stderr.String()))
		}
		return RunResults{}, fmt.Errorf("dbt produced no run results: %s not found", resultsPath)
	}
	if info.ModTime().Before(start) {
		return RunResults{}, fmt.Errorf(
			"dbt produced no run results: %s was not rewritten by this build (last modified %s, build started %s)",
			resultsPath, info.ModTime(), start)
	}

	f, err := os.Open(resultsPath)
	if err != nil {
		return RunResults{}, fmt.Errorf("dbt: open %s: %w", resultsPath, err)
	}
	defer f.Close()

	rr, parseErr := ParseRunResults(f)
	if parseErr != nil {
		return RunResults{}, fmt.Errorf("dbt: parse %s: %w", resultsPath, parseErr)
	}

	if runErr != nil {
		failed := rr.Failed()
		names := make([]string, len(failed))
		for i, n := range failed {
			names[i] = n.UniqueID
		}
		return rr, fmt.Errorf("dbt build --select %s failed for %d node(s): %s",
			selector, len(failed), strings.Join(names, ", "))
	}

	return rr, nil
}
