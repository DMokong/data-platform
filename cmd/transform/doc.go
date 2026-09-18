// Command transform runs one registered Go transform (internal/transform/catalog) for one day
// window, the step between the two dbt passes: `dbt build --select tag:pre_go` materialises the
// marts a transform reads, this command runs the transform and writes bronze/derived/<name>/,
// then `dbt build --select tag:post_go` picks up that derived source.
//
// Data-engineering concept: running one Go transform between the two dbt passes.
//
// Usage: transform <name> --window YYYY-MM-DD [--root DIR]. <name> is positional and comes first;
// --window names the day window to materialise; --root (default ".") sets the repo root, so the
// warehouse read root is <root>/warehouse and the bronze write root is <root>/bronze. An unknown
// <name> lists the registered transforms and exits 2.
package main
