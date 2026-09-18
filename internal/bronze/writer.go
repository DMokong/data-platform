package bronze

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
	"github.com/parquet-go/parquet-go/deprecated"

	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/window"
)

// Provenance column names. The Writer appends these four columns, in this order, after a
// batch's own columns, and rejects a batch that already carries any of them, in any letter case.
const (
	ColExtractedAt = "_extracted_at"
	ColWindowStart = "_window_start"
	ColWindowEnd   = "_window_end"
	ColSourceFile  = "_source_file"
)

// provenanceCollision reports which provenance column, if any, a batch column name collides
// with. The match ignores letter case because DuckDB resolves column names case-insensitively: a
// batch column "_EXTRACTED_AT" would make DuckDB rename the real provenance column to
// "_extracted_at_1", and `select _extracted_at` would then read the batch's value.
func provenanceCollision(name string) (string, bool) {
	for _, p := range [...]string{ColExtractedAt, ColWindowStart, ColWindowEnd, ColSourceFile} {
		if strings.EqualFold(name, p) {
			return p, true
		}
	}
	return "", false
}

// Writer writes one Parquet file per window under Root, at the path Path derives from the
// window, replacing any earlier file for the same window.
type Writer struct {
	Root string // the bronze root directory, e.g. "<repo>/bronze"
}

// Result describes a file the Writer put in place.
type Result struct {
	Path  string // bronze-relative, identical to Path(...)
	Rows  int
	Bytes int64
}

// forceEncodeErr, when non-nil, is returned by (*Writer).Write in place of a successful encode,
// as if the Parquet encoder had failed after Batch validation but before (or during) writing rows
// to the temp file. Write must still remove the temp file it had begun (or never create one) and
// leave any existing target file untouched, exactly like any other encode failure (AC-05). Always
// nil in production; only this package's own tests ever set it, and they reset it afterwards.
var forceEncodeErr error

// Fixed file settings. Everything that reaches the file's bytes is pinned here, so the output
// depends only on the batch, the window and extractedAt (AC-04).
const (
	// schemaName is the name of the Parquet schema's root element.
	schemaName = "bronze"
	// createdBy* fill the footer's created_by field. parquet-go's default embeds the build
	// information of whichever binary wrote the file; a constant keeps the footer identical
	// across binaries.
	createdByApp     = "github.com/DMokong/data-platform/internal/bronze"
	createdByVersion = "1"
	createdByBuild   = "none"
	// fileMode is the bronze file's permission. os.CreateTemp creates 0600; a bronze file is
	// shared data, so it gets the mode an ordinary os.Create would give it under umask 022.
	fileMode os.FileMode = 0o644
)

// codec is the one compression codec for every bronze column: ZSTD at the default level, one
// encoder goroutine per call. The klauspost zstd encoder behind it is pure Go on every
// architecture, so a window encodes to the same bytes on an arm64 laptop and an amd64 node.
var codec = &zstd.Codec{Level: zstd.SpeedDefault, Concurrency: 1}

// Write turns b, the rows of one window, into exactly one Parquet file at Path(zone, dataset, w)
// under wr.Root, and returns that path with the row and byte counts.
//
// The file holds the batch's columns first, in batch order, all OPTIONAL, then the REQUIRED
// provenance columns _extracted_at (extractedAt truncated to microseconds), _window_start,
// _window_end (all INT64 TIMESTAMP(MICROS, isAdjustedToUTC=true)) and _source_file (the returned
// path). Rows keep the batch's order and NULLs stay NULL. A zero-row batch still writes a file
// with the full schema.
//
// The file is encoded into a ".tmp-*" file in the target directory, synced and closed, then
// renamed over the target, so a reader sees the old file or the new one and never a mix. Any
// error, including ctx being done just before the rename, removes the temp file and leaves an
// existing target untouched. Write never reads or appends to the old file.
func (wr *Writer) Write(ctx context.Context, zone Zone, dataset string, w window.Window, b record.Batch, extractedAt time.Time) (Result, error) {
	if err := b.Validate(); err != nil {
		return Result{}, fmt.Errorf("bronze: invalid batch: %w", err)
	}
	for _, c := range b.Columns {
		if p, ok := provenanceCollision(c.Name); ok {
			return Result{}, fmt.Errorf("bronze: batch column %q collides with provenance column %q, which the Writer adds", c.Name, p)
		}
	}
	rel, err := Path(zone, dataset, w)
	if err != nil {
		return Result{}, err
	}
	if wr.Root == "" {
		return Result{}, fmt.Errorf("bronze: Writer.Root is empty")
	}

	// Path admits only [a-z0-9_]+ dataset segments, so target always stays under Root.
	target := filepath.Join(wr.Root, filepath.FromSlash(rel))
	prov := provenance{
		extractedAt: extractedAt.UnixMicro(),
		windowStart: w.Start.UnixMicro(),
		windowEnd:   w.End.UnixMicro(),
		sourceFile:  []byte(rel),
	}
	size, err := replaceFile(ctx, target, func(out io.Writer) error {
		return encode(out, b, prov)
	})
	if err != nil {
		return Result{}, fmt.Errorf("bronze: write %s: %w", rel, err)
	}
	return Result{Path: rel, Rows: len(b.Rows), Bytes: size}, nil
}

