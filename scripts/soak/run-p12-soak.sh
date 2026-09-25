#!/usr/bin/env bash
#
# P12 deployment-soak orchestration (IndexCore verification side).
#
# Topology (same host, exact-loopback Hint):
#   docker PostgreSQL 127.0.0.1:55433
#   indexcore serve (host process)  Query 127.0.0.1:8080  Hint 127.0.0.1:8090
#   AList/OpenList fixtures 127.0.0.1:9050 (root A) / 127.0.0.1:9051 (root B)
#   Reference Test Web (next start) 127.0.0.1:3100
#
# The host-process form keeps the Hint endpoint on exact 127.0.0.1 loopback and
# lets the soak driver, the fixtures, and the Reference Test Web observer share
# one host, which is what P12 requires for deterministic restart/crash control.
#
# Modes (fail-closed either way):
#   P12_MODE=test        SHORTENED TEST VALIDATION (owner-directed short profile)
#   P12_MODE=acceptance  the mandatory >=30min / >=100 visibility acceptance soak
#
# `full`/`smoke` are rejected: the shortened profile must not masquerade as the
# mandatory long-duration soak.
#
# This is verification-only: no production IndexCore code or contract changes.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INDEXCORE_REPO="${INDEXCORE_REPO:-$(cd "$SCRIPT_DIR/../.." && pwd)}"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
P12_MODE="${P12_MODE:-test}"
P12_PG_CONTAINER="${P12_PG_CONTAINER:-p12-postgres}"
P12_PG_VOLUME="${P12_PG_VOLUME:-p12-pgdata}"
P12_PG_PORT="${P12_PG_PORT:-55433}"
P12_FIXTURE_A="${P12_FIXTURE_A:-http://127.0.0.1:9050}"
P12_FIXTURE_B="${P12_FIXTURE_B:-http://127.0.0.1:9051}"
P12_HTTP_ADDR="${P12_HTTP_ADDR:-127.0.0.1:8080}"
P12_HINT_ADDR="${P12_HINT_ADDR:-127.0.0.1:8090}"
P12_TEST_WEB_URL="${P12_TEST_WEB_URL:-http://127.0.0.1:3100}"
P12_TEST_WEB_PORT="${P12_TEST_WEB_PORT:-3100}"
P12_FORBIDDEN_ORIGIN="${P12_FORBIDDEN_ORIGIN:-127.0.0.1:8080}"
P12_INDEXCORE_BIN="${P12_INDEXCORE_BIN:-/tmp/p12/bin/indexcore}"
P12_INDEXCORE_IMAGE="${P12_INDEXCORE_IMAGE:-index-core-indexcore:latest}"
REFERENCE_WEB_REPO="${REFERENCE_WEB_REPO:-/home/nathan/indexcore-reference-web}"
REFERENCE_WEB_OBSERVER="${REFERENCE_WEB_OBSERVER:-$REFERENCE_WEB_REPO/scripts/soak-observer.sh}"
EVIDENCE_DIR="${EVIDENCE_DIR:-/tmp/p12/evidence}"
P12_LOG_DIR="${P12_LOG_DIR:-/tmp/p12}"

P12_DATABASE_URL="${P12_DATABASE_URL:-postgres://indexcore:indexcore@127.0.0.1:${P12_PG_PORT}/indexcore?sslmode=disable}"
P12_ALIST_TOKEN_VALUE="${P12_ALIST_TOKEN_VALUE:-p12-fixture-token-does-not-leave-the-host}"
export P12_ALIST_TOKEN="$P12_ALIST_TOKEN_VALUE"
export INDEXCORE_DATABASE_URL="$P12_DATABASE_URL"

P12_HINT_TOKEN="${P12_HINT_TOKEN:-$(openssl rand -hex 32)}"
SCOPE_A="/hot-a"
SCOPE_B="/hot-b"

case "$P12_MODE" in
  test)
    PROFILE_LABEL="SHORTENED TEST VALIDATION"
    P12_DURATION_SECONDS="${P12_DURATION_SECONDS:-120}"
    P12_TARGET_VISIBILITY="${P12_TARGET_VISIBILITY:-10}"
    P12_SAMPLE_INTERVAL="${P12_SAMPLE_INTERVAL:-2}"
    P12_BURST_HINTS="${P12_BURST_HINTS:-24}"
    P12_CRASH_RETRY_TIMEOUT="${P12_CRASH_RETRY_TIMEOUT:-120}"
    ;;
  acceptance)
    PROFILE_LABEL="FULL ACCEPTANCE SOAK (>=30min / >=100 visibility)"
    P12_DURATION_SECONDS="${P12_DURATION_SECONDS:-1800}"
    P12_TARGET_VISIBILITY="${P12_TARGET_VISIBILITY:-100}"
    P12_SAMPLE_INTERVAL="${P12_SAMPLE_INTERVAL:-5}"
    P12_BURST_HINTS="${P12_BURST_HINTS:-24}"
    P12_CRASH_RETRY_TIMEOUT="${P12_CRASH_RETRY_TIMEOUT:-180}"
    ;;
  full|smoke)
    die "P12_MODE='$P12_MODE' is not valid; use 'test' (shortened) or 'acceptance' (long-duration)"
    ;;
  *)
    die "P12_MODE must be 'test' or 'acceptance' (got '$P12_MODE')"
    ;;
