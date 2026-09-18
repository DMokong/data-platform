package mentions_test

// Behavioral tests for the issue_mentions Transform, anchored to AC-29 ("one row per (issue_id,
// mentioned_issue_id, field) with a mention_count ... Self-mentions and unknown prefixes are
// ignored") and AC-30 ("reads warehouse/marts/dim_issues/ with parquet-go ... A re-run overwrites
// that file") in docs/fable-streams/2026-09-18-phase-1-build/spec.md, plus
// tasks/07-go-transform/brief.md's Required content for internal/transform/mentions:
//   - Input: every *.parquet under <WarehouseRoot>/marts/dim_issues/, columns issue_id, title,
//     description, design, acceptance_criteria, notes, close_reason (optional UTF-8 byte arrays;
//     nulls possible).
//   - Prefixes: everything before the first "-" across dim_issues.issue_id in the batch itself.
//   - Output columns issue_id/mentioned_issue_id/field/mention_count (String, String, String,
//     Int64), one row per (issue_id, mentioned_issue_id, field), self-mentions dropped, sorted by
//     issue_id, mentioned_issue_id, field.
//
// REQUIRED IMPLEMENTATION HOOK (does not exist yet; read before writing mentions.go):
// every test below constructs a value of an exported type this package must declare, implementing
// transform.Transform with no configuration needed (mirroring this repo's existing
// beads.Fetcher-implements-source.Fetcher convention in internal/source/beads/fetcher.go):
//
//	// Transform is the issue_mentions transform (AC-28/AC-29/AC-30). Its zero value is ready to
//	// use.
//	type Transform struct{}
//
//	func (Transform) Contract() transform.Contract { ... }
//	func (Transform) Run(ctx context.Context, env transform.Env, w window.Window) (record.Batch, error) { ... }
//
// This is a black-box test (package mentions_test): every assertion goes through Transform's
// exported Contract()/Run() methods and the record.Batch they return, driven by real dim_issues-
// shaped Parquet fixtures this file generates with parquet-go into t.TempDir() -- never any
// unexported mentions.go state. Until Transform (and transform.go, which this package also
// imports) exist, this package fails to build -- the expected Mode-A result for a brand-new
// package with no implementation yet.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/transform"
	"github.com/DMokong/data-platform/internal/transform/mentions"
	"github.com/DMokong/data-platform/internal/window"
)

// --- fixture helpers ---------------------------------------------------------------------------

// dimIssuesFixtureRow shapes one dim_issues row using only the seven columns the brief names
// (issue_id required, the six text fields optional -- "DuckDB writes them as optional UTF-8 byte
// arrays, so nulls are possible").
type dimIssuesFixtureRow struct {
	IssueID            string  `parquet:"issue_id"`
	Title              *string `parquet:"title,optional"`
	Description        *string `parquet:"description,optional"`
	Design             *string `parquet:"design,optional"`
	AcceptanceCriteria *string `parquet:"acceptance_criteria,optional"`
	Notes              *string `parquet:"notes,optional"`
	CloseReason        *string `parquet:"close_reason,optional"`
}

// dimIssuesFixtureRowWithExtras adds a couple of the real dim_issues mart's non-text columns
// (status, cycle_time_hours), so TestRun_ToleratesExtraDimIssuesColumns_AC30 can prove Run selects
// its seven columns by name out of a wider, heterogeneously-typed schema instead of assuming an
// exact column count/order.
type dimIssuesFixtureRowWithExtras struct {
	IssueID        string   `parquet:"issue_id"`
	Status         string   `parquet:"status"`
	CycleTimeHours *float64 `parquet:"cycle_time_hours,optional"`
	Title          *string  `parquet:"title,optional"`
	Description    *string  `parquet:"description,optional"`
	Design         *string  `parquet:"design,optional"`
	Notes          *string  `parquet:"notes,optional"`
	CloseReason    *string  `parquet:"close_reason,optional"`
}

func strp(s string) *string { return &s }

// dimIssuesDir returns the directory Run reads, under warehouse root root.
func dimIssuesDir(root string) string { return filepath.Join(root, "marts", "dim_issues") }

// writeParquetFixture writes rows of type T as one Parquet file at dir/filename, creating dir if
// needed.
func writeParquetFixture[T any](t *testing.T, dir, filename string, rows []T) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	f, err := os.Create(filepath.Join(dir, filename))
	if err != nil {
		t.Fatalf("Create(%s/%s): %v", dir, filename, err)
	}
	defer f.Close()
	w := parquet.NewGenericWriter[T](f)
	if _, err := w.Write(rows); err != nil {
		t.Fatalf("write fixture rows: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close fixture writer: %v", err)
	}
}

// dayWindow returns a fixed Day window; Run's dim_issues read is not windowed (dim_issues holds
// the latest snapshot only), so any valid Day window is equivalent for these tests.
func dayWindow(t *testing.T) window.Window {
	t.Helper()
	return window.Containing(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), window.Day)
}

