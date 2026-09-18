package beads

// White-box tests that drive Fetch itself through an in-memory database/sql driver, so no Dolt
// server is needed. fetcher_test.go asserts on ReadOnlyTxOptions; these prove Fetch is wired to
// it (AC-14: BeginTx carries ReadOnly and the transaction ends in a rollback, never a commit) and
// that Fetch runs exactly BuildQuery's statement, types and decodes the driver's values, and turns
// Dolt's "table not found" into an error naming the table and window (AC-12).

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/window"
)

// fakeResult is what the fake server answers to every query: either an error or a result set.
type fakeResult struct {
	err     error
	columns []string
	types   []string // DatabaseTypeName per column
	rows    [][]driver.Value
}

// fakeLog records what Fetch asked the driver to do.
type fakeLog struct {
	txOpts    []driver.TxOptions
	queries   []string
	args      [][]any
	commits   int
	rollbacks int
}

type fakeConnector struct {
	log *fakeLog
	res fakeResult
}

func (c *fakeConnector) Connect(context.Context) (driver.Conn, error) { return &fakeConn{c}, nil }
func (c *fakeConnector) Driver() driver.Driver                        { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fake: use the connector")
}

type fakeConn struct{ c *fakeConnector }

func (fc *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("fake: no prepare") }
func (fc *fakeConn) Close() error                        { return nil }
func (fc *fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("fake: use BeginTx") }

func (fc *fakeConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	fc.c.log.txOpts = append(fc.c.log.txOpts, opts)
	return fakeTx{fc.c.log}, nil
}

func (fc *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	fc.c.log.queries = append(fc.c.log.queries, query)
	vals := make([]any, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	fc.c.log.args = append(fc.c.log.args, vals)
	if fc.c.res.err != nil {
		return nil, fc.c.res.err
	}
	return &fakeRows{res: fc.c.res}, nil
}

type fakeTx struct{ log *fakeLog }

func (t fakeTx) Commit() error   { t.log.commits++; return nil }
func (t fakeTx) Rollback() error { t.log.rollbacks++; return nil }

type fakeRows struct {
	res fakeResult
	i   int
}

func (r *fakeRows) Columns() []string                           { return r.res.columns }
func (r *fakeRows) Close() error                                { return nil }
func (r *fakeRows) ColumnTypeDatabaseTypeName(index int) string { return r.res.types[index] }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.res.rows) {
		return io.EOF
	}
	copy(dest, r.res.rows[r.i])
	r.i++
	return nil
}

// newFakeFetcher returns a Fetcher whose pool is the fake driver, with events bounds in Sydney.
func newFakeFetcher(t *testing.T, res fakeResult) (*Fetcher, *fakeLog) {
	t.Helper()
	sydney, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skipf("Australia/Sydney tzdata unavailable: %v", err)
	}
	log := &fakeLog{}
	db := sql.OpenDB(&fakeConnector{log: log, res: res})
	t.Cleanup(func() { db.Close() })
	return &Fetcher{db: db, loc: sydney}, log
}

// assertReadOnlyRolledBack checks AC-14 on the recorded driver calls: exactly one transaction,
// opened ReadOnly at the default isolation level, ended by a rollback and never committed.
func assertReadOnlyRolledBack(t *testing.T, log *fakeLog) {
	t.Helper()
	if len(log.txOpts) != 1 {
		t.Fatalf("BeginTx called %d times, want 1", len(log.txOpts))
	}
	if !log.txOpts[0].ReadOnly {
		t.Errorf("BeginTx ReadOnly = false, want true (AC-14)")
	}
	if log.txOpts[0].Isolation != driver.IsolationLevel(sql.LevelDefault) {
		t.Errorf("BeginTx Isolation = %v, want the driver default", log.txOpts[0].Isolation)
	}
	if log.commits != 0 {
		t.Errorf("transaction committed %d times, want 0", log.commits)
	}
	if log.rollbacks != 1 {
		t.Errorf("transaction rolled back %d times, want 1", log.rollbacks)
	}
}