esac
P12_VISIBLE_TIMEOUT="${P12_VISIBLE_TIMEOUT:-60}"

SERVE_PID=""
WEB_PID=""
OBSERVER_PID=""
FIXTURE_A_PID=""
FIXTURE_B_PID=""
ROOT_A=""
ROOT_B=""

HINT_202=0
HINT_OTHER=0
STEADY_OK=0
VIS_LAT_MS=()
FAILURES=()

# Fail-closed bookkeeping: mandatory predicates append a failure; main exits
# non-zero if any remain.
fail_run() { FAILURES+=("$1"); log "FAIL: $1"; }
assert_eq() { # expected actual label
  [ "$1" = "$2" ] || fail_run "$3: expected '$1', got '$2'"
}
assert_ge() { # min actual label
  if ! [ "$2" -ge "$1" ] 2>/dev/null; then fail_run "$3: expected >= $1, got '$2'"; fi
}

# ---------------------------------------------------------------------------
# Lifecycle
# ---------------------------------------------------------------------------
serve_pid() { printf '%s' "${SERVE_PID:-$(cat "$P12_LOG_DIR/serve.pid" 2>/dev/null || true)}"; }
stop_serve() {
  local pid; pid="$(serve_pid)"
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    kill -TERM "$pid" 2>/dev/null || true
    local i
    for i in $(seq 1 30); do kill -0 "$pid" 2>/dev/null || break; sleep 1; done
  fi
}
kill_serve_hard() {
  local pid; pid="$(serve_pid)"
  if [ -n "$pid" ]; then kill -9 "$pid" 2>/dev/null || true; fi
  pkill -9 -f "$P12_INDEXCORE_BIN serve" 2>/dev/null || true
}
stop_web() { pkill -f 'next start -p '"$P12_TEST_WEB_PORT" 2>/dev/null || true; }
stop_fixtures() { pkill -f "alist_fixture.py --addr 127.0.0.1" 2>/dev/null || true; }
stop_observer() { [ -n "$OBSERVER_PID" ] && kill "$OBSERVER_PID" 2>/dev/null || true; }

cleanup() {
  stop_observer
  stop_web
  stop_serve
  stop_fixtures
}
trap cleanup EXIT

start_serve() {
  nohup env \
    INDEXCORE_DATABASE_URL="$P12_DATABASE_URL" \
    INDEXCORE_HTTP_ADDR="$P12_HTTP_ADDR" \
    INDEXCORE_HINT_ADDR="$P12_HINT_ADDR" \
    INDEXCORE_HINT_TOKEN="$P12_HINT_TOKEN" \
    INDEXCORE_INCREMENTAL_RUNTIME_ENABLED=true \
    INDEXCORE_INCREMENTAL_WAKE_INTERVAL=1s \
    INDEXCORE_LOG_FORMAT=json \
    INDEXCORE_LOG_LEVEL=info \
    P12_ALIST_TOKEN="$P12_ALIST_TOKEN" \
    "$P12_INDEXCORE_BIN" serve >"$P12_LOG_DIR/serve.log" 2>&1 &
  SERVE_PID=$!
  printf '%s\n' "$SERVE_PID" >"$P12_LOG_DIR/serve.pid"
  wait_http "http://$P12_HTTP_ADDR/readyz" 60 || die "IndexCore did not become ready"
}

start_test_web() {
  ( cd "$REFERENCE_WEB_REPO" && INDEXCORE_BASE_URL="http://$P12_HTTP_ADDR" \
      nohup ./node_modules/.bin/next start -p "$P12_TEST_WEB_PORT" \
      >"$P12_LOG_DIR/web.log" 2>&1 & )
  wait_http "$P12_TEST_WEB_URL/" 60 || die "Reference Test Web did not start"
}

start_observer_watch() {
  ( bash "$REFERENCE_WEB_OBSERVER" --web-url "$P12_TEST_WEB_URL" --root "$ROOT_A" \
      --state-file "$P12_LOG_DIR/observer-state" --forbidden-hostport "$P12_FORBIDDEN_ORIGIN" \
      --sample-interval 5 >"$P12_LOG_DIR/observer.log" 2>&1 & echo $! >"$P12_LOG_DIR/observer.pid" )
  sleep 1
  OBSERVER_PID="$(cat "$P12_LOG_DIR/observer.pid" 2>/dev/null || true)"
}

