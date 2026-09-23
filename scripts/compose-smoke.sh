#!/usr/bin/env bash
# Gate 3 G3-R2.6 clean-volume Compose smoke test.
# Proves: down -v -> up --build -> migrate exits 0 -> serve ready -> /readyz 200
# -> restart -> ready again with the persistent DB.
set -euo pipefail
cd "$(dirname "$0")/.."

BASE_URL="${COMPOSE_SMOKE_URL:-http://127.0.0.1:8080}"

wait_ready() {
  for _ in $(seq 1 90); do
    code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE_URL/readyz" || true)
    if [ "$code" = "200" ]; then return 0; fi
    sleep 1
  done
  echo "ERROR: /readyz did not become 200 ($BASE_URL/readyz)"
  docker compose logs --tail=50 indexcore || true
  return 1
}

echo "== docker compose down -v =="
docker compose down -v

echo "== docker compose up -d --build =="
docker compose up -d --build

echo "== waiting for one-shot migrate to complete =="
for _ in $(seq 1 90); do
  state=$(docker compose ps -a --format '{{.Service}} {{.State}}' | awk '$1=="migrate"{print $2}')
  if [ "$state" = "exited" ]; then
    code=$(docker inspect -f '{{.State.ExitCode}}' "$(docker compose ps -aq migrate)")
    echo "migrate exit code: $code"
    [ "$code" = "0" ] || { echo "ERROR: migrate exited $code"; exit 1; }
    break
  fi
  sleep 1
done

echo "== waiting for serve readiness =="
wait_ready
echo "== GET /readyz -> $(curl -s "$BASE_URL/readyz") =="

echo "== restart indexcore, DB persists =="
docker compose restart indexcore
wait_ready
echo "== GET /readyz after restart -> $(curl -s "$BASE_URL/readyz") =="

echo "COMPOSE SMOKE OK"