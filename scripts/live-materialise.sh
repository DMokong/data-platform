#!/usr/bin/env bash
# live-materialise.sh: live proof of Temporal ingestion, the two-pass mart build and the worker's
# telemetry (AC-35, AC-36, AC-37, AC-40).
#
# Starts (or reuses) a Temporal dev server on 127.0.0.1:7233, builds and starts the worker with
# PLATFORM_OTEL_EXPORTER=stdout, upserts schedule materialise-beads, triggers it, and waits for
# the MaterialiseSource run it starts to complete. Then it checks with DuckDB that the bronze
# issues files were re-extracted after the trigger, that the run's BuildMarts child completed and
# left mart and derived Parquet newer than the trigger, and, after stopping the worker so its
# telemetry is flushed, that the worker's stdout export holds the expected spans. Everything it
# started is stopped on exit. The last line is "LIVE-MATERIALISE: PASS" (exit 0) or
# "LIVE-MATERIALISE: FAIL <reason>" (exit 1).
#
# Needs: go, temporal (CLI 1.9.x), duckdb, dbt (or PLATFORM_DBT_BIN) and the beads Dolt server
# (BEADS_HOST / BEADS_PORT, default 127.0.0.1:49209). Run it from anywhere; it works from the repo
# root. The whole run stays under 9 minutes. Each step is one function.
set -euo pipefail
set -o errtrace # the ERR trap below also fires inside functions

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# 127.0.0.1, never localhost: localhost can resolve to ::1 first, and gRPC then hangs (spec F10).
# Every temporal CLI call and the worker read this.
export TEMPORAL_ADDRESS=127.0.0.1:7233
TEMPORAL_HOST=127.0.0.1
TEMPORAL_PORT=7233
DOLT_HOST="${BEADS_HOST:-127.0.0.1}"
DOLT_PORT="${BEADS_PORT:-49209}"

SCHEDULE_ID=materialise-beads
SERVER_DB=local/temporal-live.db
LOG_DIR=local/logs
RUN_DIR=local/run
BIN_DIR=local/bin
SERVER_LOG="$LOG_DIR/temporal.log"
SERVER_PID_FILE="$RUN_DIR/temporal.pid"
WORKER_BIN="$ROOT/$BIN_DIR/worker"
WORKER_LOG="$LOG_DIR/worker.log"
WORKER_PID_FILE="$RUN_DIR/worker.pid"
T_REF="$RUN_DIR/trigger.ref" # mtime = T, for "modified after T" checks
ISSUES_GLOB='bronze/raw/beads/issues/*/*.parquet'
MARTS_DIR=warehouse/marts
# Marts that must be rebuilt by the run: one from each dbt pass.
CHECK_MARTS="dim_issues mart_issue_mention_drift"
DERIVED_DATASET=issue_mentions
# The worker's spans must cover these workflow and activity types (the tracing interceptor names
# a span <operation>:<type>, e.g. RunActivity:FetchWindow), plus at least one dbt.model.* span.
SPAN_TYPES="MaterialiseSource MaterialiseWindow FetchWindow WriteWindow DbtBuild RunTransform"

# Time limits, in seconds. MAIN_BUDGET bounds everything before the exit trap, give or take one
# 10 s CLI call; the trap needs at most 20 s more (10 s per process), so the script ends inside
# 9 minutes (500 + 10 + 20 = 530 s) even when every wait runs to its limit. The completion wait
# (ingestion plus BuildMarts's two dbt builds) gets whatever the budget has left after keeping
# POST_RESERVE for the checks that follow it: two 10 s CLI calls, stopping the worker (at most
# STOP_GRACE) and the file and log checks.
MAIN_BUDGET=500
SERVER_HEALTH_TIMEOUT=60
WORKER_START_TIMEOUT=30
QUIET_TIMEOUT=180
APPEAR_TIMEOUT=30
COMPLETE_TIMEOUT=420
POST_RESERVE=45
STOP_GRACE=10

START_EPOCH=$(date +%s)
OWN_SERVER=0
SERVER_PID=""
WORKER_PID=""
FAIL_REASON=""
PASSED=0
T=""
RUN_WORKFLOW_ID=""
RUN_ID=""
BUILD_MARTS_ID=""
DAY=""

