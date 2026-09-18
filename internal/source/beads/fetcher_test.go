package beads_test

// Non-DB behavioral tests for beads.Fetcher, anchored to AC-14 ("Read-only by construction") in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md: "Every fetch runs inside
// BeginTx(ctx, &sql.TxOptions{ReadOnly: true}) ... Check: grep in verification plus a unit test on
// the transaction options." The grep half of that check is the task's own verification command
// (it greps internal/source's non-test code for write-statement keywords); this file covers the
// other half, the unit test on the transaction options.
//
// API-SURFACE NOTE FOR THE IMPLEMENTER: as with schema.go's ColumnKind (see schema_test.go), the
// brief's exact-API block does not name the "function or variable you can assert on" it asks
// fetcher.go to expose for the read-only transaction options. This test fixes that gap by
// requiring one new exported function in package beads (fetcher.go):
//
//	func ReadOnlyTxOptions() *sql.TxOptions
//
// returning the exact options every Fetch call passes to (*sql.DB).BeginTx.
//
// Black-box (package beads_test).

import (
	"testing"

	"github.com/DMokong/data-platform/internal/source"
	"github.com/DMokong/data-platform/internal/source/beads"
)

// AC-14: the transaction options beads.Fetcher uses for every fetch request ReadOnly: true.
func TestReadOnlyTxOptions_AC14(t *testing.T) {
	got := beads.ReadOnlyTxOptions()
	if got == nil {
		t.Fatal("ReadOnlyTxOptions() = nil, want a *sql.TxOptions")
	}
	if !got.ReadOnly {
		t.Errorf("ReadOnlyTxOptions().ReadOnly = false, want true")
	}
}

// AC-14: ReadOnlyTxOptions is stable across calls (the same options every fetch uses), not a
// value that happens to be true only the first time it is called.
func TestReadOnlyTxOptions_StableAcrossCalls_AC14(t *testing.T) {
	first := beads.ReadOnlyTxOptions()
	second := beads.ReadOnlyTxOptions()
	if first == nil || second == nil {
		t.Fatal("ReadOnlyTxOptions() returned nil")
	}
	if first.ReadOnly != second.ReadOnly {
		t.Errorf("ReadOnlyTxOptions().ReadOnly varied between calls: %v vs %v", first.ReadOnly, second.ReadOnly)
	}
	if !second.ReadOnly {
		t.Error("second ReadOnlyTxOptions().ReadOnly = false, want true")
	}
}

// Required content: *beads.Fetcher must satisfy source.Fetcher (the interface
// internal/source declares and every source package's Fetcher is meant to implement), so
// orchestration code (task 04's Runner) can hold a source.Fetcher without importing beads
// directly. A compile failure here means beads.Fetcher's Fetch method signature has drifted from
// source.Fetcher's.
var _ source.Fetcher = (*beads.Fetcher)(nil)

// AC-09 / Required content: NewFetcher accepts a Config value (not a pointer, and not an env
// lookup of its own) and, on the happy path with a config shaped like a real one, returns a
// non-nil *Fetcher and a nil error without needing a live database (sql.Open never dials; the
// actual connection only happens once a query runs, which the live tests exercise). Close on a
// Fetcher that never made a live query must not itself require a live database either.
func TestNewFetcherAndClose_NoDialRequired(t *testing.T) {
	cfg := beads.Config{
		Host:     "127.0.0.1",
		Port:     49209,
		Database: "trk",
		User:     "root",
		Password: "",
		EventsTZ: "UTC",
	}
	f, err := beads.NewFetcher(cfg)
	if err != nil {
		t.Fatalf("NewFetcher(%+v) error: %v", cfg, err)
	}
	if f == nil {
		t.Fatal("NewFetcher returned a nil *Fetcher with a nil error")
	}
	if err := f.Close(); err != nil {
		t.Errorf("Close() on a Fetcher that made no query: %v", err)
	}
}

// AC-09: NewFetcher rejects a Config whose EventsTZ cannot be resolved by time.LoadLocation,
// since every events query depends on that location to convert its bounds (F1) and a Fetcher that
// silently fell back to UTC would produce wrong events windows without ever erroring.
func TestNewFetcher_UnknownEventsTZ(t *testing.T) {
	cfg := beads.Config{
		Host:     "127.0.0.1",
		Port:     49209,
		Database: "trk",
		User:     "root",
		EventsTZ: "Not/AZone",
	}
	if f, err := beads.NewFetcher(cfg); err == nil {
		if f != nil {
			f.Close()
		}
		t.Fatal("NewFetcher with an unresolvable EventsTZ returned a nil error")
	}
}