func TestFetch_ReadOnlyTxRunsBuildQueryAndDecodes_AC14(t *testing.T) {
	res := fakeResult{
		columns: []string{"id", "created_at", "priority", "commit_order", "metadata", "note"},
		types:   []string{"CHAR", "DATETIME", "INT", "UNSIGNED BIGINT", "JSON", "TEXT"},
		rows: [][]driver.Value{
			{[]byte("e-1"), time.Date(2026, 9, 15, 13, 5, 0, 0, time.UTC), int64(2), uint64(7), []byte(`{"a":1}`), nil},
			{[]byte("e-2"), time.Date(2026, 9, 15, 13, 59, 59, 0, time.UTC), []byte("-3"), []byte("8"), []byte("{}"), []byte("x")},
		},
	}
	f, log := newFakeFetcher(t, res)
	w := window.Window{
		Start: time.Date(2026, 9, 15, 3, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 9, 15, 4, 0, 0, 0, time.UTC),
		Grain: window.Hour,
	}

	got, err := f.Fetch(context.Background(), "events", w)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	assertReadOnlyRolledBack(t, log)

	want, err := BuildQuery("events", w, f.loc)
	if err != nil {
		t.Fatalf("BuildQuery: %v", err)
	}
	if len(log.queries) != 1 || log.queries[0] != want.SQL {
		t.Errorf("queries = %q, want exactly [%q]", log.queries, want.SQL)
	}
	if len(log.args) != 1 || !reflect.DeepEqual(log.args[0], want.Args) {
		t.Errorf("args = %v, want %v (Sydney wall-time bounds)", log.args, want.Args)
	}

	wantBatch := record.Batch{
		Columns: []record.Column{
			{Name: "id", Kind: record.String},
			{Name: "created_at", Kind: record.Timestamp},
			{Name: "priority", Kind: record.Int64},
			{Name: "commit_order", Kind: record.Int64},
			{Name: "metadata", Kind: record.String},
			{Name: "note", Kind: record.String},
		},
		Rows: [][]any{
			{"e-1", time.Date(2026, 9, 15, 13, 5, 0, 0, time.UTC), int64(2), int64(7), `{"a":1}`, nil},
			{"e-2", time.Date(2026, 9, 15, 13, 59, 59, 0, time.UTC), int64(-3), int64(8), "{}", "x"},
		},
	}
	if !reflect.DeepEqual(got, wantBatch) {
		t.Errorf("batch =\n%#v\nwant\n%#v", got, wantBatch)
	}
}

func TestFetch_TableNotFoundNamesTableAndWindow_AC12(t *testing.T) {
	f, log := newFakeFetcher(t, fakeResult{err: &mysql.MySQLError{Number: 1146, Message: "table not found: issues"}})
	w := window.Window{
		Start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		Grain: window.Day,
	}

	got, err := f.Fetch(context.Background(), "issues", w)
	if err == nil {
		t.Fatalf("Fetch returned batch %+v and a nil error, want an error", got)
	}
	for _, part := range []string{"issues", "2026-08-01"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not contain %q", err.Error(), part)
		}
	}
	if got.Columns != nil || got.Rows != nil {
		t.Errorf("batch = %+v alongside the error, want the zero Batch", got)
	}
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1146 {
		t.Errorf("error %v does not wrap the driver's 1146 error", err)
	}
	assertReadOnlyRolledBack(t, log)
}

func TestFetch_UnknownColumnTypeErrors_AC13(t *testing.T) {
	res := fakeResult{
		columns: []string{"issue_id", "blob_col"},
		types:   []string{"VARCHAR", "BLOB"},
		rows:    [][]driver.Value{{[]byte("trk-1"), []byte{0x00}}},
	}
	f, log := newFakeFetcher(t, res)
	w := window.Window{
		Start: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
		Grain: window.Day,
	}

	if _, err := f.Fetch(context.Background(), "labels", w); err == nil {
		t.Fatal("Fetch with a BLOB column returned a nil error, want one naming the column and type")
	} else if !strings.Contains(err.Error(), "blob_col") || !strings.Contains(err.Error(), "BLOB") {
		t.Errorf("error %q does not name column blob_col and type BLOB", err.Error())
	}
	assertReadOnlyRolledBack(t, log)
}

func TestFetch_UndecodableValueErrors(t *testing.T) {
	res := fakeResult{
		columns: []string{"commit_hash", "commit_order"},
		types:   []string{"TEXT", "UNSIGNED BIGINT"},
		rows:    [][]driver.Value{{[]byte("abc"), uint64(1) << 63}},
	}
	f, log := newFakeFetcher(t, res)
	w := window.Window{
		Start: time.Date(2026, 9, 15, 3, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 9, 15, 4, 0, 0, 0, time.UTC),
		Grain: window.Hour,
	}

	if got, err := f.Fetch(context.Background(), "dolt_log", w); err == nil {
		t.Fatalf("Fetch with an unsigned value above int64 returned %+v, nil error; want an overflow error", got)
	} else if !strings.Contains(err.Error(), "commit_order") {
		t.Errorf("error %q does not name the column commit_order", err.Error())
	}
	assertReadOnlyRolledBack(t, log)
}
