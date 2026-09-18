package orchestrator

import (
	"bufio"
	"encoding/gob"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/window"
)

// SpoolRef is the claim check FetchWindow hands the workflow in place of the rows: enough to find
// the spool file and to check it is the one expected, and small enough for any payload limit.
type SpoolRef struct {
	Path      string // absolute path of the spool file under <root>/local/spool/
	Source    string
	Table     string
	Window    window.Window
	Rows      int
	FetchedAt time.Time // UTC, the instant FetchWindow started reading the source → becomes _extracted_at
}

// spoolExt is the spool file extension; the content is one gob-encoded spoolFile.
const spoolExt = ".gob"

// spoolVersion is written into every spool file, so a reader built from different code rejects a
// file it would misread instead of guessing.
const spoolVersion = 1

// spoolFile is the on-disk form of a spool: the fetched batch plus the identity of the fetch it
// came from.
//
// The encoding is encoding/gob, which is lossless for everything a record.Batch can hold. A row
// value travels as an interface value tagged with its concrete Go type, so string, int64, float64
// and bool come back as exactly those types, with every bit of a float64 kept. A nil value is sent
// as an interface with no type and comes back as nil, never as a zero value. A time.Time travels
// through its MarshalBinary form: seconds, nanoseconds and the UTC marker, so a Timestamp's wall
// fields and its time.UTC location both survive.
type spoolFile struct {
	Version   int
	Source    string
	Table     string
	Window    window.Window
	FetchedAt time.Time
	Columns   []record.Column
	Rows      [][]any
}

func init() {
	// Row values are interface values. gob pre-registers string, int64, float64 and bool, but not
	// time.Time, the Go type of a record.Timestamp value.
	gob.Register(time.Time{})
}

// spoolPath returns <dir>/<source>/<table>/<window>-<runID>-<activityID><spoolExt>. runID and
// activityID come from Temporal and are sanitised to one path segment each; source and table are
// declared names.
func spoolPath(dir, source, table string, w window.Window, runID, activityID string) string {
	name := w.String() + "-" + pathSafe(runID) + "-" + pathSafe(activityID) + spoolExt
	return filepath.Join(dir, source, table, name)
}

// pathSafe maps every rune outside [A-Za-z0-9._-] to '_', and an empty string to "_", so a value
// can never add a path separator or climb out of its directory.
func pathSafe(s string) string {
	if s == "" {
		return "_"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
}

// writeSpool encodes sf into a temp file beside path, syncs and closes it, then renames it to
// path, so a reader sees either no spool or a complete one. On any failure the temp file is
// removed.
func writeSpool(path string, sf spoolFile) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("spool: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("spool: %w", err)
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = tmp.Close() // may already be closed; the file is being discarded either way
			_ = os.Remove(tmp.Name())
		}
	}()

	buf := bufio.NewWriter(tmp)
	if err := gob.NewEncoder(buf).Encode(&sf); err != nil {
		return fmt.Errorf("spool: encode %s: %w", path, err)
	}
	if err := buf.Flush(); err != nil {
		return fmt.Errorf("spool: write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("spool: sync %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("spool: close %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("spool: %w", err)
	}
	renamed = true
	return nil
}

// readSpool decodes the spool file at path.
func readSpool(path string) (spoolFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return spoolFile{}, fmt.Errorf("spool: %w", err)
	}
	defer f.Close()
	var sf spoolFile
	if err := gob.NewDecoder(bufio.NewReader(f)).Decode(&sf); err != nil {
		return spoolFile{}, fmt.Errorf("spool: decode %s: %w", path, err)
	}
	if sf.Version != spoolVersion {
		return spoolFile{}, fmt.Errorf("spool: %s has version %d, want %d", path, sf.Version, spoolVersion)
	}
	return sf, nil
}

// removeSpool deletes the spool file at path. A file that is already gone counts as success.
func removeSpool(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("spool: %w", err)
	}
	return nil
}

// batch returns the spooled rows as a record.Batch.
func (sf spoolFile) batch() record.Batch {
	return record.Batch{Columns: sf.Columns, Rows: sf.Rows}
}

// matches reports whether sf is the spool ref points at: same source, table, window, fetch
// instant and row count.
func (sf spoolFile) matches(ref SpoolRef) error {
	switch {
	case sf.Source != ref.Source || sf.Table != ref.Table:
		return fmt.Errorf("spool holds %s/%s, ref names %s/%s", sf.Source, sf.Table, ref.Source, ref.Table)
	case !sf.Window.Start.Equal(ref.Window.Start) || !sf.Window.End.Equal(ref.Window.End) || sf.Window.Grain != ref.Window.Grain:
		return fmt.Errorf("spool holds window %s, ref names %s", sf.Window, ref.Window)
	case !sf.FetchedAt.Equal(ref.FetchedAt):
		return fmt.Errorf("spool was fetched at %s, ref says %s", sf.FetchedAt.Format(time.RFC3339Nano), ref.FetchedAt.Format(time.RFC3339Nano))
	case len(sf.Rows) != ref.Rows:
		return fmt.Errorf("spool holds %d rows, ref says %d", len(sf.Rows), ref.Rows)
	}
	return nil
}
