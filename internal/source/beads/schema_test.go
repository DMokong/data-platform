package beads_test

// Behavioral tests for beads's driver-column-type -> record.Kind mapping, anchored to AC-13
// ("Deterministic, untransformed batches" -- "Columns are the table's SELECT * columns in table
// order, with kinds derived from the driver's column types; an unknown type is an error, TINYINT
// stays Int64 and JSON stays String") in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md, and the brief's "Columns." / "Observed
// driver behaviour" bullets under "Fetch rules.".
//
// API-SURFACE NOTE FOR THE IMPLEMENTER: the brief's "Required content" exact-API block does not
// pin a signature for this mapping (it only says the logic lives in schema.go). The brief's own
// "Unit tests (no DB)" section requires "the type mapping, including the unknown-type error" to
// be independently unit-testable, and the "Columns." bullet says the unknown-type error must name
// "the column and type". This test file therefore fixes that gap in the exact-API contract by
// requiring one new exported function in package beads (schema.go):
//
//	func ColumnKind(columnName, databaseTypeName string) (record.Kind, error)
//
// -- pure, no DB access, taking the column's name (for the error message) and
// sql.ColumnType.DatabaseTypeName() (for the mapping) and returning the record.Kind Fetch should
// use for that column, or an error naming both columnName and databaseTypeName. If the
// implementer prefers a different exported name, that is a spec/plan mismatch worth flagging back
// (see test-author's report), not something to silently reconcile by rewriting this file.
//
// Black-box (package beads_test).

import (
	"strings"
	"testing"

	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/source/beads"
)

// AC-13: CHAR / VARCHAR / TEXT variants / JSON / ENUM / SET / DECIMAL all map to record.String.
// JSON staying String (rather than being parsed) is explicitly called out: "ingestion never
// transforms".
func TestColumnKind_String_AC13(t *testing.T) {
	for _, dbType := range []string{
		"CHAR", "VARCHAR", "TEXT", "TINYTEXT", "MEDIUMTEXT", "LONGTEXT",
		"JSON", "ENUM", "SET", "DECIMAL",
	} {
		t.Run(dbType, func(t *testing.T) {
			got, err := beads.ColumnKind("col", dbType)
			if err != nil {
				t.Fatalf("ColumnKind(%q) error: %v", dbType, err)
			}
			if got != record.String {
				t.Errorf("ColumnKind(%q) = %v, want %v", dbType, got, record.String)
			}
		})
	}
}

// AC-13 / brief "Observed driver behaviour": TINYINT / SMALLINT / MEDIUMINT / INT / BIGINT map to
// record.Int64, signed or unsigned -- including the driver's observed "UNSIGNED ..." prefix form
// (e.g. "UNSIGNED BIGINT" for dolt_log.commit_order). TINYINT explicitly "stays Int64" (not Bool),
// even though tinyint(1) is sometimes treated as boolean elsewhere.
func TestColumnKind_Int64_AC13(t *testing.T) {
	for _, dbType := range []string{
		"TINYINT", "SMALLINT", "MEDIUMINT", "INT", "BIGINT",
		"UNSIGNED TINYINT", "UNSIGNED SMALLINT", "UNSIGNED MEDIUMINT", "UNSIGNED INT", "UNSIGNED BIGINT",
	} {
		t.Run(dbType, func(t *testing.T) {
			got, err := beads.ColumnKind("col", dbType)
			if err != nil {
				t.Fatalf("ColumnKind(%q) error: %v", dbType, err)
			}
			if got != record.Int64 {
				t.Errorf("ColumnKind(%q) = %v, want %v", dbType, got, record.Int64)
			}
		})
	}
}

// AC-13: FLOAT / DOUBLE map to record.Float64.
func TestColumnKind_Float64_AC13(t *testing.T) {
	for _, dbType := range []string{"FLOAT", "DOUBLE"} {
		t.Run(dbType, func(t *testing.T) {
			got, err := beads.ColumnKind("col", dbType)
			if err != nil {
				t.Fatalf("ColumnKind(%q) error: %v", dbType, err)
			}
			if got != record.Float64 {
				t.Errorf("ColumnKind(%q) = %v, want %v", dbType, got, record.Float64)
			}
		})
	}
}

// AC-13: DATETIME / TIMESTAMP map to record.Timestamp.
func TestColumnKind_Timestamp_AC13(t *testing.T) {
	for _, dbType := range []string{"DATETIME", "TIMESTAMP"} {
		t.Run(dbType, func(t *testing.T) {
			got, err := beads.ColumnKind("col", dbType)
			if err != nil {
				t.Fatalf("ColumnKind(%q) error: %v", dbType, err)
			}
			if got != record.Timestamp {
				t.Errorf("ColumnKind(%q) = %v, want %v", dbType, got, record.Timestamp)
			}
		})
	}
}

// AC-13: "anything else -> an error naming the column and type." A driver type this package has
// no mapping for (e.g. BLOB, GEOMETRY, BIT) must fail loudly, and the error text must let a
// developer find both which column and which database type caused it, without needing to attach
// a debugger.
func TestColumnKind_UnknownType_AC13(t *testing.T) {
	for _, dbType := range []string{"BLOB", "GEOMETRY", "BIT", "YEAR", "TIME"} {
		t.Run(dbType, func(t *testing.T) {
			_, err := beads.ColumnKind("mystery_column", dbType)
			if err == nil {
				t.Fatalf("ColumnKind(%q) returned a nil error, want a rejection of the unmapped type", dbType)
			}
			if !strings.Contains(err.Error(), "mystery_column") {
				t.Errorf("error %q does not name the column %q", err.Error(), "mystery_column")
			}
			if !strings.Contains(err.Error(), dbType) {
				t.Errorf("error %q does not name the type %q", err.Error(), dbType)
			}
		})
	}
}