# The continuous observer is part of the oracle: if it dies or reports a failure
# at any point, the run fails. It must never be restarted.
check_observer() { # label
  local label="${1:-observer}"
  if [ -z "$OBSERVER_PID" ] || ! kill -0 "$OBSERVER_PID" 2>/dev/null; then
    fail_run "$label: continuous Reference Test Web observer is not alive"
    return 1
  fi
  if grep -q 'OBSERVER FAILURE' "$P12_LOG_DIR/observer.log" 2>/dev/null; then
    fail_run "$label: continuous observer reported a failure"
    return 1
  fi
  return 0
}

ensure_binary() {
  if [ -x "$P12_INDEXCORE_BIN" ]; then return 0; fi
  mkdir -p "$(dirname "$P12_INDEXCORE_BIN")"
  local name="p12-extract-$$"
  docker create --name "$name" "$P12_INDEXCORE_IMAGE" >/dev/null || die "cannot create image $P12_INDEXCORE_IMAGE"
  docker cp "$name:/usr/local/bin/indexcore" "$P12_INDEXCORE_BIN" >/dev/null
  docker rm "$name" >/dev/null
  chmod +x "$P12_INDEXCORE_BIN"
}

# ---------------------------------------------------------------------------
# Provisioning
# ---------------------------------------------------------------------------
start_postgres_clean() {
  docker rm -f "$P12_PG_CONTAINER" >/dev/null 2>&1 || true
  docker volume rm "$P12_PG_VOLUME" >/dev/null 2>&1 || true
  docker run -d --name "$P12_PG_CONTAINER" \
    -e POSTGRES_USER=indexcore -e POSTGRES_PASSWORD=indexcore -e POSTGRES_DB=indexcore \
    -p "127.0.0.1:${P12_PG_PORT}:5432" -v "${P12_PG_VOLUME}:/var/lib/postgresql" \
    postgres:18 >/dev/null || die "cannot start postgres"
  local i
  for i in $(seq 1 40); do docker exec "$P12_PG_CONTAINER" pg_isready -U indexcore >/dev/null 2>&1 && break; sleep 1; done
  "$P12_INDEXCORE_BIN" migrate 2>&1 | tail -1
}

start_fixtures_and_seed() {
  ( nohup python3 "$INDEXCORE_REPO/scripts/soak/alist_fixture.py" --addr 127.0.0.1:9050 >"$P12_LOG_DIR/fixture-a.log" 2>&1 & echo $! >"$P12_LOG_DIR/fixture-a.pid" )
  ( nohup python3 "$INDEXCORE_REPO/scripts/soak/alist_fixture.py" --addr 127.0.0.1:9051 >"$P12_LOG_DIR/fixture-b.log" 2>&1 & echo $! >"$P12_LOG_DIR/fixture-b.pid" )
  FIXTURE_A_PID="$(cat "$P12_LOG_DIR/fixture-a.pid" 2>/dev/null || true)"
  FIXTURE_B_PID="$(cat "$P12_LOG_DIR/fixture-b.pid" 2>/dev/null || true)"
  wait_http "$P12_FIXTURE_A/healthz" 20 || die "fixture A not up"
  wait_http "$P12_FIXTURE_B/healthz" 20 || die "fixture B not up"
  fixture_reset "$P12_FIXTURE_A" '{"dirs":{"/":[{"name":"hot-a","is_dir":true},{"name":"cold-a","is_dir":true},{"name":"top-a.txt","is_dir":false,"size":5,"sha1":"a1"}],"/hot-a":[],"/cold-a":[]}}'
  fixture_reset "$P12_FIXTURE_B" '{"dirs":{"/":[{"name":"hot-b","is_dir":true},{"name":"top-b.txt","is_dir":false,"size":5,"sha1":"b1"}],"/hot-b":[]}}'
}

create_roots_and_scan() {
  ROOT_A="$(python3 -c 'import uuid;print(uuid.uuid4())')"
  ROOT_B="$(python3 -c 'import uuid;print(uuid.uuid4())')"
  local pair id port
  for pair in "$ROOT_A:9050" "$ROOT_B:9051"; do
    id="${pair%%:*}"; port="${pair##*:}"
    "$P12_INDEXCORE_BIN" root create --root-id "$id" --lifecycle ACTIVE >/dev/null 2>&1
    "$P12_INDEXCORE_BIN" root config set --root-id "$id" --grace 1h --move-horizon 1h --min-consecutive 1 --min-independent 1 >/dev/null 2>&1
    "$P12_INDEXCORE_BIN" root adapter set --root-id "$id" --collector alist \
      --config "{\"base_url\":\"http://127.0.0.1:$port\",\"path\":\"/\",\"token_env\":\"P12_ALIST_TOKEN\"}" >/dev/null 2>&1
    "$P12_INDEXCORE_BIN" scan --root "$id" >/dev/null 2>&1 || die "initial scan failed for $id"
  done
  log "roots created: A=$ROOT_A B=$ROOT_B"
}

