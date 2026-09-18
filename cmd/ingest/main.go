package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/runner"
	"github.com/DMokong/data-platform/internal/source/beads"
)

// dateLayout is the calendar-date form accepted by --from and --to.
const dateLayout = "2006-01-02"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses args, validates the flag combination, runs the Runner, prints a per-table summary
// and returns the process exit code: 2 for a usage error, 1 if any window failed, 0 on success.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	fs.SetOutput(stderr)

	sourceName := fs.String("source", "", "source name to ingest (only \"beads\" is registered)")
	from := fs.String("from", "", "backfill range start date, YYYY-MM-DD")
	to := fs.String("to", "", "backfill range end date, YYYY-MM-DD")
	once := fs.Bool("once", false, "run a lookback pass instead of a backfill")
	nowFlag := fs.String("now", "", "extraction clock for --once, RFC 3339 (default: current time)")
	root := fs.String("root", ".", "repo root; the bronze root is <root>/bronze")

	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: ingest --source <name> (--once [--now RFC3339] | --from YYYY-MM-DD --to YYYY-MM-DD) [--root DIR]")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return 2 // flag.ContinueOnError already printed the error and usage.
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	usageErr := func(msg string) int {
		fmt.Fprintln(stderr, "ingest:", msg)
		fs.Usage()
		return 2
	}

	if *sourceName == "" {
		return usageErr("--source is required")
	}
	if *sourceName != "beads" {
		return usageErr(fmt.Sprintf("unknown --source %q (only \"beads\" is registered)", *sourceName))
	}

	backfillMode := set["from"] || set["to"]
	lookbackMode := *once
	switch {
	case backfillMode && lookbackMode:
		return usageErr("give either --once or --from/--to, not both")
	case !backfillMode && !lookbackMode:
		return usageErr("exactly one mode is required: --once, or --from and --to")
	case backfillMode && !(set["from"] && set["to"]):
		return usageErr("backfill mode requires both --from and --to")
	case set["now"] && !lookbackMode:
		return usageErr("--now is only valid with --once")
	}

	var fromDate, toDate, nowTime time.Time
	var err error
	if backfillMode {
		if fromDate, err = time.Parse(dateLayout, *from); err != nil {
			return usageErr(fmt.Sprintf("--from %q: %v", *from, err))
		}
		if toDate, err = time.Parse(dateLayout, *to); err != nil {
			return usageErr(fmt.Sprintf("--to %q: %v", *to, err))
		}
	} else if set["now"] {
		if nowTime, err = time.Parse(time.RFC3339, *nowFlag); err != nil {
			return usageErr(fmt.Sprintf("--now %q: %v", *nowFlag, err))
		}
	} else {
		nowTime = time.Now()
	}

	cfg, err := beads.ConfigFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, "ingest:", err)
		return 1
	}
	fetcher, err := beads.NewFetcher(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "ingest:", err)
		return 1
	}
	defer fetcher.Close()

	rn := &runner.Runner{
		Source:  beads.Declaration(),
		Fetcher: fetcher,
		Writer:  &bronze.Writer{Root: filepath.Join(*root, "bronze")},
	}

	ctx := context.Background()
	var summary runner.Summary
	var runErr error
	if backfillMode {
		summary, runErr = rn.Backfill(ctx, fromDate, toDate)
	} else {
		summary, runErr = rn.RunOnce(ctx, nowTime)
	}

	for _, ts := range summary.Tables {
		fmt.Fprintf(stdout, "table=%s windows=%d rows=%d bytes=%d\n", ts.Table, ts.Windows, ts.Rows, ts.Bytes)
	}

	if runErr != nil {
		for _, ts := range summary.Tables {
			for _, f := range ts.Failed {
				fmt.Fprintf(stderr, "ingest: failed window: %s\n", f)
			}
		}
		return 1
	}
	return 0
}
