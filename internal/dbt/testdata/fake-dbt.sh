#!/usr/bin/env bash
# fake-dbt.sh is a test double for the real `dbt` binary, used only by internal/dbt's tests
# (Runner.Build sets Bin to this script's path). Task 09's tests reuse this exact path, so its
# behaviour is a small, stable contract:
#
#   - It always records its full argv, one argument per line, to the file named by the
#     FAKE_DBT_ARGV_FILE env var.
#   - Unless FAKE_DBT_NO_WRITE=1 is set, it copies the fixture file named by FAKE_DBT_FIXTURE onto
#     <project-dir>/target/run_results.json, creating <project-dir>/target first. <project-dir> is
#     read from the --project-dir argument and resolved relative to the script's working
#     directory, exactly as the real dbt binary would resolve it.
#   - It exits with the code in FAKE_DBT_EXIT_CODE (default 0).
#
# It never touches dbt/, warehouse/ or anything outside the temp WorkDir a test points it at.
set -euo pipefail

argv_file="${FAKE_DBT_ARGV_FILE:?FAKE_DBT_ARGV_FILE must be set}"
: > "$argv_file"
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$argv_file"
done

project_dir=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--project-dir" ]; then
    project_dir="$arg"
  fi
  prev="$arg"
done

if [ -z "${FAKE_DBT_NO_WRITE:-}" ]; then
  fixture="${FAKE_DBT_FIXTURE:?FAKE_DBT_FIXTURE must be set when FAKE_DBT_NO_WRITE is unset}"
  : "${project_dir:?fake-dbt.sh: no --project-dir argument found in argv}"
  mkdir -p "$project_dir/target"
  cp "$fixture" "$project_dir/target/run_results.json"
fi

exit "${FAKE_DBT_EXIT_CODE:-0}"