provision() {
  ensure_binary
  log "provisioning (mode=$P12_MODE profile=\"$PROFILE_LABEL\")"
  cleanup; sleep 1
  docker stop index-core-indexcore-1 >/dev/null 2>&1 || true
  start_postgres_clean
  start_fixtures_and_seed
  create_roots_and_scan
  start_serve
  start_test_web
  start_observer_watch
  check_observer "provision"
  log "provisioned; readyz=$(curl -s -o /dev/null -w '%{http_code}' "http://$P12_HTTP_ADDR/readyz")"
}

# ---------------------------------------------------------------------------
# Steady soak
# ---------------------------------------------------------------------------
# one_mutation <root> <fixture> <scope> <resource> [timeout]
one_mutation() {
  local root="$1" fixture="$2" scope="$3" resource="$4" timeout="${5:-$P12_VISIBLE_TIMEOUT}"
  fixture_upsert "$fixture" "{\"path\":\"$scope\",\"entry\":{\"name\":\"$resource\",\"is_dir\":false,\"size\":9,\"sha1\":\"$resource\"}}"
  local t0; t0="$(now_ms)"
  hint_send "$root" "$scope" POSSIBLE_CHANGE
  case "$HINT_CODE" in
    202) HINT_202=$((HINT_202 + 1)) ;;
    *)   HINT_OTHER=$((HINT_OTHER + 1)) ;;
  esac
  if [ "$HINT_CODE" != "202" ]; then
    fail_run "mutation $resource: Hint final code '$HINT_CODE' (expected 202, attempts=$HINT_ATTEMPTS)"
    return 1
  fi
  if ! observer_wait_visible "$root" "$resource" "$timeout" >/dev/null 2>&1; then
    fail_run "mutation $resource: not Test Web-visible within ${timeout}s"
    return 1
  fi
  local t1; t1="$(now_ms)"
  local latency=$((t1 - t0))
  VIS_LAT_MS+=("$latency")
  printf '%s,%s,%s,%s,%s,%s\n' "$STEADY_OK" "$root" "$scope" "$resource" "$HINT_CODE" "$latency" >>"$EVIDENCE_DIR/steady.csv"
  STEADY_OK=$((STEADY_OK + 1))
  return 0
}

run_steady() {
  log "steady soak start: duration=${P12_DURATION_SECONDS}s target=${P12_TARGET_VISIBILITY}"
  : >"$EVIDENCE_DIR/steady.csv"
  echo "seq,root,scope,resource,hint_code,latency_ms" >>"$EVIDENCE_DIR/steady.csv"
  local start now seq=0
  start="$(now_epoch)"
  while :; do
    now="$(now_epoch)"
    [ $((now - start)) -ge "$P12_DURATION_SECONDS" ] && break

    seq=$((seq + 1))
    local resource="soak-$(printf '%05d' "$seq").txt"
    if ! one_mutation "$ROOT_A" "$P12_FIXTURE_A" "$SCOPE_A" "$resource"; then
      printf 'steady_failure,seq=%s,resource=%s\n' "$seq" "$resource" >>"$EVIDENCE_DIR/steady.csv"
      break
    fi
    resource="soak-b-$(printf '%05d' "$seq").txt"
    if ! one_mutation "$ROOT_B" "$P12_FIXTURE_B" "$SCOPE_B" "$resource"; then
      printf 'steady_failure,seq=%s,resource=%s\n' "$seq" "$resource" >>"$EVIDENCE_DIR/steady.csv"
      break
    fi
    sleep "$P12_SAMPLE_INTERVAL"
  done
  assert_ge "$P12_TARGET_VISIBILITY" "$STEADY_OK" "steady visibility"
  log "steady soak done: visible=${STEADY_OK} hint202=${HINT_202} hint429=${HINT_429_COUNT} other=${HINT_OTHER}"
}

latency_summary() {
  if [ "${#VIS_LAT_MS[@]}" -eq 0 ]; then echo "min=NA p50=NA p95=NA max=NA n=0"; return; fi
  printf '%s\n' "${VIS_LAT_MS[@]}" | sort -n >"$P12_LOG_DIR/lat.txt"
  local n min max p50 p95
  n="$(wc -l <"$P12_LOG_DIR/lat.txt")"
  min="$(head -1 "$P12_LOG_DIR/lat.txt")"
  max="$(tail -1 "$P12_LOG_DIR/lat.txt")"
  p50="$(awk -v n="$n" 'NR==int((n+1)/2){print; exit}' "$P12_LOG_DIR/lat.txt")"
  p95="$(awk -v n="$n" 'NR==int((n*95+99)/100){print; exit}' "$P12_LOG_DIR/lat.txt")"
  echo "min=${min}ms p50=${p50}ms p95=${p95}ms max=${max}ms n=${n}"
}