func runMentions(t *testing.T, warehouseRoot string) record.Batch {
	t.Helper()
	tr := mentions.Transform{}
	batch, err := tr.Run(context.Background(), transform.Env{WarehouseRoot: warehouseRoot}, dayWindow(t))
	if err != nil {
		t.Fatalf("Transform.Run: %v", err)
	}
	return batch
}

// mentionRowT is a parsed, typed view of one output row, for easy comparison in test tables.
type mentionRowT struct {
	issueID     string
	mentionedID string
	field       string
	count       int64
}

// parseRows converts batch.Rows into []mentionRowT, assuming the exact column order
// TestRun_SchemaAndRowOrder_AC29_AC30 pins (issue_id, mentioned_issue_id, field, mention_count).
func parseRows(t *testing.T, batch record.Batch) []mentionRowT {
	t.Helper()
	rows := make([]mentionRowT, len(batch.Rows))
	for i, r := range batch.Rows {
		if len(r) != 4 {
			t.Fatalf("row %d has %d values, want 4 (issue_id, mentioned_issue_id, field, mention_count): %v", i, len(r), r)
		}
		issueID, ok1 := r[0].(string)
		mentionedID, ok2 := r[1].(string)
		field, ok3 := r[2].(string)
		count, ok4 := r[3].(int64)
		if !ok1 || !ok2 || !ok3 || !ok4 {
			t.Fatalf("row %d = %v has unexpected value types, want (string, string, string, int64)", i, r)
		}
		rows[i] = mentionRowT{issueID, mentionedID, field, count}
	}
	return rows
}

func onlyRow(t *testing.T, batch record.Batch) mentionRowT {
	t.Helper()
	rows := parseRows(t, batch)
	if len(rows) != 1 {
		t.Fatalf("batch has %d rows, want exactly 1: %+v", len(rows), rows)
	}
	return rows[0]
}

// --- tests ---------------------------------------------------------------------------------------

