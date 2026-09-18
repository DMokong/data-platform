package main

// Tests for the worker command's configuration and flag handling. Neither needs a Temporal
// server: flag errors return before the client dials, and the rest are pure functions. The live
// script (scripts/live-materialise.sh) runs the built binary against a dev server.

import (
	"bytes"
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// F10: the default address is 127.0.0.1, never localhost.
func TestTemporalConfigFromEnv_Defaults(t *testing.T) {
	got := temporalConfigFromEnv(envMap(nil))
	if got.Address != "127.0.0.1:7233" {
		t.Errorf("default address = %q, want %q", got.Address, "127.0.0.1:7233")
	}
	if got.Namespace != "default" {
		t.Errorf("default namespace = %q, want %q", got.Namespace, "default")
	}
}

func TestTemporalConfigFromEnv_Overrides(t *testing.T) {
	got := temporalConfigFromEnv(envMap(map[string]string{
		"TEMPORAL_ADDRESS":   "10.0.0.5:7233",
		"TEMPORAL_NAMESPACE": "data",
	}))
	if got.Address != "10.0.0.5:7233" || got.Namespace != "data" {
		t.Errorf("config = %+v, want address 10.0.0.5:7233 and namespace data", got)
	}
}

func TestWorkerOptions_LimitConcurrentActivities(t *testing.T) {
	if n := workerOptions().MaxConcurrentActivityExecutionSize; n != 8 {
		t.Errorf("MaxConcurrentActivityExecutionSize = %d, want 8", n)
	}
}

// Usage errors exit 2 before any connection is attempted. TEMPORAL_ADDRESS points at a port
// nothing listens on, so a run that got as far as dialling would fail differently (exit 1).
func TestRun_UsageErrorsExit2(t *testing.T) {
	env := envMap(map[string]string{"TEMPORAL_ADDRESS": "127.0.0.1:1"})
	for _, args := range [][]string{{"-bogus"}, {"-apply-schedule", "extra"}, {"-apply-schedule=maybe"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, env, &stdout, &stderr); code != 2 {
			t.Errorf("run(%v) = %d, want 2 (stderr: %s)", args, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "Usage: worker") {
			t.Errorf("run(%v) printed no usage: %q", args, stderr.String())
		}
	}
}