# ---------------------------------------------------------------------------
# Scenarios (each fails closed)
# ---------------------------------------------------------------------------
scenario_hint_burst() {
  log "scenario: hint burst (${P12_BURST_HINTS} hints)"
  local i codes=() c202=0 c429=0 cother=0
  for i in $(seq 1 5); do
    fixture_upsert "$P12_FIXTURE_A" "{\"path\":\"$SCOPE_A\",\"entry\":{\"name\":\"burst-a-$i.txt\",\"is_dir\":false,\"size\":3,\"sha1\":\"ba$i\"}}"
  done
  for i in $(seq 1 5); do
    fixture_upsert "$P12_FIXTURE_B" "{\"path\":\"$SCOPE_B\",\"entry\":{\"name\":\"burst-b-$i.txt\",\"is_dir\":false,\"size\":3,\"sha1\":\"bb$i\"}}"
  done
  local fatal_before; fatal_before="$(grep -c incremental_runtime_fatal "$P12_LOG_DIR/serve.log" || true)"
  for i in $(seq 1 "$P12_BURST_HINTS"); do
    if [ $((i % 2)) -eq 0 ]; then hint_send "$ROOT_A" "$SCOPE_A" POSSIBLE_CHANGE; else hint_send "$ROOT_B" "$SCOPE_B" POSSIBLE_CHANGE; fi
    codes+=("$HINT_CODE")
    case "$HINT_CODE" in
      202) c202=$((c202 + 1)) ;;
      429) c429=$((c429 + 1)) ;;
      *)   cother=$((cother + 1)) ;;
    esac
  done
  local ok=0
  for i in $(seq 1 5); do observer_wait_visible "$ROOT_A" "burst-a-$i.txt" 60 >/dev/null 2>&1 && ok=$((ok + 1)); done
  for i in $(seq 1 5); do observer_wait_visible "$ROOT_B" "burst-b-$i.txt" 60 >/dev/null 2>&1 && ok=$((ok + 1)); done
  local fatal_after; fatal_after="$(grep -c incremental_runtime_fatal "$P12_LOG_DIR/serve.log" || true)"
  python3 - "$EVIDENCE_DIR/burst.json" "${codes[*]}" "$ok" "$fatal_before" "$fatal_after" <<'PY'
import json, sys
path, codes, ok, fb, fa = sys.argv[1:6]
codes = codes.split() if codes else []
json.dump({"hints": len(codes), "codes": codes,
           "final_202": codes.count("202"), "final_429": codes.count("429"),
           "unexpected_final": len([c for c in codes if c not in ("202", "429")]),
           "visible": int(ok), "expected_visible": 10,
           "runtime_fatal_before": int(fb), "runtime_fatal_after": int(fa)}, open(path, "w"), indent=2)
PY
  assert_eq 0 "$cother" "burst unexpected final Hint codes (202/429 only)"
  assert_eq 10 "$ok" "burst visibility (10 expected)"
  assert_eq "$fatal_before" "$fatal_after" "burst runtime fatal count unchanged"
  log "burst: final202=$c202 final429=$c429 unexpected=$cother visible=${ok}/10 fatal=${fatal_after}"
}

scenario_transient_retry() {
  log "scenario: provider transient -> automatic retry"
  fixture_upsert "$P12_FIXTURE_A" "{\"path\":\"$SCOPE_A\",\"entry\":{\"name\":\"retry-a.txt\",\"is_dir\":false,\"size\":4,\"sha1\":\"ra\"}}"
  fixture_fail "$P12_FIXTURE_A" "{\"count\":1,\"paths\":[\"$SCOPE_A\"],\"status\":500,\"code\":500}"
  local t0; t0="$(now_epoch)"
  hint_send "$ROOT_A" "$SCOPE_A" POSSIBLE_CHANGE
  if [ "$HINT_CODE" != "202" ]; then
    fail_run "transient: Hint final code '$HINT_CODE' (expected 202)"
  fi
  local retry_seen=0 visible=0 elapsed=0
  while [ "$elapsed" -lt "$P12_CRASH_RETRY_TIMEOUT" ]; do
    if grep -q incremental_retry_promotion "$P12_LOG_DIR/serve.log"; then retry_seen=1; fi
    if observer_wait_visible "$ROOT_A" "retry-a.txt" 5 >/dev/null 2>&1; then visible=1; break; fi
    elapsed=$(( $(now_epoch) - t0 )); sleep 3
  done
  local error_class; error_class="$(psql_q "select coalesce(last_error_class,'') from index_dirty_scope_work where root_id='$ROOT_A' and scope_key='$SCOPE_A';" || true)"
  python3 - "$EVIDENCE_DIR/transient.json" "$retry_seen" "$visible" "$elapsed" "$error_class" <<'PY'
import json, sys
path, retry_seen, visible, elapsed, error_class = sys.argv[1:6]
json.dump({"retry_promotion_seen": retry_seen == "1", "visible": visible == "1",
           "seconds_to_visible": int(elapsed), "last_error_class": error_class}, open(path, "w"), indent=2)
PY
  assert_eq 1 "$retry_seen" "transient retry promotion observed"
  assert_eq 1 "$visible" "transient eventual visibility"
  log "transient retry: retry_seen=$retry_seen visible=$visible after ${elapsed}s"
}

