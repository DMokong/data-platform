package beads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/window"
)

// errTableNotFound is the MySQL error number Dolt returns for a table that does not exist, which
// is what AS OF reports for an instant before the table's first commit (spec F2).
const errTableNotFound = 1146

// Fetcher reads declared beads tables from the Dolt server. It implements source.Fetcher.
type Fetcher struct {
	db  *sql.DB
	cfg Config
	loc *time.Location // cfg.EventsTZ, resolved once
}

// NewFetcher resolves cfg.EventsTZ and prepares a connection pool. It does not dial: the first
// Fetch opens the first connection.
func NewFetcher(cfg Config) (*Fetcher, error) {
	loc, err := cfg.eventsLocation()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("beads: open %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Database, err)
	}
	return &Fetcher{db: db, cfg: cfg, loc: loc}, nil
}

// Close releases the connection pool.
func (f *Fetcher) Close() error {
	return f.db.Close()
}

// ReadOnlyTxOptions returns the transaction options every Fetch passes to BeginTx: a read-only
// transaction (AC-14), which Dolt enforces by failing any write inside it (spec F2). The
// isolation level is left at the driver default, so opening the transaction sets no session
// variable.
func ReadOnlyTxOptions() *sql.TxOptions {
	return &sql.TxOptions{ReadOnly: true}
}

// Fetch returns table's rows for window w exactly as the server served them, ordered by the
// table's unique key, with columns in table order. The query runs inside a read-only transaction
// that is always rolled back, never committed: there is nothing to commit, and a rollback cannot
// write even on a server configured to turn each SQL commit into a Dolt commit. A snapshot
// window ending before the table's first commit is an error naming the table and window, never
// an empty batch.
func (f *Fetcher) Fetch(ctx context.Context, table string, w window.Window) (record.Batch, error) {
	q, err := BuildQuery(table, w, f.loc)
	if err != nil {
		return record.Batch{}, err
	}
	tx, err := f.db.BeginTx(ctx, ReadOnlyTxOptions())
	if err != nil {
		return record.Batch{}, fmt.Errorf("beads: fetch %s window %s: begin read-only transaction: %w", table, w, err)
	}
	// Safety net for the error paths; on success the explicit Rollback below runs first and this
	// one returns sql.ErrTxDone, which is ignored.
	defer tx.Rollback()

	batch, err := query(ctx, tx, q)
	if err != nil {
		var me *mysql.MySQLError
		if errors.As(err, &me) && me.Number == errTableNotFound {
			return record.Batch{}, fmt.Errorf("beads: fetch %s window %s: table %s did not exist yet at %s UTC "+
				"(the window ends before the table's first commit): %w", table, w, table, w.End.Format(boundLayout), err)
		}
		return record.Batch{}, fmt.Errorf("beads: fetch %s window %s: %w", table, w, err)
	}
	if err := tx.Rollback(); err != nil {
		return record.Batch{}, fmt.Errorf("beads: fetch %s window %s: end read-only transaction: %w", table, w, err)
	}
	if err := batch.Validate(); err != nil {
		return record.Batch{}, fmt.Errorf("beads: fetch %s window %s: %w", table, w, err)
	}
	return batch, nil
}

// query runs q in tx and reads the whole result into a Batch, typing columns from the driver's
// column types.
func query(ctx context.Context, tx *sql.Tx, q Query) (record.Batch, error) {
	rows, err := tx.QueryContext(ctx, q.SQL, q.Args...)
	if err != nil {
		return record.Batch{}, err
	}
	defer rows.Close()

	types, err := rows.ColumnTypes()
	if err != nil {
		return record.Batch{}, fmt.Errorf("read column types: %w", err)
	}
	cols := make([]record.Column, len(types))
	for i, ct := range types {
		kind, err := ColumnKind(ct.Name(), ct.DatabaseTypeName())
		if err != nil {
			return record.Batch{}, err
		}
		cols[i] = record.Column{Name: ct.Name(), Kind: kind}
	}

	out := [][]any{}
	scanned := make([]any, len(cols))
	dest := make([]any, len(cols))
	for i := range scanned {
		dest[i] = &scanned[i]
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return record.Batch{}, fmt.Errorf("scan row %d: %w", len(out), err)
		}
		row := make([]any, len(cols))
		for i, v := range scanned {
			decoded, err := decodeValue(cols[i].Kind, v)
			if err != nil {
				return record.Batch{}, fmt.Errorf("row %d, column %q (%s): %w",
					len(out), cols[i].Name, types[i].DatabaseTypeName(), err)
			}
			row[i] = decoded
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return record.Batch{}, err
	}
	if err := rows.Close(); err != nil {
		return record.Batch{}, err
	}
	return record.Batch{Columns: cols, Rows: out}, nil
}