log() { printf '[live-materialise %s] %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }

# fail REASON: record the reason and exit; the EXIT trap prints it.
fail() {
	FAIL_REASON="$*"
	exit 1
}

# An unexpected failing command (set -e) still gets a clear reason.
on_err() {
	local rc=$1 line=$2 cmd=$3
	if [ -z "$FAIL_REASON" ]; then
		FAIL_REASON="command failed (exit $rc) at line $line: $cmd"
	fi
}
trap 'on_err $? $LINENO "$BASH_COMMAND"' ERR

elapsed() { echo $(($(date +%s) - START_EPOCH)); }
remaining() { echo $((MAIN_BUDGET - $(elapsed))); }
min() { if [ "$1" -lt "$2" ]; then echo "$1"; else echo "$2"; fi; }

# port_open HOST PORT: something accepts TCP connections there.
port_open() { (exec 3<>"/dev/tcp/$1/$2") 2>/dev/null; }

# is_alive PID: the process exists and is not a zombie.
is_alive() {
	local stat
	stat=$(ps -p "$1" -o stat= 2>/dev/null) || return 1
	[[ $stat != Z* ]]
}

# stop_pid NAME PID: SIGTERM, then SIGKILL if it is still alive after STOP_GRACE seconds.
stop_pid() {
	local name=$1 pid=$2 i
	is_alive "$pid" || return 0
	log "stopping $name (pid $pid)"
	kill -TERM "$pid" 2>/dev/null || true
	for ((i = 0; i < STOP_GRACE * 2; i++)); do
		if ! is_alive "$pid"; then
			wait "$pid" 2>/dev/null || true
			return 0
		fi
		sleep 0.5
	done
	log "$name did not stop within ${STOP_GRACE}s; sending SIGKILL"
	kill -KILL "$pid" 2>/dev/null || true
	wait "$pid" 2>/dev/null || true
}

stop_worker() {
	if [ -n "$WORKER_PID" ]; then
		stop_pid worker "$WORKER_PID"
	fi
	rm -f "$WORKER_PID_FILE"
}

# stop_server stops the dev server only if this script started it.
stop_server() {
	if [ "$OWN_SERVER" -eq 1 ]; then
		if [ -n "$SERVER_PID" ]; then
			stop_pid "temporal dev server" "$SERVER_PID"
		fi
		rm -f "$SERVER_PID_FILE"
	fi
}

# Last step: on any exit or signal, stop what this script started, then print the verdict as the
# very last line.
cleanup() {
	local rc=$?
	trap - EXIT INT TERM HUP ERR
	set +e
	stop_worker
	stop_server
	rm -f "$T_REF"
	if [ "$PASSED" -eq 1 ] && [ "$rc" -eq 0 ]; then
		log "wall time $(elapsed)s"
		echo "LIVE-MATERIALISE: PASS"
		exit 0
	fi
	if [ -z "$FAIL_REASON" ]; then
		# Every failure path sets a reason; none set and status 0 means a signal arrived.
		if [ "$rc" -eq 0 ]; then FAIL_REASON="interrupted by a signal"; else FAIL_REASON="exited early (status $rc)"; fi
	fi
	log "wall time $(elapsed)s"
	echo "LIVE-MATERIALISE: FAIL $FAIL_REASON"
	exit 1
}

# Step 0: a worker left behind by an earlier, killed run would compete for tasks.
kill_stale_worker() {
	[ -f "$WORKER_PID_FILE" ] || return 0
	local pid
	pid=$(tr -dc '0-9' <"$WORKER_PID_FILE")
	if [ -n "$pid" ] && is_alive "$pid" && ps -p "$pid" -o command= | grep -qF "$BIN_DIR/worker"; then
		log "step 0: stopping stale worker from an earlier run (pid $pid)"
		stop_pid "stale worker" "$pid"
	fi
	rm -f "$WORKER_PID_FILE"
}

# dbt-duckdb's external materialisation needs warehouse/marts to exist (spec F5); the worker's
# dbt runner creates it too, but the script does not rely on that.
prepare_dirs() {
	mkdir -p "$LOG_DIR" "$RUN_DIR" "$BIN_DIR" "$MARTS_DIR"
}

check_dolt() {
	port_open "$DOLT_HOST" "$DOLT_PORT" || fail "beads Dolt server not reachable at $DOLT_HOST:$DOLT_PORT"
}

# Step 1: use a server already listening on 127.0.0.1:7233, or start (and own) a dev server with
# a fresh DB, so no stale run or missed tick carries over.
ensure_server() {
	if port_open "$TEMPORAL_HOST" "$TEMPORAL_PORT"; then
		log "step 1: a Temporal server already listens on $TEMPORAL_ADDRESS; using it (not owned, left running)"
		OWN_SERVER=0
	else
		log "step 1: starting temporal server start-dev on $TEMPORAL_ADDRESS with a fresh $SERVER_DB (log: $SERVER_LOG)"
		rm -f "$SERVER_DB"*
		OWN_SERVER=1
		# The one temporal call without --command-timeout: a long-lived server, stopped by the trap.
		temporal server start-dev --ip 127.0.0.1 --db-filename "$SERVER_DB" >"$SERVER_LOG" 2>&1 </dev/null &
		SERVER_PID=$!
		echo "$SERVER_PID" >"$SERVER_PID_FILE"
	fi
	wait_server_healthy
}

wait_server_healthy() {
	local deadline=$(($(date +%s) + SERVER_HEALTH_TIMEOUT))
	while :; do
		if temporal operator cluster health --command-timeout 5s 2>/dev/null | grep -q SERVING &&
			temporal operator namespace describe --namespace default --command-timeout 5s >/dev/null 2>&1; then
			log "server healthy after $(elapsed)s"
			return 0
		fi
		if [ "$OWN_SERVER" -eq 1 ] && ! is_alive "$SERVER_PID"; then
			fail "dev server exited during startup: $(tail -n 3 "$SERVER_LOG" | tr '\n' ' ')"
		fi
		[ "$(date +%s)" -lt "$deadline" ] || fail "Temporal server at $TEMPORAL_ADDRESS not healthy within ${SERVER_HEALTH_TIMEOUT}s"
		sleep 1
	done
}

# Step 2a: build the worker and upsert the schedule.
build_worker() {
	log "step 2: building $BIN_DIR/worker"
	GOTOOLCHAIN=local CGO_ENABLED=0 go build -o "$WORKER_BIN" ./cmd/worker
}

apply_schedule() {
	"$WORKER_BIN" -root "$ROOT" -apply-schedule || fail "worker -apply-schedule failed"
}

# Step 2b: start the worker in the background and wait until it polls. Its telemetry goes to
# stdout, so the spans land in WORKER_LOG next to the SDK's log lines.
start_worker() {
	PLATFORM_OTEL_EXPORTER=stdout "$WORKER_BIN" -root "$ROOT" >"$WORKER_LOG" 2>&1 </dev/null &
	WORKER_PID=$!
	echo "$WORKER_PID" >"$WORKER_PID_FILE"
	local deadline=$(($(date +%s) + WORKER_START_TIMEOUT))
	until grep -q 'Started Worker' "$WORKER_LOG"; do
		is_alive "$WORKER_PID" || fail "worker exited at startup: $(tail -n 3 "$WORKER_LOG" | tr '\n' ' ')"
		[ "$(date +%s)" -lt "$deadline" ] || fail "worker did not start polling within ${WORKER_START_TIMEOUT}s (see $WORKER_LOG)"
		sleep 0.5
	done
	log "worker polling (pid $WORKER_PID, log: $WORKER_LOG)"
}

count_running() {
	temporal workflow count --command-timeout 10s \
		--query "ExecutionStatus='Running' AND (WorkflowType='MaterialiseSource' OR WorkflowType='BuildMarts')" |
		awk '/Total:/ {print $2}'
}

# schedule_interval prints the schedule's interval in seconds (900 for the declared 15 m).
schedule_interval() {
	temporal schedule describe --schedule-id "$SCHEDULE_ID" -o json --command-timeout 10s |
		{ grep -o '"interval": *"[0-9]*s"' || true; } | head -n 1 | tr -dc '0-9'
}

# Before triggering, wait until no MaterialiseSource / BuildMarts execution is running (on a server
# this script does not own, the new worker finishes them) and no scheduled tick is due within
# 10 s. Under overlap policy Skip, a running execution would swallow the trigger.
wait_for_quiet() {
	local interval running now to_tick
	local limit
	# Leave at least 120 s of the budget for the trigger, the run itself and the checks after it.
	limit=$(min "$QUIET_TIMEOUT" "$(($(remaining) - 120))")
	[ "$limit" -gt 0 ] || fail "no time left in the ${MAIN_BUDGET}s budget to wait for a quiet schedule"
	local deadline=$(($(date +%s) + limit))
	interval=$(schedule_interval)
	[ -n "$interval" ] && [ "$interval" -gt 0 ] || fail "could not read the interval of schedule $SCHEDULE_ID"
	while :; do
		running=$(count_running)
		now=$(date +%s)
		to_tick=$((interval - now % interval))
		if [ "${running:-1}" -eq 0 ] && [ "$to_tick" -gt 10 ]; then
			return 0
		fi
		if [ "$now" -ge "$deadline" ]; then
			if [ "$OWN_SERVER" -eq 0 ]; then
				fail "$running MaterialiseSource/BuildMarts execution(s) still Running on the external server after ${limit}s"
			fi
			fail "schedule $SCHEDULE_ID not quiet after ${limit}s ($running running, next tick in ${to_tick}s)"
		fi
		if [ "${running:-1}" -ne 0 ]; then
			log "waiting: $running MaterialiseSource/BuildMarts execution(s) running"
		else
			log "waiting: a scheduled tick is due in ${to_tick}s"
		fi
		sleep 2
	done
}

# Step 3: record T (and a reference file whose mtime is T) and trigger the schedule.
trigger_schedule() {
	T=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	touch -d "$T" "$T_REF"
	log "step 3: T=$T; triggering schedule $SCHEDULE_ID"
	temporal schedule trigger --schedule-id "$SCHEDULE_ID" --command-timeout 15s
}

# json_field NAME: the first "NAME": "value" string in the JSON on stdin; empty if there is none.
json_field() {
	{ grep -o "\"$1\": *\"[^\"]*\"" || true; } | head -n 1 | sed -E 's/.*"([^"]*)"$/\1/'
}

# Step 4a: the MaterialiseSource execution started at or after T must appear within 30 s.
find_execution() {
	local deadline=$(($(date +%s) + APPEAR_TIMEOUT)) out
	while :; do
		out=$(temporal workflow list --limit 1 -o json --command-timeout 10s \
			--query "WorkflowType='MaterialiseSource' AND StartTime >= '$T'")
		RUN_WORKFLOW_ID=$(printf '%s\n' "$out" | json_field workflowId)
		RUN_ID=$(printf '%s\n' "$out" | json_field runId)
		if [ -n "$RUN_WORKFLOW_ID" ] && [ -n "$RUN_ID" ]; then
			log "step 4: execution $RUN_WORKFLOW_ID (run $RUN_ID)"
			return 0
		fi
		[ "$(date +%s)" -lt "$deadline" ] ||
			fail "no MaterialiseSource execution started at or after $T within ${APPEAR_TIMEOUT}s of the trigger"
		sleep 1
	done
}

workflow_status() {
	temporal workflow describe --workflow-id "$RUN_WORKFLOW_ID" --run-id "$RUN_ID" -o json --command-timeout 10s |
		{ grep -o 'WORKFLOW_EXECUTION_STATUS_[A-Z_]*' || true; } | head -n 1
}

# Step 4b: block on the execution's result (at most 7 min, and within the script budget less
# POST_RESERVE); fail fast on any status but Completed.
wait_for_completion() {
	local budget out rc=0 status
	budget=$(min "$COMPLETE_TIMEOUT" "$(($(remaining) - POST_RESERVE))")
	[ "$budget" -gt 0 ] || fail "no time left in the ${MAIN_BUDGET}s budget to wait for $RUN_WORKFLOW_ID"
	log "waiting up to ${budget}s for $RUN_WORKFLOW_ID to close"
	out=$(temporal workflow result --workflow-id "$RUN_WORKFLOW_ID" --run-id "$RUN_ID" \
		--command-timeout "${budget}s" 2>&1) || rc=$?
	printf '%s\n' "$out" | cut -c1-600
	status=$(workflow_status)
	case "$status" in
	WORKFLOW_EXECUTION_STATUS_COMPLETED)
		log "MaterialiseSource completed ($(elapsed)s since start)"
		;;
	WORKFLOW_EXECUTION_STATUS_RUNNING)
		fail "$RUN_WORKFLOW_ID still Running after ${budget}s"
		;;
	*)
		fail "$RUN_WORKFLOW_ID closed as ${status#WORKFLOW_EXECUTION_STATUS_} (result exit $rc): $(printf '%s' "$out" | tr '\n' ' ' | cut -c1-300)"
		;;
	esac
}

