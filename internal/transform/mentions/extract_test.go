package mentions

// Table-driven tests for the pure text-extraction rule AC-29 describes in
// docs/fable-streams/2026-09-18-phase-1-build/spec.md: "From each dim_issues text field ... the
// transform extracts references to issue ids whose prefix occurs among dim_issues ids ...
// including child ids such as trk-bam.1. It emits one row per (issue_id, mentioned_issue_id,
// field) with a mention_count. Self-mentions and unknown prefixes are ignored." and
// tasks/07-go-transform/brief.md's own pattern spec: `\b(<prefix alternatives>)-[0-9a-z]+(\.[0-9]+)*\b`,
// case-sensitive, where a trailing sentence period is not part of the id.
//
// REQUIRED IMPLEMENTATION HOOK (does not exist yet; read before writing extract.go):
// every test below calls an unexported function that extract.go must declare:
//
//	// extractMentions scans text for references to any issue id whose prefix is in prefixes,
//	// matching \b(<alt1>|<alt2>|...)-[0-9a-z]+(\.[0-9]+)*\b case-sensitively (AC-29's pattern,
//	// including child ids such as "trk-bam.1"; a trailing sentence period is never part of a
//	// match, since \b only tests position and does not consume it), and returns a count of
//	// matches per mentioned issue id. selfID's own id is excluded (a self-mention), and any
//	// prefix outside prefixes never matches at all, because the alternation is built only from
//	// prefixes -- there is no separate "is this prefix known" filter to get wrong. A mentioned id
//	// with zero matches is never present as a key in the result.
//	func extractMentions(text, selfID string, prefixes []string) map[string]int64
//
// This is the pure, single-field half of the extraction the brief's file split names
// ("mentions.go, extract.go"): extract.go is free-text parsing with no I/O and no knowledge of
// which field it was called for or of any other issue's fields, which is exactly what makes it
// unit-testable without a Parquet fixture (unlike the multi-field, multi-issue behaviors --
// "the same id in two fields", schema and row order -- covered in mentions_test.go against
// Transform.Run). Until extract.go exists, `go test ./internal/transform/mentions/...` fails to
// build with "undefined: extractMentions" -- the expected Mode-A result for a brand-new package.

import "testing"

func TestExtractMentions(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		selfID   string
		prefixes []string
		want     map[string]int64
	}{
		{
			name:     "parenthesised mention", // AC-29: punctuation and word boundaries
			text:     "see (trk-abc) for context",
			selfID:   "trk-self",
			prefixes: []string{"trk"},
			want:     map[string]int64{"trk-abc": 1},
		},
		{
			name:     "comma after a mention",
			text:     "trk-abc, then trk-def",
			selfID:   "trk-self",
			prefixes: []string{"trk"},
			want:     map[string]int64{"trk-abc": 1, "trk-def": 1},
		},
		{
			name:     "trailing sentence period is not part of the id",
			text:     "this closes trk-abc.",
			selfID:   "trk-self",
			prefixes: []string{"trk"},
			want:     map[string]int64{"trk-abc": 1},
		},
		{
			name:     "child id with a dotted suffix",
			text:     "see trk-bam.1 for the parent story",
			selfID:   "trk-self",
			prefixes: []string{"trk"},
			want:     map[string]int64{"trk-bam.1": 1},
		},
		{
			name:     "child id at sentence end keeps the dotted suffix and drops only the sentence period",
			text:     "this closes trk-bam.1.",
			selfID:   "trk-self",
			prefixes: []string{"trk"},
			want:     map[string]int64{"trk-bam.1": 1},
		},
		{
			name:     "a hyphenated suffix run against the id is not part of it",
			text:     "see trk-7gx-health for detail",
			selfID:   "trk-self",
			prefixes: []string{"trk"},
			want:     map[string]int64{"trk-7gx": 1},
		},
		{
			name:     "duplicate mentions within one field are counted",
			text:     "trk-abc appears, and trk-abc appears again",
			selfID:   "trk-self",
			prefixes: []string{"trk"},
			want:     map[string]int64{"trk-abc": 2},
		},
		{
			name:     "self-mention is dropped",
			text:     "trk-self refers to itself and to trk-other",
			selfID:   "trk-self",
			prefixes: []string{"trk"},
			want:     map[string]int64{"trk-other": 1},
		},
		{
			name:     "unknown prefix is ignored",
			text:     "see claw-99 and trk-abc",
			selfID:   "trk-self",
			prefixes: []string{"trk"}, // "claw" never occurs among dim_issues ids in this case
			want:     map[string]int64{"trk-abc": 1},
		},
		{
			name:     "case-sensitive: an uppercase prefix does not match a lowercase one",
			text:     "see TRK-abc",
			selfID:   "trk-self",
			prefixes: []string{"trk"},
			want:     map[string]int64{},
		},
		{
			name:     "multiple known prefixes both match",
			text:     "relates to trk-1 and bam-2",
			selfID:   "trk-self",
			prefixes: []string{"trk", "bam"},
			want:     map[string]int64{"trk-1": 1, "bam-2": 1},
		},
		{
			name:     "empty text yields no mentions",
			text:     "",
			selfID:   "trk-self",
			prefixes: []string{"trk"},
			want:     map[string]int64{},
		},
		{
			name:     "text with no matching prefix at all yields no mentions",
			text:     "nothing to see here",
			selfID:   "trk-self",
			prefixes: []string{"trk"},
			want:     map[string]int64{},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractMentions(c.text, c.selfID, c.prefixes)
			if len(got) != len(c.want) {
				t.Fatalf("extractMentions(%q, selfID=%q, prefixes=%v) = %v, want %v", c.text, c.selfID, c.prefixes, got, c.want)
			}
			for id, wantCount := range c.want {
				gotCount, ok := got[id]
				if !ok || gotCount != wantCount {
					t.Errorf("extractMentions(%q, ...)[%q] = %v (present=%v), want %d", c.text, id, gotCount, ok, wantCount)
				}
			}
		})
	}
}
