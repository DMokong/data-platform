// Package source defines the extraction contract every Fetcher implements. A Source declares,
// as plain inspectable data, which tables it serves and how each is extracted: the window grain
// (the unit of one bronze file), the lookback (how many trailing windows each run re-extracts to
// absorb late or corrected data), the shape (an event log selected by a time column, or a
// time-travel snapshot of the whole table) and the cadence at which the Runner fires. A Fetcher
// turns one declared table and one window into a record.Batch; it never writes files, never picks
// windows and never cleans data.
//
// Data-engineering concept: declared extraction contracts (grain, cadence, lookback).
package source