scenario_graceful_restart() {
  log "scenario: graceful restart"
  local readyz_seen_503=0 degraded_seen=0 web_held_200=0 leak=0 recovered=0 inflight=0
  # 1) create one in-flight cycle in the NORMAL window (IndexCore still healthy).
  fixture_upsert "$P12_FIXTURE_A" "{\"path\":\"$SCOPE_A\",\"entry\":{\"name\":\"drain-a.txt\",\"is_dir\":false,\"size\":4,\"sha1\":\"da\"}}"
  fixture_delay "$P12_FIXTURE_A" "{\"seconds\":8,\"paths\":[\"$SCOPE_A\"]}"
  hint_send "$ROOT_A" "$SCOPE_A" POSSIBLE_CHANGE
  local i
  for i in $(seq 1 30); do
    local n; n="$(psql_q "select count(*) from index_dirty_scope_work where root_id='$ROOT_A' and scope_key='$SCOPE_A' and work_state='IN_FLIGHT';")"
    [ "$n" = "1" ] && { inflight=1; break; }
    sleep 0.5
  done
  # 2) declare the degraded window immediately before shutdown (not seconds
  #    early), so the observer never sees a healthy page in a degraded window.
  echo degraded >"$P12_LOG_DIR/observer-state"
  local pid; pid="$(serve_pid)"
  : >"$P12_LOG_DIR/readyz-poll.txt"
  local pollers=(); local k
  for k in $(seq 1 8); do
    ( while kill -0 "$pid" 2>/dev/null; do
        curl -s -o /dev/null -w '%{http_code}\n' -m 2 "http://$P12_HTTP_ADDR/readyz" 2>/dev/null >>"$P12_LOG_DIR/readyz-poll.txt" || echo 000 >>"$P12_LOG_DIR/readyz-poll.txt"
      done ) &
    pollers+=($!)
  done
  kill -TERM "$pid" 2>/dev/null || true
  local body code
  for i in $(seq 1 45); do
    body="$(curl -s -m 5 "$P12_TEST_WEB_URL/" 2>/dev/null || true)"
    code="$(curl -s -o /dev/null -w '%{http_code}' -m 5 "$P12_TEST_WEB_URL/" 2>/dev/null || echo 000)"
    [ "$code" = "200" ] && web_held_200=1
    case "$body" in *"IndexCore is not fully available"*|*"IndexCore is unreachable"*) degraded_seen=1 ;; esac
    case "$body" in *"$P12_FORBIDDEN_ORIGIN"*) leak=1 ;; esac
    kill -0 "$pid" 2>/dev/null || break
    sleep 1
  done
  local p; for p in "${pollers[@]}"; do wait "$p" 2>/dev/null || true; done
  grep -q '^503$' "$P12_LOG_DIR/readyz-poll.txt" && readyz_seen_503=1
  fixture_delay "$P12_FIXTURE_A" '{"seconds":0}'
  # 3) restart, then leave the degraded window only once IndexCore is ready.
  start_serve
  rm -f "$P12_LOG_DIR/observer-state"
  if observer_wait_visible "$ROOT_A" "soak-00001.txt" 30 >/dev/null 2>&1 || one_mutation "$ROOT_A" "$P12_FIXTURE_A" "$SCOPE_A" "post-restart-a.txt"; then recovered=1; fi
  python3 - "$EVIDENCE_DIR/restart.json" "$readyz_seen_503" "$degraded_seen" "$web_held_200" "$leak" "$recovered" "$inflight" <<'PY'
import json, sys
p = sys.argv[1:]
json.dump({"readyz_503": p[1] == "1", "readyz_503_note": "not-observed is allowed for this shortened test",
           "degraded_seen": p[2] == "1", "web_held_200": p[3] == "1",
           "origin_leak": p[4] == "1", "recovered_and_visible": p[5] == "1",
           "inflight_created": p[6] == "1"}, open(p[0], "w"), indent=2)
PY
  # readyz 503 is explicitly NON-BLOCKING (P11 readiness tests cover it).
  assert_eq 1 "$degraded_seen" "graceful restart degraded render"
  assert_eq 1 "$web_held_200" "graceful restart Web held HTTP 200"
  assert_eq 0 "$leak" "graceful restart no private-origin leak"
  assert_eq 1 "$recovered" "graceful restart recovery + later visibility"
  log "graceful restart: readyz503=$readyz_seen_503(not-blocking) degraded=$degraded_seen hold200=$web_held_200 leak=$leak recovered=$recovered"
}

