package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/DMokong/data-platform/internal/bronze"
	"github.com/DMokong/data-platform/internal/source"
	"github.com/DMokong/data-platform/internal/window"
)

// Activities holds what the ingestion activities need: the source declarations, one Fetcher per
// source, the bronze Writer and the spool directory. Its methods are the activities; Register
// registers them under the Activity* names.
type Activities struct {
	Sources  map[string]source.Source  // declarations by source name
	Fetchers map[string]source.Fetcher // by source name
	Writer   *bronze.Writer
	SpoolDir string           // <root>/local/spool
	Now      func() time.Time // extraction clock for FetchedAt; nil → time.Now
}

// FetchWindowInput names one table of one source and the window to fetch.
type FetchWindowInput struct {
	Source, Table string
	Window        window.Window
}

// Error types of the non-retryable failures below: retrying cannot fix a request for an unknown
// source or table, a malformed window, or a ref that points outside the spool directory.
const (
	errTypeBadInput = "BadInput"
	errTypeSetup    = "Setup"
)

func badInput(format string, args ...any) error {
	return temporal.NewNonRetryableApplicationError(fmt.Sprintf(format, args...), errTypeBadInput, nil)
}

func badSetup(format string, args ...any) error {
	return temporal.NewNonRetryableApplicationError(fmt.Sprintf(format, args...), errTypeSetup, nil)
}

// FetchWindow reads one declared table for one window from its source and parks the rows in a
// spool file, returning a SpoolRef instead of the rows (claim-check). FetchedAt is taken just
// before the read and becomes the bronze file's _extracted_at.
//
// The spool file is <SpoolDir>/<source>/<table>/<window>-<run id>-<activity id>.gob, written to a
// temp file in the same directory and renamed into place. A retry of this activity reads the
// source again and rewrites the same path; only the attempt that completes hands its ref on.
func (a *Activities) FetchWindow(ctx context.Context, in FetchWindowInput) (SpoolRef, error) {
	if err := a.checkTable(in.Source, in.Table, in.Window); err != nil {
		return SpoolRef{}, err
	}
	fetcher, ok := a.Fetchers[in.Source]
	if !ok || fetcher == nil {
		return SpoolRef{}, badSetup("orchestrator: no Fetcher for source %q", in.Source)
	}
	spoolDir, err := a.spoolDir()
	if err != nil {
		return SpoolRef{}, err
	}

	now := a.Now
	if now == nil {
		now = time.Now
	}
	// Bronze stores _extracted_at in microseconds; truncating here keeps FetchedAt and the
	// column exactly equal.
	fetchedAt := now().UTC().Truncate(time.Microsecond)

	activity.RecordHeartbeat(ctx, "fetch: querying source")
	batch, err := fetcher.Fetch(ctx, in.Table, in.Window)
	if err != nil {
		return SpoolRef{}, fmt.Errorf("orchestrator: fetch %s/%s %s: %w", in.Source, in.Table, in.Window, err)
	}
	activity.RecordHeartbeat(ctx, fmt.Sprintf("fetch: read %d rows", len(batch.Rows)))

	info := activity.GetInfo(ctx)
	path := spoolPath(spoolDir, in.Source, in.Table, in.Window, info.WorkflowExecution.RunID, info.ActivityID)
	err = writeSpool(path, spoolFile{
		Version:   spoolVersion,
		Source:    in.Source,
		Table:     in.Table,
		Window:    in.Window,
		FetchedAt: fetchedAt,
		Columns:   batch.Columns,
		Rows:      batch.Rows,
	})
	if err != nil {
		return SpoolRef{}, fmt.Errorf("orchestrator: %s/%s %s: %w", in.Source, in.Table, in.Window, err)
	}
	activity.RecordHeartbeat(ctx, "fetch: spool written")

	return SpoolRef{
		Path:      path,
		Source:    in.Source,
		Table:     in.Table,
		Window:    in.Window,
		Rows:      len(batch.Rows),
		FetchedAt: fetchedAt,
	}, nil
}