# Step 5: the schedule's id, interval and overlap policy.
show_schedule() {
	local out lines
	out=$(temporal schedule describe --schedule-id "$SCHEDULE_ID" --command-timeout 10s)
	lines=$(printf '%s\n' "$out" | grep -E '^[[:space:]]*(ScheduleId|Spec|OverlapPolicy)[[:space:]]' || true)
	log "step 5: temporal schedule describe --schedule-id $SCHEDULE_ID"
	printf '%s\n' "$lines"
	printf '%s\n' "$lines" | grep -Eq "ScheduleId[[:space:]]+$SCHEDULE_ID\$" || fail "schedule describe shows no id $SCHEDULE_ID"
	printf '%s\n' "$lines" | grep -q '"every":"15m 0s"' || fail "schedule $SCHEDULE_ID interval is not 15m"
	printf '%s\n' "$lines" | grep -Eq 'OverlapPolicy[[:space:]]+Skip$' || fail "schedule $SCHEDULE_ID overlap policy is not Skip"
}

# Step 6: the lookback re-extraction moved _extracted_at of the issues files past T.
check_bronze() {
	local out max later
	compgen -G "$ISSUES_GLOB" >/dev/null || fail "no bronze files match $ISSUES_GLOB"
	out=$(duckdb -noheader -csv -c "SELECT strftime(max(_extracted_at) AT TIME ZONE 'UTC', '%Y-%m-%dT%H:%M:%S.%fZ'), max(_extracted_at) > TIMESTAMPTZ '$T' FROM read_parquet('$ISSUES_GLOB')")
	max=${out%%,*}
	later=${out##*,}
	log "step 6: max(_extracted_at) over $ISSUES_GLOB = $max (T = $T, later: $later)"
	[ "$later" = "true" ] || fail "max(_extracted_at) $max is not later than T $T"
}

# Step 7: the run started a BuildMarts child, and it completed. Its id build-marts/beads/<day>
# names the day whose derived partition RunTransform wrote.
check_build_marts() {
	local out status
	out=$(temporal workflow list --limit 1 -o json --command-timeout 10s \
		--query "WorkflowType='BuildMarts' AND ParentWorkflowId='$RUN_WORKFLOW_ID' AND ParentRunId='$RUN_ID'")
	BUILD_MARTS_ID=$(printf '%s\n' "$out" | json_field workflowId)
	status=$(printf '%s\n' "$out" | { grep -o 'WORKFLOW_EXECUTION_STATUS_[A-Z_]*' || true; } | head -n 1)
	[ -n "$BUILD_MARTS_ID" ] || fail "$RUN_WORKFLOW_ID started no BuildMarts child"
	log "step 7: BuildMarts child $BUILD_MARTS_ID: ${status#WORKFLOW_EXECUTION_STATUS_}"
	[ "$status" = WORKFLOW_EXECUTION_STATUS_COMPLETED ] ||
		fail "BuildMarts child $BUILD_MARTS_ID of $RUN_WORKFLOW_ID is ${status#WORKFLOW_EXECUTION_STATUS_}, not Completed"
	DAY=${BUILD_MARTS_ID##*/}
	# The run's day is the UTC day it started in: T's, or the next one if T was just before midnight.
	case "$DAY" in
	"${T%%T*}" | "$(date -u +%Y-%m-%d)") ;;
	*) fail "BuildMarts child $BUILD_MARTS_ID is not for today (UTC ${T%%T*})" ;;
	esac
}

