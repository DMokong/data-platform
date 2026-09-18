// Behavioral tests for internal/telemetry's Config/Setup surface -- AC-38 in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md: "internal/telemetry installs global
// tracer and meter providers with service.name=data-platform-worker. The exporter is chosen by
// PLATFORM_OTEL_EXPORTER: otlp (default: when no OTEL_EXPORTER_OTLP_* endpoint env is set, it
// targets 127.0.0.1:4317 insecure, the workspace Alloy collector, F8/F10), stdout, or none.
// Shutdown flushes."
package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/DMokong/data-platform/internal/telemetry"
)

// unsetEnv removes key from the environment for the duration of the test, restoring whatever was
// there (including "not set") afterwards. Unlike t.Setenv(key, ""), this makes the variable
// genuinely absent, which is what ConfigFromEnv's "no endpoint env var is set" branch checks for.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
}

// TestConfigFromEnv_DefaultsToOTLP_AC38 checks the documented default: with PLATFORM_OTEL_EXPORTER
// absent, ConfigFromEnv reports "otlp" and passes the caller's service name through unchanged.
func TestConfigFromEnv_DefaultsToOTLP_AC38(t *testing.T) {
	unsetEnv(t, "PLATFORM_OTEL_EXPORTER")

	cfg := telemetry.ConfigFromEnv("data-platform-worker")

	if cfg.ServiceName != "data-platform-worker" {
		t.Errorf("ServiceName = %q, want %q", cfg.ServiceName, "data-platform-worker")
	}
	if cfg.Exporter != "otlp" {
		t.Errorf("Exporter = %q, want the documented default %q", cfg.Exporter, "otlp")
	}
}

// TestConfigFromEnv_Overrides_AC38 checks that PLATFORM_OTEL_EXPORTER overrides the default for
// each documented exporter value.
func TestConfigFromEnv_Overrides_AC38(t *testing.T) {
	for _, exporter := range []string{"stdout", "none", "otlp"} {
		t.Run(exporter, func(t *testing.T) {
			t.Setenv("PLATFORM_OTEL_EXPORTER", exporter)
			cfg := telemetry.ConfigFromEnv("svc-under-test")
			if cfg.Exporter != exporter {
				t.Errorf("Exporter = %q, want %q", cfg.Exporter, exporter)
			}
			if cfg.ServiceName != "svc-under-test" {
				t.Errorf("ServiceName = %q, want %q", cfg.ServiceName, "svc-under-test")
			}
		})
	}
}

// TestSetup_UnknownExporter_Errors_AC38 checks that an unrecognized Exporter value is rejected
// rather than silently falling back to some default sink.
func TestSetup_UnknownExporter_Errors_AC38(t *testing.T) {
	_, err := telemetry.Setup(context.Background(), telemetry.Config{
		ServiceName: "svc-unknown-exporter",
		Exporter:    "carrier-pigeon",
	})
	if err == nil {
		t.Fatalf("Setup with Exporter=%q returned a nil error, want one", "carrier-pigeon")
	}
}

// TestSetup_None_InstallsRealProvider_AC38 checks that Exporter="none" is a real, working sink
// selection (no error, shutdown succeeds) rather than an unimplemented no-op branch: the span
// context produced by the installed global tracer must be valid, which only a real SDK
// TracerProvider produces (the untouched go.opentelemetry.io/otel API default produces invalid,
// all-zero span contexts).
func TestSetup_None_InstallsRealProvider_AC38(t *testing.T) {
	ctx := context.Background()
	shutdown, err := telemetry.Setup(ctx, telemetry.Config{ServiceName: "svc-none-test", Exporter: "none"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if shutdown == nil {
		t.Fatalf("Setup returned a nil shutdown func")
	}

	tracer := otel.Tracer("telemetry_setup_test")
	_, span := tracer.Start(ctx, "none-smoke-span")
	sc := span.SpanContext()
	span.End()

	if !sc.IsValid() {
		t.Errorf("global tracer span context after Setup(exporter=none) is invalid; want a real SDK TracerProvider installed (with a discarding exporter), not the untouched API no-op provider")
	}

	telemetry.RecordWrite(ctx, "beads", "issues", "bronze", 1, 10)
	telemetry.RecordActivity(ctx, "smoke", time.Millisecond, nil)

	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestSetup_Stdout_WritesSpanJSONAfterShutdown_AC38 checks that Exporter="stdout" routes span
// data to the configured io.Writer, and that it is actually there once Shutdown returns (Setup's
// documented contract: "Shutdown flushes both").
func TestSetup_Stdout_WritesSpanJSONAfterShutdown_AC38(t *testing.T) {
	var buf bytes.Buffer
	ctx := context.Background()

	shutdown, err := telemetry.Setup(ctx, telemetry.Config{
		ServiceName: "svc-stdout-test",
		Exporter:    "stdout",
		Stdout:      &buf,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	tracer := otel.Tracer("telemetry_setup_test")
	_, span := tracer.Start(ctx, "stdout-smoke-span")
	span.End()

	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	if buf.Len() == 0 {
		t.Fatalf("Shutdown returned but no span JSON was written to the configured writer")
	}

	dec := json.NewDecoder(&buf)
	var sawExpectedSpan bool
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if name, _ := doc["Name"].(string); name == "stdout-smoke-span" {
			sawExpectedSpan = true
		}
	}
	if !sawExpectedSpan {
		t.Fatalf("decoded stdout output did not contain a span named %q", "stdout-smoke-span")
	}
}

// TestSetup_OTLPDefaultEndpoint_AC38 exercises the "when no OTEL_EXPORTER_OTLP_* endpoint env is
// set, [otlp] targets 127.0.0.1:4317 insecure" half of AC-38, against the workspace's real local
// collector (spec F8: Grafana Alloy listens on 127.0.0.1:4317; F10: the OTel SDK's own default
// endpoint is "localhost", which resolves to ::1 first in this shell and hangs rather than
// connecting or failing fast). A bounded deadline on Setup+shutdown makes a regression to that
// SDK default fail fast here instead of hanging.
func TestSetup_OTLPDefaultEndpoint_AC38(t *testing.T) {
	for _, k := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	} {
		unsetEnv(t, k)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	shutdown, err := telemetry.Setup(ctx, telemetry.Config{ServiceName: "svc-otlp-default-test", Exporter: "otlp"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	telemetry.RecordActivity(ctx, "otlp-default-smoke", time.Millisecond, nil)

	done := make(chan error, 1)
	go func() { done <- shutdown(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("shutdown did not return within 5s -- looks like Setup targeted the SDK's own " +
			"\"localhost\" default (F10: resolves to ::1 and hangs) instead of the explicit " +
			"127.0.0.1:4317 fallback (F8/F10)")
	}
}
