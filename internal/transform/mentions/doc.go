// Package mentions implements the issue_mentions transform: it reads the dim_issues mart with
// parquet-go and extracts references to other issue ids out of six free-text fields (title,
// description, design, acceptance_criteria, notes, close_reason). Matching an id like "trk-abc"
// or a child id like "trk-bam.1" against arbitrary prose, correctly at word boundaries and
// without swallowing a trailing sentence period, is exactly the kind of pattern-over-strings work
// SQL string functions do badly and a small regular expression does well.
//
// Data-engineering concept: free-text parsing where SQL is the wrong tool.
package mentions