// AC-29 / AC-30: the output batch's schema is exactly issue_id/mentioned_issue_id/field/
// mention_count (String, String, String, Int64), and rows are sorted by issue_id, then
// mentioned_issue_id, then field -- regardless of file read order or the six fields' natural scan
// order. The fixture spans two *.parquet files (proving "every *.parquet under dim_issues/" is
// read), with the numerically/alphabetically later issue (trk-9) in the first file and the earlier
// one (trk-2) in the second, and with two fields on trk-9 whose alphabetical order (close_reason <
// title) is the *opposite* of the six fields' declared scan order (title, ..., close_reason) --
// so passing this test requires an explicit final sort, not an accidentally-already-sorted input.
func TestRun_SchemaAndRowOrder_AC29_AC30(t *testing.T) {
	root := t.TempDir()
	dir := dimIssuesDir(root)

	writeParquetFixture(t, dir, "part-0.parquet", []dimIssuesFixtureRow{
		{IssueID: "trk-9", Title: strp("relates to trk-8 and trk-3 and trk-1"), CloseReason: strp("fixed by trk-1")},
	})
	writeParquetFixture(t, dir, "part-1.parquet", []dimIssuesFixtureRow{
		{IssueID: "trk-2", Design: strp("depends on trk-1")},
	})

	batch := runMentions(t, root)

	wantCols := []record.Column{
		{Name: "issue_id", Kind: record.String},
		{Name: "mentioned_issue_id", Kind: record.String},
		{Name: "field", Kind: record.String},
		{Name: "mention_count", Kind: record.Int64},
	}
	if !reflect.DeepEqual(batch.Columns, wantCols) {
		t.Fatalf("batch.Columns = %+v, want %+v", batch.Columns, wantCols)
	}

	got := parseRows(t, batch)
	want := []mentionRowT{
		{"trk-2", "trk-1", "design", 1},
		{"trk-9", "trk-1", "close_reason", 1},
		{"trk-9", "trk-1", "title", 1},
		{"trk-9", "trk-3", "title", 1},
		{"trk-9", "trk-8", "title", 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v, want %+v (sorted by issue_id, mentioned_issue_id, field)", got, want)
	}
}

// AC-29: two mentions of the same id within one field collapse into one row with mention_count 2,
// not two separate rows.
func TestRun_DuplicateMentionsWithinOneFieldAreCounted_AC29(t *testing.T) {
	root := t.TempDir()
	writeParquetFixture(t, dimIssuesDir(root), "part-0.parquet", []dimIssuesFixtureRow{
		{IssueID: "trk-1", Title: strp("trk-2 shows up, and trk-2 shows up again")},
		{IssueID: "trk-2"},
	})
	row := onlyRow(t, runMentions(t, root))
	want := mentionRowT{"trk-1", "trk-2", "title", 2}
	if row != want {
		t.Errorf("row = %+v, want %+v", row, want)
	}
}

// AC-29: "the same id in two fields" produces two rows (one per field), not a single row merging
// the fields or their counts.
func TestRun_SameMentionInTwoFieldsProducesTwoRows_AC29(t *testing.T) {
	root := t.TempDir()
	writeParquetFixture(t, dimIssuesDir(root), "part-0.parquet", []dimIssuesFixtureRow{
		{IssueID: "trk-1", Title: strp("mentions trk-2"), Description: strp("also mentions trk-2")},
		{IssueID: "trk-2"},
	})
	got := parseRows(t, runMentions(t, root))
	want := []mentionRowT{
		{"trk-1", "trk-2", "description", 1},
		{"trk-1", "trk-2", "title", 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v, want %+v (one row per field, not merged)", got, want)
	}
}

// AC-29: a self-mention is dropped, while a genuine mention of another issue in the same field
// still survives -- proving the drop is selective, not a whole-issue exclusion.
func TestRun_SelfMentionDropped_AC29(t *testing.T) {
	root := t.TempDir()
	writeParquetFixture(t, dimIssuesDir(root), "part-0.parquet", []dimIssuesFixtureRow{
		{IssueID: "trk-1", Design: strp("trk-1 refers to itself, and to trk-2")},
		{IssueID: "trk-2"},
	})
	got := parseRows(t, runMentions(t, root))
	want := []mentionRowT{{"trk-1", "trk-2", "design", 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v, want %+v (self-mention trk-1->trk-1 dropped)", got, want)
	}
}

// AC-29: a mention whose prefix never occurs among the batch's own dim_issues ids is ignored (no
// "claw-*" issue exists here), while a mention of a real id in the same field still survives.
func TestRun_UnknownPrefixIgnored_AC29(t *testing.T) {
	root := t.TempDir()
	writeParquetFixture(t, dimIssuesDir(root), "part-0.parquet", []dimIssuesFixtureRow{
		{IssueID: "trk-1", Notes: strp("relates to claw-1 and trk-2")},
		{IssueID: "trk-2"},
	})
	got := parseRows(t, runMentions(t, root))
	want := []mentionRowT{{"trk-1", "trk-2", "notes", 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v, want %+v (claw-1 ignored: no claw-* issue exists)", got, want)
	}
}

// AC-29: "the set of issue-id prefixes is everything before the first '-' across dim_issues
// ids" -- a prefix other than "trk" (here "zzz") is recognized as soon as an issue with that
// prefix is present in the batch, proving prefixes are derived from the data, not hardcoded.
func TestRun_PrefixesAreDerivedFromActualIssueIDs_AC29(t *testing.T) {
	root := t.TempDir()
	writeParquetFixture(t, dimIssuesDir(root), "part-0.parquet", []dimIssuesFixtureRow{
		{IssueID: "trk-1", Title: strp("relates to zzz-9")},
		{IssueID: "zzz-9"},
	})
	got := parseRows(t, runMentions(t, root))
	want := []mentionRowT{{"trk-1", "zzz-9", "title", 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v, want %+v (zzz-9 recognized because zzz-9 itself is a dim_issues id)", got, want)
	}
}

// AC-29 ("null fields"): an issue with every text field NULL contributes no rows and does not
// make Run fail, while a normal issue's mention in the same batch is still extracted correctly.
func TestRun_NullFieldsDoNotCrashOrProduceRows_AC29(t *testing.T) {
	root := t.TempDir()
	writeParquetFixture(t, dimIssuesDir(root), "part-0.parquet", []dimIssuesFixtureRow{
		{IssueID: "trk-1"}, // every text field NULL
		{IssueID: "trk-2", CloseReason: strp("closed as duplicate of trk-1")},
	})
	batch := runMentions(t, root) // must not error
	got := parseRows(t, batch)
	want := []mentionRowT{{"trk-2", "trk-1", "close_reason", 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v, want %+v (an all-NULL issue contributes nothing, but does not break the run)", got, want)
	}
}

// AC-30: Run selects its seven columns by name out of a dim_issues file that also carries the real
// mart's other columns (status, cycle_time_hours -- of different Parquet types), rather than
// assuming an exact column set or order.
func TestRun_ToleratesExtraDimIssuesColumns_AC30(t *testing.T) {
	root := t.TempDir()
	hours := 12.5
	writeParquetFixture(t, dimIssuesDir(root), "part-0.parquet", []dimIssuesFixtureRowWithExtras{
		{IssueID: "trk-1", Status: "open", CycleTimeHours: &hours, Title: strp("relates to trk-2")},
		{IssueID: "trk-2", Status: "closed"},
	})
	got := parseRows(t, runMentions(t, root))
	want := []mentionRowT{{"trk-1", "trk-2", "title", 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v, want %+v (extra non-text columns present on the real mart must not break Run)", got, want)
	}
}
