#!/usr/bin/env bash
#
# Helper-level regression for the soak driver's Hint 429 retry path
# (verification-only). It mocks curl so the 429/backpressure branch is actually
# executable without waiting for real Hint-ingress saturation, and it proves the
# retryable-429 counter is initialized (the Round 1 unbound-variable bug).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Minimal environment for lib.sh (no live services are contacted: curl is mocked).
P12_HINT_TOKEN="self-test-token"
P12_HINT_ADDR="127.0.0.1:8099"
P12_FORBIDDEN_ORIGIN="127.0.0.1:8080"
P12_TEST_WEB_URL="http://127.0.0.1:3100"
REFERENCE_WEB_OBSERVER="/nonexistent/observer.sh"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

# hint_send runs curl inside command substitution, so a shell variable counter
# would not survive the subshell. The call count therefore lives in a file.
CALLS_FILE="$(mktemp)"
echo 0 >"$CALLS_FILE"
MOCK_MODE=""

# Mock curl: only the flags hint_send uses are interpreted (-o, -D, -w).
curl() {
  local out="" hdr=""
  while [ $# -gt 0 ]; do
    case "$1" in
      -o) out="$2"; shift 2 ;;
      -D) hdr="$2"; shift 2 ;;
      -w) shift 2 ;;
      *) shift ;;
    esac
  done
  local n; n=$(( $(cat "$CALLS_FILE" 2>/dev/null || echo 0) + 1 )); echo "$n" >"$CALLS_FILE"
  case "$MOCK_MODE" in
    two429then202)
      if [ "$n" -le 2 ]; then
        printf '' > "$out"; printf 'Retry-After: 0\r\n' > "$hdr"; echo 429
      else
        printf '{"status":"accepted"}' > "$out"; printf 'Content-Type: application/json\r\n' > "$hdr"; echo 202
      fi
      ;;
    always429)
      printf '' > "$out"; printf 'Retry-After: 0\r\n' > "$hdr"; echo 429
      ;;
    serverError)
      printf '' > "$out"; printf '\r\n' > "$hdr"; echo 500
      ;;
    transportFail)
      return 7
      ;;
  esac
}

fail() { echo "FAIL: $*" >&2; exit 1; }

# 1) genuine 429 then 202: bounded retry, counter initialized, final code 202.
echo 0 >"$CALLS_FILE"; MOCK_MODE="two429then202"; HINT_429_COUNT=0; HINT_MAX_ATTEMPTS=5
hint_send "root-x" "/hot-a" POSSIBLE_CHANGE
[ "$HINT_CODE" = "202" ] || fail "expected final 202, got $HINT_CODE"
[ "$HINT_ATTEMPTS" = "3" ] || fail "expected 3 attempts, got $HINT_ATTEMPTS"
[ "$HINT_429_COUNT" = "2" ] || fail "expected 2 retryable 429, got $HINT_429_COUNT"

# 2) persistent 429: bounded attempts, no crash under set -u.
echo 0 >"$CALLS_FILE"; MOCK_MODE="always429"; HINT_429_COUNT=0; HINT_MAX_ATTEMPTS=4
hint_send "root-x" "/hot-a" POSSIBLE_CHANGE
[ "$HINT_CODE" = "429" ] || fail "expected final 429, got $HINT_CODE"
[ "$HINT_ATTEMPTS" = "4" ] || fail "expected 4 attempts, got $HINT_ATTEMPTS"
[ "$HINT_429_COUNT" = "4" ] || fail "expected 4 retryable 429, got $HINT_429_COUNT"

# 3) an unexpected 500 is surfaced as the final code (caller must fail closed).
echo 0 >"$CALLS_FILE"; MOCK_MODE="serverError"; HINT_MAX_ATTEMPTS=3
hint_send "root-x" "/hot-a" POSSIBLE_CHANGE
[ "$HINT_CODE" = "500" ] || fail "expected final 500, got $HINT_CODE"

# 4) transport failure surfaces as 000.
echo 0 >"$CALLS_FILE"; MOCK_MODE="transportFail"; HINT_MAX_ATTEMPTS=2
hint_send "root-x" "/hot-a" POSSIBLE_CHANGE
[ "$HINT_CODE" = "000" ] || fail "expected 000 on transport failure, got $HINT_CODE"

echo "hint 429 retry-path self-test: OK"