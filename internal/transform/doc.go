// Package transform defines the contract a Go transform declares (inputs, one output, window
// grain, guarding tests) and Materialise, which runs a transform and writes its output through
// the shared bronze Writer. dbt and Go agree only on table names (the warehouse is the contract);
// this package is the Go half of that agreement, the piece a dbt source declaration and a
// catalog.All() listing both point at.
//
// Data-engineering concept: the transform contract (inputs, one output, window, tests) shared by
// dbt and Go.
package transform
