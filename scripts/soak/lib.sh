#!/usr/bin/env bash
#
# P12 deployment-soak shared helpers (verification-only).
#
# Sourced by run-p12-soak.sh. Everything here is test/verification support: it
# drives the provider fixture control surface, sends authenticated loopback
# Mutation Hints, and asks the Reference Test Web observer to confirm rendered
# visibility. It never touches provider credentials, the Hint token of another
# process, or IndexCore internals beyond the accepted HTTP surfaces.

log()  { printf '%s [p12] %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"; }
die()  { printf '%s [p12] FATAL: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*" >&2; exit 1; }
require_cmd() { command -v "$1" >/dev/null 2>&1 || die "missing command: $1"; }

now_ms() { python3 -c 'import time;print(int(time.time()*1000))'; }
now_epoch() { date +%s; }

wait_http() {
  local url="$1" tries="${2:-60}" i
  for i in $(seq 1 "$tries"); do
    curl -fsS -m 3 "$url" >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

psql_q() {
  docker exec "$P12_PG_CONTAINER" psql -U indexcore -d indexcore -t -A -c "$1"
}

fixture_reset()   { curl -s -XPOST "$1/__control/reset"   -H 'Content-Type: application/json' -d "$2" >/dev/null; }
fixture_upsert()  { curl -s -XPOST "$1/__control/upsert"  -H 'Content-Type: application/json' -d "$2" >/dev/null; }
fixture_delete()  { curl -s -XPOST "$1/__control/delete"  -H 'Content-Type: application/json' -d "$2" >/dev/null; }
fixture_fail()    { curl -s -XPOST "$1/__control/fail"    -H 'Content-Type: application/json' -d "$2" >/dev/null; }
fixture_block()   { curl -s -XPOST "$1/__control/block"   -H 'Content-Type: application/json' -d "$2" >/dev/null; }
fixture_delay()   { curl -s -XPOST "$1/__control/delay"   -H 'Content-Type: application/json' -d "$2" >/dev/null; }
fixture_unblock() { curl -s -XPOST "$1/__control/unblock" -H 'Content-Type: application/json' -d '{}' >/dev/null; }
fixture_state()   { curl -s "$1/__control/state"; }

# Hint retry/backpressure state. Counters are initialized here so the 429
# branch is executable under `set -u` (HINT_429_COUNT was previously unbound).
HINT_MAX_ATTEMPTS="${HINT_MAX_ATTEMPTS:-5}"
: "${HINT_429_COUNT:=0}"
HINT_CODE="000"
HINT_RETRY_AFTER=""
HINT_BODY=""
HINT_ATTEMPTS=0

# hint_send <root_id> <scope_key> [reason]
# Sets HINT_CODE (final code; 000 on transport failure), HINT_ATTEMPTS,
# HINT_RETRY_AFTER and HINT_BODY, and increments HINT_429_COUNT per retryable
# 429. 429 is retried up to HINT_MAX_ATTEMPTS honouring Retry-After. The caller
# must require the final HINT_CODE (normal mutation requires 202).
hint_send() {
  local root="$1" scope="$2" reason="${3:-POSSIBLE_CHANGE}"
  local attempt out hdrs
  HINT_CODE="000"; HINT_RETRY_AFTER=""; HINT_BODY=""; HINT_ATTEMPTS=0
  for attempt in $(seq 1 "$HINT_MAX_ATTEMPTS"); do
    HINT_ATTEMPTS="$attempt"
    out="$(mktemp)"; hdrs="$(mktemp)"
    HINT_CODE="$(curl -s -m 10 -o "$out" -D "$hdrs" -w '%{http_code}' \
      -XPOST -H "Authorization: Bearer $P12_HINT_TOKEN" -H 'Content-Type: application/json' \
      --data "{\"root_id\":\"$root\",\"scope_key\":\"$scope\",\"reason\":\"$reason\"}" \
      "http://$P12_HINT_ADDR/internal/v1/mutation-hints" 2>/dev/null || echo 000)"
    HINT_RETRY_AFTER="$(awk -F': ' 'tolower($1)=="retry-after"{print $2}' "$hdrs" | tr -d '\r' | head -1)"
    HINT_BODY="$(cat "$out")"
    rm -f "$out" "$hdrs"
    if [ "$HINT_CODE" != "429" ]; then
      return 0
    fi
    HINT_429_COUNT=$((HINT_429_COUNT + 1))
    sleep "${HINT_RETRY_AFTER:-1}"
  done
  return 0
}

# observer_wait_visible <root_id> <resource> <timeout_seconds> [forbidden_origin]
observer_wait_visible() {
  local root="$1" resource="$2" timeout="${3:-60}" forbidden="${4:-$P12_FORBIDDEN_ORIGIN}"
  bash "$REFERENCE_WEB_OBSERVER" --wait-visible \
    --web-url "$P12_TEST_WEB_URL" --root "$root" \
    --expect-resource "$resource" --visible-timeout "$timeout" \
    --forbidden-hostport "$forbidden"
}

# canonical_present <root_id> <canonical_path> -> echoes count
canonical_present() {
  psql_q "select count(*) from index_canonical_resource where root_id='$1' and canonical_path='$2' and resource_presence='PRESENT';"
}