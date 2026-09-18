package record_test

// Behavioral tests for internal/record, anchored to AC-02 ("Record contract") in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md and the exact-API "Required content"
// section of tasks/01-foundation/brief.md. Every test is commented with the AC it proves; this
// package only imports the exported surface of internal/record (black-box).

import (
	"testing"
	"time"

	"github.com/DMokong/data-platform/internal/record"
)

// AC-02: Kind.String() gives every kind a distinct, non-empty rendering (used in error
// messages and logs; the exact wording is not pinned by the spec, only that kinds are
// distinguishable).
func TestKindString_AC02(t *testing.T) {
	kinds := []record.Kind{record.String, record.Int64, record.Float64, record.Bool, record.Timestamp}
	seen := map[string]record.Kind{}
	for _, k := range kinds {
		s := k.String()
		if s == "" {
			t.Errorf("Kind(%d).String() is empty", int(k))
			continue
		}
		if other, ok := seen[s]; ok {
			t.Errorf("Kind %d and Kind %d both render as %q; String() must distinguish kinds", int(other), int(k), s)
		}
		seen[s] = k
	}
}

// AC-02: a well-formed batch — one column per kind, including a nil in every column and a
// UTC-located Timestamp value — passes Validate().
func TestBatchValidate_ValidBatch_AC02(t *testing.T) {
	b := record.Batch{
		Columns: []record.Column{
			{Name: "id", Kind: record.Int64},
			{Name: "title", Kind: record.String},
			{Name: "score", Kind: record.Float64},
			{Name: "is_open", Kind: record.Bool},
			{Name: "created_at", Kind: record.Timestamp},
		},
		Rows: [][]any{
			{int64(1), "hello", 3.5, true, time.Date(2026, 9, 15, 13, 0, 0, 0, time.UTC)},
			{nil, nil, nil, nil, nil}, // AC-02: nil is a valid value for every kind
		},
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate() on well-formed batch = %v, want nil", err)
	}
}

// AC-02: Validate rejects an empty column name.
func TestBatchValidate_EmptyColumnName_AC02(t *testing.T) {
	b := record.Batch{
		Columns: []record.Column{{Name: "", Kind: record.String}},
		Rows:    [][]any{{"x"}},
	}
	if err := b.Validate(); err == nil {
		t.Errorf("Validate() = nil, want error for empty column name")
	}
}

// AC-02: Validate rejects a duplicate column name.
func TestBatchValidate_DuplicateColumnName_AC02(t *testing.T) {
	b := record.Batch{
		Columns: []record.Column{
			{Name: "id", Kind: record.Int64},
			{Name: "id", Kind: record.String},
		},
		Rows: [][]any{{int64(1), "x"}},
	}
	if err := b.Validate(); err == nil {
		t.Errorf("Validate() = nil, want error for duplicate column name")
	}
}

// AC-02: Validate rejects an unknown kind.
func TestBatchValidate_UnknownKind_AC02(t *testing.T) {
	b := record.Batch{
		Columns: []record.Column{{Name: "mystery", Kind: record.Kind(99)}},
		Rows:    [][]any{{"x"}},
	}
	if err := b.Validate(); err == nil {
		t.Errorf("Validate() = nil, want error for unknown kind")
	}
}

// AC-02: Validate rejects a ragged row (length differs from len(Columns)).
func TestBatchValidate_RaggedRow_AC02(t *testing.T) {
	b := record.Batch{
		Columns: []record.Column{
			{Name: "id", Kind: record.Int64},
			{Name: "title", Kind: record.String},
		},
		Rows: [][]any{
			{int64(1), "ok"},
			{int64(2)}, // missing the second column's value
		},
	}
	if err := b.Validate(); err == nil {
		t.Errorf("Validate() = nil, want error for ragged row")
	}
}

// AC-02: Validate rejects a non-nil value whose Go type does not match its column's kind.
func TestBatchValidate_TypeMismatch_AC02(t *testing.T) {
	cases := []struct {
		name  string
		kind  record.Kind
		value any
	}{
		{"string column given int64", record.String, int64(1)},
		{"int64 column given string", record.Int64, "1"},
		{"int64 column given float64", record.Int64, 1.0},
		{"float64 column given int64", record.Float64, int64(1)},
		{"bool column given string", record.Bool, "true"},
		{"bool column given int64", record.Bool, int64(1)},
		{"timestamp column given string", record.Timestamp, "2026-09-15T13:00:00Z"},
		{"timestamp column given int64", record.Timestamp, int64(0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := record.Batch{
				Columns: []record.Column{{Name: "col", Kind: c.kind}},
				Rows:    [][]any{{c.value}},
			}
			if err := b.Validate(); err == nil {
				t.Errorf("Validate() = nil, want error for %s", c.name)
			}
		})
	}
}

// AC-02: Timestamp is a "naive wall-clock timestamp" — Validate rejects a time.Time value
// whose Location is not time.UTC, even though the Go type (time.Time) matches.
func TestBatchValidate_TimestampNonUTCLocation_AC02(t *testing.T) {
	sydney, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skipf("Australia/Sydney tzdata unavailable in this environment: %v", err)
	}
	b := record.Batch{
		Columns: []record.Column{{Name: "created_at", Kind: record.Timestamp}},
		Rows: [][]any{
			{time.Date(2026, 9, 15, 23, 0, 0, 0, sydney)},
		},
	}
	if err := b.Validate(); err == nil {
		t.Errorf("Validate() = nil, want error for non-UTC Timestamp value")
	}
}

// AC-02: a Timestamp value located in time.UTC is accepted (control case for the rejection
// above, proving the rejection is about Location and not about Timestamp columns generally).
func TestBatchValidate_TimestampUTCLocationAccepted_AC02(t *testing.T) {
	b := record.Batch{
		Columns: []record.Column{{Name: "created_at", Kind: record.Timestamp}},
		Rows: [][]any{
			{time.Date(2026, 9, 15, 23, 0, 0, 0, time.UTC)},
		},
	}
	if err := b.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil for UTC-located Timestamp value", err)
	}
}