scenario_crash_recovery() {
  log "scenario: deterministic crash recovery"
  local inflight=0 i visible=0
  # 1) block one scoped request and confirm in-flight in the NORMAL window.
  fixture_upsert "$P12_FIXTURE_A" "{\"path\":\"$SCOPE_A\",\"entry\":{\"name\":\"crash-a.txt\",\"is_dir\":false,\"size\":4,\"sha1\":\"ca\"}}"
  fixture_block "$P12_FIXTURE_A" "{\"paths\":[\"$SCOPE_A\"]}"
  hint_send "$ROOT_A" "$SCOPE_A" POSSIBLE_CHANGE
  if [ "$HINT_CODE" != "202" ]; then fail_run "crash: Hint final code '$HINT_CODE' (expected 202)"; fi
  for i in $(seq 1 40); do
    local n; n="$(psql_q "select count(*) from index_dirty_scope_work where root_id='$ROOT_A' and scope_key='$SCOPE_A' and work_state='IN_FLIGHT';")"
    [ "$n" = "1" ] && { inflight=1; break; }
    sleep 1
  done
  # 2) declare degraded immediately before the hard kill.
  echo degraded >"$P12_LOG_DIR/observer-state"
  kill_serve_hard
  fixture_unblock "$P12_FIXTURE_A"
  sleep 1
  local recovered_events_before; recovered_events_before="$(grep -c incremental_inflight_recovered "$P12_LOG_DIR/serve.log" || true)"
  # 3) restart on the same volume, then leave the degraded window once ready.
  start_serve
  rm -f "$P12_LOG_DIR/observer-state"
  sleep 3
  local recovered_events_after; recovered_events_after="$(grep -c incremental_inflight_recovered "$P12_LOG_DIR/serve.log" || true)"
  hint_send "$ROOT_A" "$SCOPE_A" POSSIBLE_CHANGE
  if observer_wait_visible "$ROOT_A" "crash-a.txt" "$P12_CRASH_RETRY_TIMEOUT" >/dev/null 2>&1; then visible=1; fi
  local residue; residue="$(psql_q "select count(*) from index_dirty_scope_work where root_id='$ROOT_A' and scope_key='$SCOPE_A' and work_state='IN_FLIGHT';")"
  python3 - "$EVIDENCE_DIR/crash.json" "$inflight" "$recovered_events_before" "$recovered_events_after" "$visible" "$residue" <<'PY'
import json, sys
p = sys.argv[1:]
json.dump({"blocked_inflight_confirmed": p[1] == "1",
           "recovered_events_before": int(p[2]), "recovered_events_after": int(p[3]),
           "visible_after_recovery": p[4] == "1", "inflight_residue": int(p[5])},
          open(p[0], "w"), indent=2)
PY
  assert_eq 1 "$inflight" "crash recovery blocked in-flight confirmed"
  # start_serve truncates serve.log, so assert the startup recovery event in the
  # NEW process log rather than comparing counts across two different logs.
  assert_ge 1 "$recovered_events_after" "crash startup stale-IN_FLIGHT recovery event"
  assert_eq 1 "$visible" "crash recovery eventual visibility"
  assert_eq 0 "$residue" "crash recovery IN_FLIGHT residue"
  log "crash recovery: inflight=$inflight recovered_events=$recovered_events_after visible=$visible residue=$residue"
}

