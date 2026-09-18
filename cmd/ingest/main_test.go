package main

// Behavioral tests for the `ingest` CLI's mode/flag validation, anchored to the "cmd/ingest" bullet
// of tasks/04-runner/brief.md's "Required content" section: "Exactly one mode must be given.
// Invalid combinations print usage and exit 2" and "--source (required; only beads is
// registered)". This is the entry point that makes AC-15's backfill command and AC-18's lookback
// command runnable as commands at all
// (docs/fable-streams/2026-09-18-phase-1-build/spec.md).
//
// These tests build the real `ingest` binary once, in TestMain (from this package's own directory
// — go test sets a package's own directory as its working directory), and run it as a subprocess
// for each case, rather than assuming any particular internal function name for flag parsing: the
// brief pins the CLI's observable flags and exit codes, not its internal shape. Every case here is
// chosen so it must fail during flag validation, before the command ever builds a beads Fetcher or
// dials the Dolt server (the brief lists "Build the beads Fetcher ..." after the flag-validation
// bullet), so these tests need no live database and no BEADS_* environment variables.
//
// The binary is built with `go build`, not run with `go run .`: `go run` does not propagate the
// child process's exit code — every nonzero exit, whether 1 or 2, is reported by the `go` tool
// itself as 1 (verified against this toolchain's `go run`; see the implementer's round-1 report
// for the repro). That would make every exit-2 assertion below indistinguishable from an exit-1
// failure, so the tests exec the built binary directly, whose exit code is exact.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// binPath is the path to the `ingest` binary TestMain builds once, shared by every test in this
// package.
var binPath string

// TestMain builds the ingest binary once into a temp directory, runs the package's tests against
// it, then removes the directory.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ingest-test-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ingest test: MkdirTemp:", err)
		os.Exit(1)
	}
	binPath = filepath.Join(dir, "ingest")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "ingest test: go build -o %s .: %v\n%s", binPath, err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// runIngest runs the built ingest binary with args, and returns its exit code and combined
// stdout+stderr.
func runIngest(t *testing.T, args ...string) (exitCode int, output string) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	out, err := cmd.CombinedOutput()
	output = string(out)
	if err == nil {
		return 0, output
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), output
	}
	t.Fatalf("%s %v: %v\noutput:\n%s", binPath, args, err, output)
	return -1, output
}

// Required content ("cmd/ingest"): "Exactly one mode must be given. Invalid combinations print
// usage and exit 2." No mode flag at all (neither --once nor --from/--to) is an invalid
// combination.
func TestIngest_NoModeGiven_ExitsUsage(t *testing.T) {
	code, out := runIngest(t, "--source", "beads")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (usage error: no mode given)\noutput:\n%s", code, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Errorf("exit 2 produced no output, want a usage message")
	}
}

// Required content ("cmd/ingest"): giving both a lookback flag and backfill flags at once is an
// invalid combination, since exactly one mode must be given.
func TestIngest_BothModesGiven_ExitsUsage(t *testing.T) {
	code, out := runIngest(t, "--source", "beads", "--once", "--from", "2026-09-14", "--to", "2026-09-15")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (usage error: both --once and --from/--to given)\noutput:\n%s", code, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Errorf("exit 2 produced no output, want a usage message")
	}
}

// Required content ("cmd/ingest"): "--source (required; only beads is registered)". Backfill
// mode's own flags given without --source is an invalid combination.
func TestIngest_MissingSource_ExitsUsage(t *testing.T) {
	code, out := runIngest(t, "--from", "2026-09-14", "--to", "2026-09-15")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (usage error: --source is required)\noutput:\n%s", code, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Errorf("exit 2 produced no output, want a usage message")
	}
}

// Required content ("cmd/ingest"): backfill mode's flags are "--from / --to" — a paired range.
// --from without --to is an incomplete backfill range and not a valid lookback run either, so it
// is an invalid combination.
func TestIngest_FromWithoutTo_ExitsUsage(t *testing.T) {
	code, out := runIngest(t, "--source", "beads", "--from", "2026-09-14")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (usage error: --from given without --to)\noutput:\n%s", code, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Errorf("exit 2 produced no output, want a usage message")
	}
}
