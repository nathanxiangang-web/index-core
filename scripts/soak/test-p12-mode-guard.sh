#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNNER="$SCRIPT_DIR/run-p12-soak.sh"
TEST_TMP="$(mktemp -d)"
FAKE_BIN="$TEST_TMP/bin"
mkdir -p "$FAKE_BIN"
trap 'rm -rf "$TEST_TMP"' EXIT

for command_name in docker curl python3; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"$FAKE_BIN/$command_name"
  chmod +x "$FAKE_BIN/$command_name"
done
printf '#!/usr/bin/env bash\nprintf "test-token\\n"\n' >"$FAKE_BIN/openssl"
chmod +x "$FAKE_BIN/openssl"

LAST_OUTPUT=""
LAST_STATUS=0
run_config_case() {
  set +e
  LAST_OUTPUT="$(env \
    PATH="$FAKE_BIN:$PATH" \
    REFERENCE_WEB_OBSERVER="$TEST_TMP/missing-observer" \
    EVIDENCE_DIR="$TEST_TMP/evidence" \
    P12_LOG_DIR="$TEST_TMP/log" \
    "$@" bash "$RUNNER" 2>&1)"
  LAST_STATUS=$?
  set -e
}

expect_guard_rejection() {
  local expected="$1"
  shift
  run_config_case "$@"
  if [ "$LAST_STATUS" -eq 0 ]; then
    printf 'expected config rejection, got exit 0\n' >&2
    exit 1
  fi
  if [[ "$LAST_OUTPUT" != *"$expected"* ]]; then
    printf 'expected guard message %q, got:\n%s\n' "$expected" "$LAST_OUTPUT" >&2
    exit 1
  fi
}

expect_guard_pass() {
  run_config_case "$@"
  if [[ "$LAST_OUTPUT" != *"observer not found"* ]]; then
    printf 'expected execution to pass config guard and stop at observer check, got:\n%s\n' "$LAST_OUTPUT" >&2
    exit 1
  fi
  if [[ "$LAST_OUTPUT" == *"must be >="* || "$LAST_OUTPUT" == *"must be an integer >="* ]]; then
    printf 'configuration was unexpectedly rejected:\n%s\n' "$LAST_OUTPUT" >&2
    exit 1
  fi
}

expect_guard_rejection \
  "P12_DURATION_SECONDS must be >= 1800 in acceptance mode" \
  P12_MODE=acceptance P12_DURATION_SECONDS=1799 P12_TARGET_VISIBILITY=100

expect_guard_rejection \
  "P12_TARGET_VISIBILITY must be >= 100 in acceptance mode" \
  P12_MODE=acceptance P12_DURATION_SECONDS=1800 P12_TARGET_VISIBILITY=99

expect_guard_rejection \
  "P12_DURATION_SECONDS must be an integer >= 1800 in acceptance mode" \
  P12_MODE=acceptance P12_DURATION_SECONDS=short P12_TARGET_VISIBILITY=100

expect_guard_pass \
  P12_MODE=acceptance P12_DURATION_SECONDS=1800 P12_TARGET_VISIBILITY=100

expect_guard_pass \
  P12_MODE=test P12_DURATION_SECONDS=1 P12_TARGET_VISIBILITY=1

printf 'P12 mode guard tests passed\n'