# ---------------------------------------------------------------------------
# Final durable-state verification (fail-closed)
# ---------------------------------------------------------------------------
verify_durable_state() {
  log "final durable-state verification"
  local work_state admission_status root_lifecycle
  work_state="$(psql_q "select work_state||':'||count(*) from index_dirty_scope_work group by work_state order by work_state;")"
  admission_status="$(psql_q "select status||':'||count(*) from index_admission group by status order by status;")"
  root_lifecycle="$(psql_q "select lifecycle_state||':'||count(*) from index_root group by lifecycle_state order by lifecycle_state;")"
  local inflight pending retry blocked
  inflight="$(psql_q "select count(*) from index_dirty_scope_work where work_state='IN_FLIGHT';")"
  pending="$(psql_q "select count(*) from index_dirty_scope_work where work_state='PENDING';")"
  retry="$(psql_q "select count(*) from index_dirty_scope_work where work_state='RETRY_WAIT' and pending_not_before is not null and pending_not_before <= now();")"
  blocked="$(psql_q "select count(*) from index_dirty_scope_work where work_state in ('BLOCKED','SUSPENDED');")"
  local adm_pending; adm_pending="$(psql_q "select count(*) from index_admission where status='PENDING';")"
  local active_roots; active_roots="$(psql_q "select count(*) from index_root where lifecycle_state='ACTIVE';")"
  stop_serve
  sleep 1
  local lock_free; lock_free="$(psql_q "select pg_try_advisory_lock(490651870);" | tr -d '[:space:]')"
  python3 - "$EVIDENCE_DIR/durable_state.json" "$inflight" "$pending" "$retry" "$blocked" "$adm_pending" "$active_roots" "$lock_free" "$work_state" "$admission_status" "$root_lifecycle" <<'PY'
import json, sys
p = sys.argv[1:]
json.dump({
  "unexpected_inflight": int(p[1]),
  "unresolved_pending_work": int(p[2]),
  "overdue_retry_wait": int(p[3]),
  "blocked_or_suspended": int(p[4]),
  "pending_admission": int(p[5]),
  "active_roots": int(p[6]),
  "writer_lock_free_after_shutdown": p[7] == "t",
  "work_state": [l for l in p[8].splitlines() if l],
  "admission_status": [l for l in p[9].splitlines() if l],
  "root_lifecycle": [l for l in p[10].splitlines() if l],
}, open(p[0], "w"), indent=2)
PY
  assert_eq 0 "$inflight" "durable unexpected IN_FLIGHT"
  assert_eq 0 "$pending" "durable unresolved PENDING work"
  assert_eq 0 "$retry" "durable overdue RETRY_WAIT"
  assert_eq 0 "$blocked" "durable BLOCKED/SUSPENDED"
  assert_eq 0 "$adm_pending" "durable PENDING admission"
  assert_ge 2 "$active_roots" "durable ACTIVE roots"
  assert_eq t "$lock_free" "durable writer lock released after shutdown"
  log "durable: inflight=$inflight pending=$pending retry_due=$retry blocked=$blocked adm_pending=$adm_pending active_roots=$active_roots lock_free=$lock_free"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
main() {
  require_cmd docker; require_cmd curl; require_cmd python3; require_cmd openssl
  mkdir -p "$EVIDENCE_DIR" "$P12_LOG_DIR/bin"
  [ -f "$REFERENCE_WEB_OBSERVER" ] || die "observer not found: $REFERENCE_WEB_OBSERVER"
  bash "$SCRIPT_DIR/test-hint-429.sh" >"$P12_LOG_DIR/hint-429-selftest.log" 2>&1 \
    || die "Hint 429 retry-path self-test failed (see $P12_LOG_DIR/hint-429-selftest.log)"

  provision
  run_steady
  check_observer "after steady"

  scenario_hint_burst
  check_observer "after hint burst"
  scenario_transient_retry
  check_observer "after transient retry"
  scenario_graceful_restart
  check_observer "after graceful restart"
  scenario_crash_recovery
  check_observer "after crash recovery"

  # Stop the continuous observer before the final shutdown probe so it is not
  # asked to judge an intentional IndexCore-down interval outside a declared
  # window.
  stop_observer
  verify_durable_state
  stop_web
  stop_fixtures

  latency_summary >"$EVIDENCE_DIR/latency.txt"
  {
    echo "profile=$PROFILE_LABEL"
    echo "mode=$P12_MODE"
    echo "duration_seconds=$P12_DURATION_SECONDS"
    echo "target_visibility=$P12_TARGET_VISIBILITY"
    echo "visible_confirmations=$STEADY_OK"
    echo "hint_final_202=$HINT_202"
    echo "hint_retryable_429=$HINT_429_COUNT"
    echo "hint_other=$HINT_OTHER"
    echo "root_a=$ROOT_A"
    echo "root_b=$ROOT_B"
    echo "failures=${#FAILURES[@]}"
    echo "latency=$(cat "$EVIDENCE_DIR/latency.txt")"
  } >"$EVIDENCE_DIR/summary.env"

  log "================ P12 SOAK SUMMARY ($PROFILE_LABEL) ================"
  cat "$EVIDENCE_DIR/summary.env"
  echo "--- durable_state.json ---"; cat "$EVIDENCE_DIR/durable_state.json"
  echo "--- burst.json ---"; cat "$EVIDENCE_DIR/burst.json"
  echo "--- transient.json ---"; cat "$EVIDENCE_DIR/transient.json"
  echo "--- restart.json ---"; cat "$EVIDENCE_DIR/restart.json"
  echo "--- crash.json ---"; cat "$EVIDENCE_DIR/crash.json"
  log "=================================================="

  if [ "${#FAILURES[@]}" -gt 0 ]; then
    log "P12 $PROFILE_LABEL FAILED (fail-closed): ${#FAILURES[@]} failure(s)"
    local f
    for f in "${FAILURES[@]}"; do printf '  - %s\n' "$f" >&2; done
    exit 1
  fi
  log "P12 $PROFILE_LABEL completed (fail-closed, no failures)"
}

main "$@"