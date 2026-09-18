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
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/orchestrator"
	"github.com/DMokong/data-platform/internal/source"
	"github.com/DMokong/data-platform/internal/source/beads"
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

// run parses args, connects to Temporal and either upserts the schedules (-apply-schedule) or
// runs the worker until SIGINT / SIGTERM. It returns the process exit code: 2 for a usage error,
// 1 for any other failure, 0 on success.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repo root; bronze is <root>/bronze, spool files go to <root>/local/spool")
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
	c, err := client.Dial(client.Options{
		HostPort:  tcfg.Address,
		Namespace: tcfg.Namespace,
		// slog's default logger logs at INFO; the SDK's own default logs every command at DEBUG.
		Logger: tlog.NewStructuredLogger(slog.Default()),
	})
	if err != nil {
		fmt.Fprintf(stderr, "worker: connect to Temporal at %s (namespace %s): %v\n", tcfg.Address, tcfg.Namespace, err)
		return 1
	}
	defer c.Close()

	sources := orchestrator.Declarations()
	if *applySchedule {
		return applySchedules(c, sources, stdout, stderr)
	}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintf(stderr, "worker: -root %q: %v\n", *root, err)
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

	acts := &orchestrator.Activities{
		Sources:  sources,
		Fetchers: map[string]source.Fetcher{beads.Declaration().Name: fetcher},
		Writer:   &bronze.Writer{Root: filepath.Join(absRoot, "bronze")},
		SpoolDir: filepath.Join(absRoot, "local", "spool"),
	}
	w := worker.New(c, orchestrator.TaskQueue, workerOptions())
	orchestrator.Register(w, acts)

	fmt.Fprintf(stdout, "worker: polling task queue %q at %s (namespace %s), root %s\n",
		orchestrator.TaskQueue, tcfg.Address, tcfg.Namespace, absRoot)
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
