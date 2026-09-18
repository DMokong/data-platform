package dbt

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Timing is one phase of a dbt node's execution, taken verbatim from run_results.json's
// per-node "timing" array (typically "compile" and "execute").
type Timing struct {
	Name        string
	StartedAt   time.Time
	CompletedAt time.Time
}

// NodeResult is one entry of run_results.json's "results" array: the outcome of running or
// testing one dbt node (model, test, seed, snapshot, ...).
type NodeResult struct {
	UniqueID string // e.g. "model.data_platform.dim_issues"

	// ResourceType is the first dot-segment of UniqueID: model, test, seed, snapshot, ...
	ResourceType string
	// Name is the last dot-segment of UniqueID.
	Name string

	Status        string // success | error | fail | pass | warn | skipped | runtime error
	ExecutionTime float64
	Timing        []Timing
	Message       string
	Failures      *int
}

// RunResults is the parsed shape of dbt's run_results.json artefact: the run's metadata plus
// one NodeResult per node dbt attempted.
type RunResults struct {
	InvocationID string
	GeneratedAt  time.Time
	ElapsedTime  float64
	Results      []NodeResult
}

// rawRunResults mirrors only the fields of run_results.json (schema v6, dbt 1.12.x) that
// ParseRunResults needs; everything else in the artefact (args, compiled_code, adapter_response,
// thread_id, ...) is dropped on the floor.
type rawRunResults struct {
	Metadata struct {
		DbtSchemaVersion string    `json:"dbt_schema_version"`
		DbtVersion       string    `json:"dbt_version"`
		GeneratedAt      time.Time `json:"generated_at"`
		InvocationID     string    `json:"invocation_id"`
	} `json:"metadata"`
	Results     []rawNodeResult `json:"results"`
	ElapsedTime float64         `json:"elapsed_time"`
}

type rawNodeResult struct {
	Status        string      `json:"status"`
	Timing        []rawTiming `json:"timing"`
	ExecutionTime float64     `json:"execution_time"`
	Message       string      `json:"message"`
	Failures      *int        `json:"failures"`
	UniqueID      string      `json:"unique_id"`
}

type rawTiming struct {
	Name        string    `json:"name"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

// ParseRunResults decodes a dbt run_results.json artefact from r.
func ParseRunResults(r io.Reader) (RunResults, error) {
	var raw rawRunResults
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return RunResults{}, fmt.Errorf("dbt: decode run_results.json: %w", err)
	}

	results := make([]NodeResult, len(raw.Results))
	for i, n := range raw.Results {
		resourceType, name := splitUniqueID(n.UniqueID)

		timing := make([]Timing, len(n.Timing))
		for j, t := range n.Timing {
			timing[j] = Timing{Name: t.Name, StartedAt: t.StartedAt, CompletedAt: t.CompletedAt}
		}

		results[i] = NodeResult{
			UniqueID:      n.UniqueID,
			ResourceType:  resourceType,
			Name:          name,
			Status:        n.Status,
			ExecutionTime: n.ExecutionTime,
			Timing:        timing,
			Message:       n.Message,
			Failures:      n.Failures,
		}
	}

	return RunResults{
		InvocationID: raw.Metadata.InvocationID,
		GeneratedAt:  raw.Metadata.GeneratedAt,
		ElapsedTime:  raw.ElapsedTime,
		Results:      results,
	}, nil
}

// splitUniqueID derives ResourceType (the first dot-segment) and Name (the last dot-segment)
// from a dbt unique_id, e.g. "model.data_platform.dim_issues" -> ("model", "dim_issues") or a
// hashed generic test id "test.data_platform.unique_x_y.67aa220841" -> ("test", "67aa220841").
func splitUniqueID(uniqueID string) (resourceType, name string) {
	parts := strings.Split(uniqueID, ".")
	if len(parts) == 0 {
		return "", ""
	}
	return parts[0], parts[len(parts)-1]
}

// Failed returns the nodes whose Status marks them as not having succeeded: error, fail or
// runtime error.
func (rr RunResults) Failed() []NodeResult {
	var out []NodeResult
	for _, n := range rr.Results {
		switch n.Status {
		case "error", "fail", "runtime error":
			out = append(out, n)
		}
	}
	return out
}
