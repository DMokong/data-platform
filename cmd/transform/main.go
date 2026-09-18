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
	"github.com/DMokong/data-platform/internal/transform"
	"github.com/DMokong/data-platform/internal/transform/catalog"
	"github.com/DMokong/data-platform/internal/window"
)

// dateLayout is the calendar-date form accepted by --window.
const dateLayout = "2006-01-02"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses args -- the transform name first, positional, then flags from the remaining args --
// runs that transform for the given window and prints a one-line summary. It returns the process
// exit code: 2 for a usage error or an unknown transform name, 1 if the run itself fails, 0 on
// success.
func run(args []string, stdout, stderr io.Writer) int {
	usage := func() {
		fmt.Fprintln(stderr, "Usage: transform <name> --window YYYY-MM-DD [--root DIR]")
	}

	if len(args) == 0 {
		fmt.Fprintln(stderr, "transform: a transform name is required")
		usage()
		return 2
	}
	name := args[0]

	fs := flag.NewFlagSet("transform", flag.ContinueOnError)
	fs.SetOutput(stderr)
	windowStr := fs.String("window", "", "window date, YYYY-MM-DD")
	root := fs.String("root", ".", "repo root; warehouse is <root>/warehouse, bronze is <root>/bronze")
	fs.Usage = func() {
		usage()
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2 // flag.ContinueOnError already printed the error and usage.
	}

	t, ok := catalog.Lookup(name)
	if !ok {
		fmt.Fprintf(stderr, "transform: unknown transform %q\n", name)
		fmt.Fprintln(stderr, "known transforms:")
		for _, tr := range catalog.All() {
			fmt.Fprintf(stderr, "  %s\n", tr.Contract().Name)
		}
		return 2
	}

	if *windowStr == "" {
		fmt.Fprintln(stderr, "transform: --window is required")
		usage()
		return 1
	}
	date, err := time.Parse(dateLayout, *windowStr)
	if err != nil {
		fmt.Fprintf(stderr, "transform: invalid --window %q: %v\n", *windowStr, err)
		return 1
	}
	w := window.Containing(date, window.Day)

	env := transform.Env{WarehouseRoot: filepath.Join(*root, "warehouse")}
	wr := &bronze.Writer{Root: filepath.Join(*root, "bronze")}

	res, err := transform.Materialise(context.Background(), t, env, wr, w, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "transform: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "transform=%s window=%s rows=%d path=%s\n", name, w.String(), res.Rows, res.Path)
	return 0
}
