package mentions

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parquet-go/parquet-go"

	"github.com/DMokong/data-platform/internal/record"
	"github.com/DMokong/data-platform/internal/transform"
	"github.com/DMokong/data-platform/internal/window"
)

// textFields are the six dim_issues text columns scanned for mentions, and also the exact set of
// "field" values the output batch can carry.
var textFields = []string{"title", "description", "design", "acceptance_criteria", "notes", "close_reason"}

// dimIssuesRow is the shape Run reads out of dim_issues Parquet: issue_id required, the six text
// fields optional (DuckDB writes them as optional UTF-8 byte arrays, so nulls are possible).
// Reading into this narrower struct projects exactly these seven columns out of whatever else the
// real mart's schema carries (parquet-go's GenericReader maps by field name; extra source columns
// and columns this struct lacks are simply not read).
type dimIssuesRow struct {
	IssueID            string  `parquet:"issue_id"`
	Title              *string `parquet:"title,optional"`
	Description        *string `parquet:"description,optional"`
	Design             *string `parquet:"design,optional"`
	AcceptanceCriteria *string `parquet:"acceptance_criteria,optional"`
	Notes              *string `parquet:"notes,optional"`
	CloseReason        *string `parquet:"close_reason,optional"`
}

// fieldText returns r's six scanned text fields, keyed by the textFields name, with a NULL
// column read as "".
func (r dimIssuesRow) fieldText() map[string]string {
	return map[string]string{
		"title":               strVal(r.Title),
		"description":         strVal(r.Description),
		"design":              strVal(r.Design),
		"acceptance_criteria": strVal(r.AcceptanceCriteria),
		"notes":               strVal(r.Notes),
		"close_reason":        strVal(r.CloseReason),
	}
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Transform is the issue_mentions transform (AC-28/AC-29/AC-30). Its zero value is ready to use.
type Transform struct{}

// Contract declares issue_mentions: reads dim_issues, writes issue_mentions, grain Day, guarded
// by the derived source's freshness check and the mart_issue_mention_drift post_go mart.
func (Transform) Contract() transform.Contract {
	return transform.Contract{
		Name:   "issue_mentions",
		Inputs: []string{"dim_issues"},
		Output: "issue_mentions",
		Grain:  window.Day,
		Tests:  []string{"source_freshness:derived.issue_mentions", "mart_issue_mention_drift"},
	}
}

// Run reads every *.parquet file under <env.WarehouseRoot>/marts/dim_issues/ with parquet-go,
// derives the set of known issue-id prefixes from dim_issues.issue_id itself (everything before
// the first "-"), and extracts mentions from each of the six text fields of every issue. The
// window w is unused: dim_issues holds only the latest snapshot, so there is nothing to window
// over; Run reads it whole regardless of w.
func (Transform) Run(_ context.Context, env transform.Env, _ window.Window) (record.Batch, error) {
	dir := filepath.Join(env.WarehouseRoot, "marts", "dim_issues")
	rows, err := readDimIssues(dir)
	if err != nil {
		return record.Batch{}, fmt.Errorf("mentions: read %s: %w", dir, err)
	}

	prefixes := prefixesOf(rows)

	type key struct{ issueID, mentionedID, field string }
	counts := make(map[key]int64)
	for _, r := range rows {
		texts := r.fieldText()
		for _, field := range textFields {
			for mentionedID, c := range extractMentions(texts[field], r.IssueID, prefixes) {
				counts[key{r.IssueID, mentionedID, field}] += c
			}
		}
	}

	type outRow struct {
		issueID, mentionedID, field string
		count                       int64
	}
	out := make([]outRow, 0, len(counts))
	for k, c := range counts {
		out = append(out, outRow{k.issueID, k.mentionedID, k.field, c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].issueID != out[j].issueID {
			return out[i].issueID < out[j].issueID
		}
		if out[i].mentionedID != out[j].mentionedID {
			return out[i].mentionedID < out[j].mentionedID
		}
		return out[i].field < out[j].field
	})

	batch := record.Batch{
		Columns: []record.Column{
			{Name: "issue_id", Kind: record.String},
			{Name: "mentioned_issue_id", Kind: record.String},
			{Name: "field", Kind: record.String},
			{Name: "mention_count", Kind: record.Int64},
		},
		Rows: make([][]any, len(out)),
	}
	for i, r := range out {
		batch.Rows[i] = []any{r.issueID, r.mentionedID, r.field, r.count}
	}
	return batch, nil
}

// prefixesOf returns the set of issue-id prefixes -- everything before the first "-" -- across
// rows' issue ids, sorted, so the alternation built from it (and therefore Run's output) does not
// depend on file or map iteration order.
func prefixesOf(rows []dimIssuesRow) []string {
	seen := make(map[string]bool)
	var prefixes []string
	for _, r := range rows {
		p := prefixOf(r.IssueID)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	return prefixes
}

// prefixOf returns everything before the first "-" in issueID, or "" if issueID has none.
func prefixOf(issueID string) string {
	i := strings.IndexByte(issueID, '-')
	if i < 0 {
		return ""
	}
	return issueID[:i]
}

// readDimIssues reads every *.parquet file directly under dir, in name order, and concatenates
// their rows.
func readDimIssues(dir string) ([]dimIssuesRow, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".parquet" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var rows []dimIssuesRow
	for _, name := range names {
		fileRows, err := readDimIssuesFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		rows = append(rows, fileRows...)
	}
	return rows, nil
}

// readDimIssuesFile reads one dim_issues Parquet file's rows, projected onto dimIssuesRow.
func readDimIssuesFile(path string) ([]dimIssuesRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := parquet.NewGenericReader[dimIssuesRow](f)
	defer r.Close()

	var rows []dimIssuesRow
	buf := make([]dimIssuesRow, 256)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			rows = append(rows, buf[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
	}
	return rows, nil
}
