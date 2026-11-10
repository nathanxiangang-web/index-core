#!/usr/bin/env bash
# Only testutil's exact owner-marked loopback PG18 target is accepted.
set -uo pipefail
: "${INDEXCORE_TEST_DATABASE_URL:?Set the owned index138_test DSN}"
: "${INDEXCORE_REPLAY_EVIDENCE_DIR:?Set a new, empty evidence directory}"
export INDEXCORE_ISSUE138_DISPOSABLE=owned-disposable-only
mkdir -p "$INDEXCORE_REPLAY_EVIDENCE_DIR"
status=0
for mode in baseline f1 f2; do
  INDEXCORE_ISSUE134_REPLAY="$mode" go test -json -count=1 -timeout=12m -run '^TestIssue134SchedulingReplay$' ./internal/runtime/app >"$INDEXCORE_REPLAY_EVIDENCE_DIR/test-$mode.jsonl" 2>&1
  code=$?
  printf '%s exit=%s\n' "$mode" "$code"
  if [ "$code" -ne 0 ]; then status=1; fi
done
python3 scripts/perf/analyze-issue134-replay.py "$INDEXCORE_REPLAY_EVIDENCE_DIR" >"$INDEXCORE_REPLAY_EVIDENCE_DIR/analysis.json"
exit "$status"