// WriteWindow redeems a SpoolRef: it reads the spool file and writes its rows to bronze as
// <source>/<table> for ref.Window, with ref.FetchedAt as _extracted_at. It does not delete the
// spool; the workflow runs DeleteSpool once WriteWindow has succeeded, so a retry whose earlier
// completion was lost still finds the file. The same ref always yields byte-identical bronze.
func (a *Activities) WriteWindow(ctx context.Context, ref SpoolRef) (bronze.Result, error) {
	if err := a.checkTable(ref.Source, ref.Table, ref.Window); err != nil {
		return bronze.Result{}, err
	}
	if err := a.checkSpoolPath(ref.Path); err != nil {
		return bronze.Result{}, err
	}
	if a.Writer == nil {
		return bronze.Result{}, badSetup("orchestrator: Activities.Writer is nil")
	}

	activity.RecordHeartbeat(ctx, "write: reading spool")
	sf, err := readSpool(ref.Path)
	if err != nil {
		return bronze.Result{}, fmt.Errorf("orchestrator: %s/%s %s: %w", ref.Source, ref.Table, ref.Window, err)
	}
	if err := sf.matches(ref); err != nil {
		return bronze.Result{}, badInput("orchestrator: spool %s does not match its ref: %v", ref.Path, err)
	}
	activity.RecordHeartbeat(ctx, "write: writing bronze")

	res, err := a.Writer.Write(ctx, bronze.Raw, ref.Source+"/"+ref.Table, ref.Window, sf.batch(), ref.FetchedAt)
	if err != nil {
		return bronze.Result{}, fmt.Errorf("orchestrator: %w", err)
	}
	activity.RecordHeartbeat(ctx, "write: bronze written")
	return res, nil
}

// DeleteSpool removes the spool file ref points at. It is idempotent: a file that is already gone
// counts as success.
func (a *Activities) DeleteSpool(ctx context.Context, ref SpoolRef) error {
	if err := a.checkSpoolPath(ref.Path); err != nil {
		return err
	}
	return removeSpool(ref.Path)
}

// checkTable reports whether source sourceName declares table tableName and w is a well-formed
// window of that table's grain.
func (a *Activities) checkTable(sourceName, tableName string, w window.Window) error {
	src, ok := a.Sources[sourceName]
	if !ok {
		return badInput("orchestrator: unknown source %q", sourceName)
	}
	t, ok := src.Table(tableName)
	if !ok {
		return badInput("orchestrator: source %q declares no table %q", sourceName, tableName)
	}
	if err := w.Validate(); err != nil {
		return badInput("orchestrator: %s/%s: %v", sourceName, tableName, err)
	}
	if w.Grain != t.Grain {
		return badInput("orchestrator: %s/%s is extracted in %s windows, got a %s window", sourceName, tableName, t.Grain, w.Grain)
	}
	return nil
}

// spoolDir returns SpoolDir as an absolute path, so a SpoolRef names the same file whatever the
// reading process's working directory.
func (a *Activities) spoolDir() (string, error) {
	if a.SpoolDir == "" {
		return "", badSetup("orchestrator: Activities.SpoolDir is empty")
	}
	dir, err := filepath.Abs(a.SpoolDir)
	if err != nil {
		return "", badSetup("orchestrator: SpoolDir %q: %v", a.SpoolDir, err)
	}
	return dir, nil
}

// checkSpoolPath rejects a ref path that is not an absolute path strictly inside SpoolDir, so a
// ref can never make WriteWindow read, or DeleteSpool remove, any other file.
func (a *Activities) checkSpoolPath(path string) error {
	dir, err := a.spoolDir()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		return badInput("orchestrator: spool path %q is not absolute", path)
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return badInput("orchestrator: spool path %q is not inside %s", path, dir)
	}
	return nil
}
