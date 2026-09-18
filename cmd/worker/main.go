package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"go.temporal.io/sdk/client"
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/dbt"
	"github.com/DMokong/data-platform/internal/orchestrator"
	"github.com/DMokong/data-platform/internal/source"
	"github.com/DMokong/data-platform/internal/source/beads"
	"github.com/DMokong/data-platform/internal/telemetry"
	"github.com/DMokong/data-platform/internal/transform"
)

// Temporal connection defaults. The address is 127.0.0.1, never localhost: localhost can resolve
// to ::1 first, where the dev server does not listen, and gRPC then hangs until it times out.
const (
	defaultTemporalAddress   = "127.0.0.1:7233"
	defaultTemporalNamespace = "default"
)

// maxConcurrentActivities caps the activities one worker runs at once, so a MaterialiseSource
// fan-out (26 windows, up to 4 tables each) cannot open dozens of Dolt queries together.
const maxConcurrentActivities = 8

// scheduleTimeout bounds each schedule upsert under -apply-schedule.
const scheduleTimeout = 30 * time.Second

// serviceName is the OpenTelemetry service.name of the worker's spans and metrics.
const serviceName = "data-platform-worker"

// telemetryShutdownTimeout bounds the final flush of spans and metrics when the worker stops, so
// an unreachable collector cannot hold up the exit (the live script allows 10 s after SIGTERM).
const telemetryShutdownTimeout = 5 * time.Second

// defaultDbtBin is the dbt executable when PLATFORM_DBT_BIN is unset: "dbt" on PATH.
const defaultDbtBin = "dbt"

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// temporalConfig is where the worker connects.
type temporalConfig struct {
	Address   string
	Namespace string
}

// temporalConfigFromEnv reads TEMPORAL_ADDRESS and TEMPORAL_NAMESPACE through getenv; an unset or
// empty variable takes its default.
func temporalConfigFromEnv(getenv func(string) string) temporalConfig {
	cfg := temporalConfig{Address: getenv("TEMPORAL_ADDRESS"), Namespace: getenv("TEMPORAL_NAMESPACE")}
	if cfg.Address == "" {
		cfg.Address = defaultTemporalAddress
	}
	if cfg.Namespace == "" {
		cfg.Namespace = defaultTemporalNamespace
	}
	return cfg
}

// workerOptions are the options the worker runs with.
func workerOptions() worker.Options {
	return worker.Options{MaxConcurrentActivityExecutionSize: maxConcurrentActivities}
}

// clientOptions are the options every Temporal client of this binary dials with; the worker adds
// its tracing interceptor and metrics handler to them.
func clientOptions(tcfg temporalConfig) client.Options {
	return client.Options{
		HostPort:  tcfg.Address,
		Namespace: tcfg.Namespace,
		// slog's default logger logs at INFO; the SDK's own default logs every command at DEBUG.
		Logger: tlog.NewStructuredLogger(slog.Default()),
	}
}

// run parses args, then either upserts the schedules (-apply-schedule) or runs the worker until
// SIGINT / SIGTERM. It returns the process exit code: 2 for a usage error, 1 for any other
// failure, 0 on success.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repo root; bronze is <root>/bronze, spool files go to <root>/local/spool, dbt runs from here")
	applySchedule := fs.Bool("apply-schedule", false, "upsert the schedule of every declared source, then exit without starting a worker")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: worker [-root DIR] [-apply-schedule]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2 // flag.ContinueOnError already printed the error and usage.
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "worker: unexpected arguments: %v\n", fs.Args())
		fs.Usage()
		return 2
	}

	tcfg := temporalConfigFromEnv(getenv)
	if *applySchedule {
		c, err := client.Dial(clientOptions(tcfg))
		if err != nil {
			fmt.Fprintf(stderr, "worker: connect to Temporal at %s (namespace %s): %v\n", tcfg.Address, tcfg.Namespace, err)
			return 1
		}
		defer c.Close()
		return applySchedules(c, orchestrator.Declarations(), stdout, stderr)
	}
	return runWorker(*root, tcfg, getenv, stdout, stderr)
}