// replaceFile writes a new file through encodeTo into a temp file beside target, then renames it
// over target. On any failure (or panic) the temp file is removed and target is not touched.
func replaceFile(ctx context.Context, target string, encodeTo func(io.Writer) error) (int64, error) {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return 0, err
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = tmp.Close() // may already be closed; the file is being discarded either way
			_ = os.Remove(tmp.Name())
		}
	}()

	if err := tmp.Chmod(fileMode); err != nil {
		return 0, err
	}
	if err := encodeTo(tmp); err != nil {
		return 0, fmt.Errorf("encode: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return 0, err
	}
	info, err := tmp.Stat()
	if err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("cancelled before rename: %w", err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return 0, err
	}
	renamed = true
	return info.Size(), nil
}

// provenance holds the four provenance values, already in their on-disk form. They are the same
// for every row of a file.
type provenance struct {
	extractedAt int64 // µs since the Unix epoch, UTC
	windowStart int64 // µs since the Unix epoch, UTC
	windowEnd   int64 // µs since the Unix epoch, UTC
	sourceFile  []byte
}

// encode writes b plus the provenance columns as one Parquet file to out.
func encode(out io.Writer, b record.Batch, prov provenance) error {
	if forceEncodeErr != nil {
		return forceEncodeErr
	}
	schema := schemaOf(b.Columns)
	pw := parquet.NewWriter(out,
		schema,
		parquet.Compression(codec),
		parquet.CreatedBy(createdByApp, createdByVersion, createdByBuild),
		// Version-1 data pages and PLAIN values: the most widely readable layout.
		parquet.DataPageVersion(1),
		parquet.DefaultEncoding(&parquet.Plain),
		parquet.DataPageStatistics(true),
		parquet.PageBufferSize(parquet.DefaultPageBufferSize),
		parquet.WriteBufferSize(parquet.DefaultWriteBufferSize),
		parquet.MaxRowsPerRowGroup(parquet.DefaultMaxRowsPerRowGroup), // one row group per file
	)

	n := len(b.Columns)
	row := make(parquet.Row, n+4)
	rows := []parquet.Row{row}
	for r, vals := range b.Rows {
		for i, v := range vals {
			pv, err := valueOf(b.Columns[i].Kind, v)
			if err != nil {
				return fmt.Errorf("row %d, column %q: %w", r, b.Columns[i].Name, err)
			}
			definitionLevel := 1 // OPTIONAL column, value present
			if v == nil {
				definitionLevel = 0
			}
			row[i] = pv.Level(0, definitionLevel, i)
		}
		row[n] = parquet.Int64Value(prov.extractedAt).Level(0, 0, n)
		row[n+1] = parquet.Int64Value(prov.windowStart).Level(0, 0, n+1)
		row[n+2] = parquet.Int64Value(prov.windowEnd).Level(0, 0, n+2)
		row[n+3] = parquet.ByteArrayValue(prov.sourceFile).Level(0, 0, n+3)
		if _, err := pw.WriteRows(rows); err != nil {
			return fmt.Errorf("row %d: %w", r, err)
		}
	}
	return pw.Close()
}

// valueOf converts one batch value to its Parquet value; nil becomes NULL.
func valueOf(kind record.Kind, v any) (parquet.Value, error) {
	if v == nil {
		return parquet.NullValue(), nil
	}
	var ok bool
	var pv parquet.Value
	switch kind {
	case record.String:
		var s string
		if s, ok = v.(string); ok {
			pv = parquet.ByteArrayValue([]byte(s))
		}
	case record.Int64:
		var i int64
		if i, ok = v.(int64); ok {
			pv = parquet.Int64Value(i)
		}
	case record.Float64:
		var f float64
		if f, ok = v.(float64); ok {
			pv = parquet.DoubleValue(f)
		}
	case record.Bool:
		var t bool
		if t, ok = v.(bool); ok {
			pv = parquet.BooleanValue(t)
		}
	case record.Timestamp:
		var t time.Time
		if t, ok = v.(time.Time); ok {
			pv = parquet.Int64Value(wallClockMicros(t))
		}
	default:
		return parquet.Value{}, fmt.Errorf("unknown kind %v", kind)
	}
	if !ok {
		return parquet.Value{}, fmt.Errorf("value %v (%T) does not match kind %v", v, v, kind)
	}
	return pv, nil
}