# newer_parquet DIR: how many Parquet files under DIR were modified after T.
newer_parquet() {
	{ find "$1" -name '*.parquet' -newer "$T_REF" 2>/dev/null || true; } | wc -l | tr -d ' '
}

# Step 8: both dbt passes rewrote their marts, and RunTransform wrote today's derived file.
check_marts() {
	local mart n derived
	for mart in $CHECK_MARTS; do
		n=$(newer_parquet "$MARTS_DIR/$mart")
		log "step 8: $MARTS_DIR/$mart: $n Parquet file(s) modified after T"
		[ "$n" -gt 0 ] || fail "$MARTS_DIR/$mart holds no Parquet file modified after T $T"
	done
	derived="bronze/derived/$DERIVED_DATASET/dt=$DAY/part-0.parquet"
	[ -f "$derived" ] || fail "$derived does not exist"
	n=$(newer_parquet "$derived")
	log "step 8: $derived exists (modified after T: $([ "$n" -gt 0 ] && echo yes || echo no))"
	[ "$n" -gt 0 ] || fail "$derived was not rewritten after T $T"
}

# Step 9: SIGTERM the worker and wait for it to exit by itself: telemetry shutdown, which flushes
# the stdout exporter, runs only on a clean exit.
stop_worker_flush_telemetry() {
	local i rc=0
	log "step 9: stopping the worker (pid $WORKER_PID) so telemetry shutdown flushes its stdout export"
	kill -TERM "$WORKER_PID" 2>/dev/null || true
	for ((i = 0; i < STOP_GRACE * 2; i++)); do
		is_alive "$WORKER_PID" || break
		sleep 0.5
	done
	if is_alive "$WORKER_PID"; then
		fail "worker did not exit within ${STOP_GRACE}s of SIGTERM, so its telemetry was not flushed"
	fi
	wait "$WORKER_PID" 2>/dev/null || rc=$?
	WORKER_PID=""
	rm -f "$WORKER_PID_FILE"
	[ "$rc" -eq 0 ] || fail "worker exited with status $rc after SIGTERM: $(tail -n 3 "$WORKER_LOG" | tr '\n' ' ')"
	if grep -q 'worker: telemetry shutdown:' "$WORKER_LOG"; then
		fail "telemetry shutdown failed: $(grep 'worker: telemetry shutdown:' "$WORKER_LOG" | head -n 1)"
	fi
}