// runWorker sets up telemetry first, so the Temporal client's tracing interceptor and metrics
// handler and the platform's own instruments all use the global providers it installs; then it
// connects, registers every workflow and activity, and polls until SIGINT / SIGTERM. Telemetry
// is shut down last, after the worker has stopped, which flushes the spans and metrics still
// buffered (with PLATFORM_OTEL_EXPORTER=stdout, onto stdout).
func runWorker(root string, tcfg temporalConfig, getenv func(string) string, stdout, stderr io.Writer) int {
	telCfg := telemetry.ConfigFromEnv(serviceName)
	telCfg.Stdout = stdout
	shutdownTelemetry, err := telemetry.Setup(context.Background(), telCfg)
	if err != nil {
		fmt.Fprintln(stderr, "worker:", err)
		return 1
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), telemetryShutdownTimeout)
		defer cancel()
		if err := shutdownTelemetry(ctx); err != nil {
			fmt.Fprintln(stderr, "worker: telemetry shutdown:", err)
		}
	}()

	tracing, err := temporalotel.NewTracingInterceptor(temporalotel.TracerOptions{})
	if err != nil {
		fmt.Fprintln(stderr, "worker: tracing interceptor:", err)
		return 1
	}
	opts := clientOptions(tcfg)
	opts.Interceptors = []interceptor.ClientInterceptor{tracing}
	opts.MetricsHandler = temporalotel.NewMetricsHandler(temporalotel.MetricsHandlerOptions{
		// The handler's default reaction to a meter error is to panic; a metric is not worth the
		// worker.
		OnError: func(err error) { slog.Warn("temporal metrics handler", "error", err) },
	})
	c, err := client.Dial(opts)
	if err != nil {
		fmt.Fprintf(stderr, "worker: connect to Temporal at %s (namespace %s): %v\n", tcfg.Address, tcfg.Namespace, err)
		return 1
	}
	defer c.Close()

	absRoot, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(stderr, "worker: -root %q: %v\n", root, err)
		return 1
	}
	bcfg, err := beads.ConfigFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, "worker:", err)
		return 1
	}
	fetcher, err := beads.NewFetcher(bcfg)
	if err != nil {
		fmt.Fprintln(stderr, "worker:", err)
		return 1
	}
	defer fetcher.Close()

	dbtBin := getenv("PLATFORM_DBT_BIN")
	if dbtBin == "" {
		dbtBin = defaultDbtBin
	}
	acts := &orchestrator.Activities{
		Sources:      orchestrator.Declarations(),
		Fetchers:     map[string]source.Fetcher{beads.Declaration().Name: fetcher},
		Writer:       &bronze.Writer{Root: filepath.Join(absRoot, "bronze")},
		SpoolDir:     filepath.Join(absRoot, "local", "spool"),
		DbtRunner:    dbt.Runner{Bin: dbtBin, WorkDir: absRoot},
		TransformEnv: transform.Env{WarehouseRoot: filepath.Join(absRoot, "warehouse")},
	}
	w := worker.New(c, orchestrator.TaskQueue, workerOptions())
	orchestrator.Register(w, acts)

	fmt.Fprintf(stdout, "worker: polling task queue %q at %s (namespace %s), root %s, dbt %s, telemetry %s\n",
		orchestrator.TaskQueue, tcfg.Address, tcfg.Namespace, absRoot, dbtBin, telCfg.Exporter)
	if err := w.Run(worker.InterruptCh()); err != nil {
		fmt.Fprintln(stderr, "worker:", err)
		return 1
	}
	fmt.Fprintln(stdout, "worker: stopped")
	return 0
}

// applySchedules upserts the schedule of every source, in name order, and reports each one.
func applySchedules(c client.Client, sources map[string]source.Source, stdout, stderr io.Writer) int {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		src := sources[name]
		ctx, cancel := context.WithTimeout(context.Background(), scheduleTimeout)
		err := orchestrator.ApplySchedule(ctx, c, src)
		cancel()
		if err != nil {
			fmt.Fprintln(stderr, "worker:", err)
			return 1
		}
		fmt.Fprintf(stdout, "worker: schedule %s applied: every %v, overlap skip, catch-up window %v, starts %s on %q\n",
			orchestrator.ScheduleID(name), src.Cadence, orchestrator.ScheduleCatchupWindow,
			orchestrator.WorkflowMaterialiseSource, orchestrator.TaskQueue)
	}
	return 0
}