// wallClockMicros encodes a naive timestamp: its wall-clock fields, unchanged, counted in µs from
// 1970-01-01 00:00:00 as a TIMESTAMP(isAdjustedToUTC=false) value is defined. The Location of t
// plays no part, so no time-zone conversion can creep in.
func wallClockMicros(t time.Time) int64 {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC).UnixMicro()
}

// Leaf types. Physical INT64 for every timestamp; the logical type carries the semantics.
var (
	// utcTimestamp is TIMESTAMP(MICROS, isAdjustedToUTC=true): an instant. parquet-go also sets
	// the legacy TIMESTAMP_MICROS converted type, which means exactly that.
	utcTimestamp = parquet.TimestampAdjusted(parquet.Microsecond, true)
	// naiveTimestamp is TIMESTAMP(MICROS, isAdjustedToUTC=false): a wall-clock reading with no
	// zone. parquet-go would also set the legacy TIMESTAMP_MICROS converted type, which means
	// UTC-adjusted and would contradict the logical type for a reader that only knows converted
	// types; naiveTimestampType drops it.
	naiveTimestamp = parquet.Leaf(naiveTimestampType{parquet.TimestampAdjusted(parquet.Microsecond, false).Type()})
)

// naiveTimestampType is parquet-go's TIMESTAMP(MICROS, isAdjustedToUTC=false) type without a
// converted type, the form the Parquet format specifies for local (naive) timestamps.
type naiveTimestampType struct{ parquet.Type }

// ConvertedType reports no legacy converted type; see naiveTimestamp.
func (naiveTimestampType) ConvertedType() *deprecated.ConvertedType { return nil }

// leafOf returns the Parquet leaf node for a record kind.
func leafOf(kind record.Kind) parquet.Node {
	switch kind {
	case record.String:
		return parquet.String()
	case record.Int64:
		return parquet.Leaf(parquet.Int64Type)
	case record.Float64:
		return parquet.Leaf(parquet.DoubleType)
	case record.Bool:
		return parquet.Leaf(parquet.BooleanType)
	case record.Timestamp:
		return naiveTimestamp
	default: // unreachable: Batch.Validate rejects unknown kinds
		panic(fmt.Sprintf("bronze: no Parquet type for kind %v", kind))
	}
}

// schemaOf returns the file schema for a batch: its columns in order, all OPTIONAL, then the four
// REQUIRED provenance columns.
func schemaOf(cols []record.Column) *parquet.Schema {
	names := make([]string, 0, len(cols)+4)
	group := make(parquet.Group, len(cols)+4)
	for _, c := range cols {
		names = append(names, c.Name)
		group[c.Name] = parquet.Optional(leafOf(c.Kind))
	}
	names = append(names, ColExtractedAt, ColWindowStart, ColWindowEnd, ColSourceFile)
	group[ColExtractedAt] = parquet.Required(utcTimestamp)
	group[ColWindowStart] = parquet.Required(utcTimestamp)
	group[ColWindowEnd] = parquet.Required(utcTimestamp)
	group[ColSourceFile] = parquet.Required(parquet.String())
	return parquet.NewSchema(schemaName, newOrderedGroup(group, names))
}

// orderedGroup is a parquet.Group whose fields keep a given order; parquet.Group itself sorts
// its fields by name. parquet-go uses the same device, unexported, for variant groups.
type orderedGroup struct {
	parquet.Group
	fields []parquet.Field
}

func newOrderedGroup(g parquet.Group, order []string) *orderedGroup {
	byName := make(map[string]parquet.Field, len(g))
	for _, f := range g.Fields() {
		byName[f.Name()] = f
	}
	fields := make([]parquet.Field, len(order))
	for i, name := range order {
		fields[i] = byName[name]
	}
	return &orderedGroup{Group: g, fields: fields}
}

// Fields returns the group's fields in the given order.
func (g *orderedGroup) Fields() []parquet.Field { return g.fields }
