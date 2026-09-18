# Makefile — every phase-1 runbook step in one place. `make help` (the default target) lists
# them; README.md's runbook walks through them in order. See docs/data-platform-spec.md for the
# architecture these steps implement.
#
# TEMPORAL_ADDRESS is exported for every target: in this shell "localhost" can resolve to ::1
# first, and gRPC to a Temporal dev server listening on 127.0.0.1 then hangs until it times out
# (spec F10). Override it only if the dev server really listens elsewhere.
export TEMPORAL_ADDRESS ?= 127.0.0.1:7233

# backfill / transform window defaults: the full recorded beads history, to today in UTC.
FROM ?= 2026-08-21
TO ?= $(shell date -u +%Y-%m-%d)
WINDOW ?= $(shell date -u +%Y-%m-%d)

.PHONY: help fmt-check vet test test-live build check \
	temporal-dev worker schedule trigger ingest-once backfill \
	dbt-pre dbt-post transform build-marts freshness live ui

help:
	@echo "data-platform — phase 1 runbook targets"
	@echo ""
	@echo "  fmt-check    fail if gofmt -l . prints anything"
	@echo "  vet          go vet ./..."
	@echo "  test         go test ./..."
	@echo "  test-live    BEADS_LIVE=1 go test ./... (needs the Dolt server)"
	@echo "  build        CGO_ENABLED=0 go build -o bin/ ./cmd/..."
	@echo "  check        fmt-check, vet, test-live, build, plus doc.go / file-writer / hygiene checks"
	@echo ""
	@echo "  temporal-dev temporal server start-dev --db-filename local/temporal.db (foreground)"
	@echo "  worker       go run ./cmd/worker -root . (foreground)"
	@echo "  schedule     go run ./cmd/worker -apply-schedule"
	@echo "  trigger      temporal schedule trigger --schedule-id materialise-beads"
	@echo ""
	@echo "  ingest-once  go run ./cmd/ingest --source beads --once"
	@echo "  backfill     go run ./cmd/ingest --source beads --from \$$FROM --to \$$TO"
	@echo "               FROM defaults to 2026-08-21, TO to today (UTC); override with"
	@echo "               make backfill FROM=... TO=..."
	@echo ""
	@echo "  dbt-pre      dbt build --select tag:pre_go (creates warehouse/marts first)"
	@echo "  transform    go run ./cmd/transform issue_mentions --window \$$WINDOW"
	@echo "               WINDOW defaults to today (UTC); override with make transform WINDOW=..."
	@echo "  dbt-post     dbt build --select tag:post_go (creates warehouse/marts first)"
	@echo "  build-marts  dbt-pre, transform, dbt-post, in order"
	@echo "  freshness    dbt source freshness"
	@echo ""
	@echo "  live         bash scripts/live-materialise.sh: dev server, worker, schedule apply,"
	@echo "               trigger and a full MaterialiseSource + BuildMarts run, end to end"
	@echo "  ui           duckdb -ui -init scripts/marts-views.sql (interactive: opens a browser,"
	@echo "               not run in verification)"

fmt-check:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "$$out"; exit 1; fi; \
	echo "fmt-check: OK"

vet:
	go vet ./...

test:
	go test ./...

test-live:
	BEADS_LIVE=1 go test ./...

build:
	CGO_ENABLED=0 go build -o bin/ ./cmd/...

# check runs the full quality gate: the four targets above, then the whole-repo checks behind
# AC-41 (every package has a doc.go naming its data-engineering concept), AC-42 (file-creating
# calls live only in the Writer and the spool) and AC-44 (repo hygiene: go.mod is tidy and pinned
# to Go 1.25.x, x <= 5, with no toolchain line).
check: fmt-check vet test-live build
	@echo "--- doc.go (AC-41) ---"
	@missing=0; \
	for d in $$(go list -f '{{.Dir}}' ./...); do \
		if ! grep -q '^// Data-engineering concept: ' "$$d/doc.go" 2>/dev/null; then \
			echo "MISSING $$d/doc.go"; missing=1; \
		fi; \
	done; \
	if [ "$$missing" -ne 0 ]; then exit 1; fi; \
	echo "doc.go: OK"
	@echo "--- file-writer (AC-42) ---"
	@out=$$(grep -rnE 'os\.(Create|CreateTemp|WriteFile|OpenFile|Rename)\(' --include='*.go' cmd internal | grep -v '_test.go' || true); \
	bad=$$(printf '%s\n' "$$out" | grep -v -E '^internal/bronze/|^internal/orchestrator/spool\.go:' | grep -v '^$$' || true); \
	if [ -n "$$bad" ]; then echo "$$bad"; exit 1; fi; \
	echo "file-writer: OK"
	@echo "--- hygiene (AC-44) ---"
	@diff_out=$$(GOTOOLCHAIN=local go mod tidy -diff 2>&1); rc=$$?; \
	if [ "$$rc" -ne 0 ]; then echo "$$diff_out"; echo "go mod tidy -diff reports changes"; exit 1; fi; \
	goline=$$(grep -E '^go ' go.mod); \
	case "$$goline" in \
		go\ 1.25|go\ 1.25.[0-5]) ;; \
		*) echo "bad go directive: $$goline"; exit 1 ;; \
	esac; \
	if grep -qE '^toolchain ' go.mod; then echo "unexpected toolchain line in go.mod"; exit 1; fi; \
	echo "hygiene: OK"

temporal-dev:
	temporal server start-dev --db-filename local/temporal.db

worker:
	go run ./cmd/worker -root .

schedule:
	go run ./cmd/worker -apply-schedule

trigger:
	temporal schedule trigger --schedule-id materialise-beads

ingest-once:
	go run ./cmd/ingest --source beads --once

backfill:
	go run ./cmd/ingest --source beads --from $(FROM) --to $(TO)

dbt-pre:
	mkdir -p warehouse/marts
	dbt build --project-dir dbt --profiles-dir dbt --select tag:pre_go

dbt-post:
	mkdir -p warehouse/marts
	dbt build --project-dir dbt --profiles-dir dbt --select tag:post_go

transform:
	go run ./cmd/transform issue_mentions --window $(WINDOW)

build-marts: dbt-pre transform dbt-post

freshness:
	dbt source freshness --project-dir dbt --profiles-dir dbt

live:
	bash scripts/live-materialise.sh

ui:
	duckdb -ui -init scripts/marts-views.sql