# count_spans ERE: how many exported spans have a name matching ERE. The stdout exporter writes
# one JSON object per span, starting {"Name":"<span name>", ...
count_spans() {
	{ grep -oE "\"Name\":\"$1\"" "$WORKER_LOG" || true; } | wc -l | tr -d ' '
}

# Step 10: the stdout export holds a span for every workflow and activity type the run went
# through, and at least one dbt.model.* span.
check_spans() {
	local type n counts="" missing="" names
	for type in $SPAN_TYPES; do
		n=$(count_spans "[A-Za-z]+:$type")
		counts="$counts $type=$n"
		[ "$n" -gt 0 ] || missing="$missing $type"
	done
	n=$(count_spans 'dbt\.model\.[^"]+')
	counts="$counts dbt.model.*=$n"
	[ "$n" -gt 0 ] || missing="$missing dbt.model.*"
	log "step 10: spans in $WORKER_LOG:$counts"
	names=$({ grep -oE '"Name":"([A-Za-z]+:[A-Za-z]+|dbt\.[a-z_]+\.)' "$WORKER_LOG" || true; } |
		sed -E -e 's/^"Name":"//' -e 's/\.$/.*/' | sort | uniq -c | awk '{printf " %s=%s", $2, $1}')
	log "step 10: span names:$names"
	[ -z "$missing" ] || fail "the worker's stdout export has no span for:$missing"
}

main() {
	kill_stale_worker
	prepare_dirs
	check_dolt
	ensure_server
	build_worker
	apply_schedule
	start_worker
	wait_for_quiet
	trigger_schedule
	find_execution
	wait_for_completion
	show_schedule
	check_bronze
	check_build_marts
	check_marts
	stop_worker_flush_telemetry
	check_spans
	PASSED=1
}

trap cleanup EXIT INT TERM HUP
main
