package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// defaultOTLPEndpoint is the workspace's local Alloy collector (spec F8). The OTel SDK's own
// default target is "localhost", which resolves to ::1 first in this shell and hangs rather than
// connecting or failing fast (spec F10); Alloy listens on 127.0.0.1 only, so this fallback is
// used whenever the caller hasn't set one of the standard OTEL_EXPORTER_OTLP_*_ENDPOINT vars.
const defaultOTLPEndpoint = "127.0.0.1:4317"

// otlpEndpointEnvVars are the standard env vars that, if any is set, mean the caller (or the
// standard OTel env-var machinery the gRPC exporters read on their own) has already chosen an
// OTLP endpoint; ConfigFromEnv/Setup must not override that choice with the Alloy default.
var otlpEndpointEnvVars = []string{
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
}

// Config configures Setup.
type Config struct {
	// ServiceName populates the resource's service.name attribute.
	ServiceName string
	// Exporter selects the telemetry sink: "otlp" (the default), "stdout" or "none".
	Exporter string
	// Stdout is where span and metric JSON is written when Exporter == "stdout". nil means
	// os.Stdout.
	Stdout io.Writer
}

// ConfigFromEnv builds a Config for serviceName from the environment: PLATFORM_OTEL_EXPORTER
// selects the exporter ("otlp", "stdout" or "none"), defaulting to "otlp" when unset. The OTLP
// endpoint itself is not read here -- Setup resolves it, honouring the standard
// OTEL_EXPORTER_OTLP_* env vars when set and falling back to the workspace's local Alloy
// collector otherwise (see Setup).
func ConfigFromEnv(serviceName string) Config {
	exporter := os.Getenv("PLATFORM_OTEL_EXPORTER")
	if exporter == "" {
		exporter = "otlp"
	}
	return Config{ServiceName: serviceName, Exporter: exporter}
}

// Setup installs global TracerProvider and MeterProvider instances carrying a resource with
// service.name=cfg.ServiceName, sending data to the sink cfg.Exporter names. An unknown exporter
// value is an error. The returned shutdown func flushes and closes both providers; callers should
// defer it.
func Setup(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	res := sdkresource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(cfg.ServiceName))

	tracerOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	meterOpts := []sdkmetric.Option{sdkmetric.WithResource(res)}

	switch cfg.Exporter {
	case "otlp":
		traceExp, metricExp, err := newOTLPExporters(ctx)
		if err != nil {
			return nil, err
		}
		tracerOpts = append(tracerOpts, sdktrace.WithBatcher(traceExp))
		meterOpts = append(meterOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)))

	case "stdout":
		w := cfg.Stdout
		if w == nil {
			w = os.Stdout
		}
		traceExp, err := stdouttrace.New(stdouttrace.WithWriter(w))
		if err != nil {
			return nil, fmt.Errorf("telemetry: stdout trace exporter: %w", err)
		}
		metricExp, err := stdoutmetric.New(stdoutmetric.WithWriter(w))
		if err != nil {
			return nil, fmt.Errorf("telemetry: stdout metric exporter: %w", err)
		}
		tracerOpts = append(tracerOpts, sdktrace.WithBatcher(traceExp))
		meterOpts = append(meterOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)))

	case "none":
		// Real SDK providers, no exporter registered: spans and metrics are created (so callers
		// get valid span contexts and working instruments) but never leave the process.

	default:
		return nil, fmt.Errorf("telemetry: unknown exporter %q (want otlp, stdout or none)", cfg.Exporter)
	}

	tp := sdktrace.NewTracerProvider(tracerOpts...)
	mp := sdkmetric.NewMeterProvider(meterOpts...)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	shutdown = func(ctx context.Context) error {
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
	}
	return shutdown, nil
}

// newOTLPExporters builds the gRPC trace and metric exporters. When none of the standard
// OTEL_EXPORTER_OTLP_*_ENDPOINT env vars is set, both are pointed explicitly at
// defaultOTLPEndpoint over an insecure (plaintext) connection (spec F8, F10); otherwise no
// endpoint option is passed, so the exporters fall back to their own standard env var handling.
func newOTLPExporters(ctx context.Context) (*otlptrace.Exporter, *otlpmetricgrpc.Exporter, error) {
	var traceOpts []otlptracegrpc.Option
	var metricOpts []otlpmetricgrpc.Option
	if !otlpEndpointConfigured() {
		traceOpts = append(traceOpts, otlptracegrpc.WithEndpoint(defaultOTLPEndpoint), otlptracegrpc.WithInsecure())
		metricOpts = append(metricOpts, otlpmetricgrpc.WithEndpoint(defaultOTLPEndpoint), otlpmetricgrpc.WithInsecure())
	}

	traceExp, err := otlptracegrpc.New(ctx, traceOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("telemetry: otlp trace exporter: %w", err)
	}
	metricExp, err := otlpmetricgrpc.New(ctx, metricOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("telemetry: otlp metric exporter: %w", err)
	}
	return traceExp, metricExp, nil
}

func otlpEndpointConfigured() bool {
	for _, k := range otlpEndpointEnvVars {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			return true
		}
	}
	return false
}
