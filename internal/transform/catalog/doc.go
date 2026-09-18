// Package catalog is the explicit registry of every Go transform: All lists them, sorted by
// name, and Lookup finds one by name. Registration is explicit -- a transform is added to the
// slice inside All, not via an init() side effect -- so the full set of registered transforms is
// always visible by reading this one file, and cmd/transform and task 09's orchestrator can list
// or resolve a transform without importing each transform package directly.
//
// Data-engineering concept: an explicit transform registry for orchestration.
package catalog
