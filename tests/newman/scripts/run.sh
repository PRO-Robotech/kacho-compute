#!/usr/bin/env bash
# tests/newman/scripts/run.sh — прогон newman коллекций kacho-compute.
#
# Usage:
#   ./scripts/run.sh                       # все коллекции, сводный отчёт
#   ./scripts/run.sh --service disk        # одна коллекция
#   ./scripts/run.sh --service disk --bail # прерывать после первого fail
#   ./scripts/run.sh --delay 100           # задержка между запросами (ms)
#
# Outputs:
#   out/<resource>.json — newman JSON reporter (для агрегации)
#   out/<resource>.cli  — newman cli-вывод
#   out/summary.txt     — итоговая сводка
#
# Требует: api-gateway доступен по baseUrl из env (локально — port-forward на 18080);
#          newman установлен (`npm install -g newman`); jq для сводки.

set -euo pipefail
cd "$(dirname "$0")/.."

SERVICE=""
BAIL=""
DELAY="100"
EXTRA=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --service) SERVICE="$2"; shift 2 ;;
    --bail)    BAIL="--bail"; shift ;;
    --delay)   DELAY="$2"; shift 2 ;;
    *)         EXTRA+=("$1"); shift ;;
  esac
done

ENV="environments/local.postman_environment.json"
[[ -f "$ENV" ]] || { echo "missing env: $ENV"; exit 1; }

run_one() {
  local res="$1"
  local col="collections/${res}.postman_collection.json"
  if [[ ! -f "$col" ]]; then
    echo "[skip] $res — нет коллекции"
    return 0
  fi
  echo "===== ${res} ====="
  newman run "$col" \
    -e "$ENV" \
    --delay-request "$DELAY" \
    $BAIL \
    --reporters cli,json \
    --reporter-json-export "out/${res}.json" \
    "${EXTRA[@]}" 2>&1 | tee "out/${res}.cli" || true
}

mkdir -p out

if [[ -n "$SERVICE" ]]; then
  run_one "$SERVICE"
else
  for res in disk image snapshot instance disk-type operation; do
    run_one "$res"
  done
fi

echo
echo "===== Summary ====="
{
  printf "%-22s %10s %10s %10s\n" "RESOURCE" "ASSERT" "FAILED" "REQUESTS"
  for f in out/*.json; do
    [[ -f "$f" ]] || continue
    name=$(basename "$f" .json)
    stats=$(jq -r '"\(.run.stats.assertions.total) \(.run.stats.assertions.failed) \(.run.stats.requests.total)"' "$f" 2>/dev/null || echo "0 0 0")
    set -- $stats
    printf "%-22s %10s %10s %10s\n" "$name" "$1" "$2" "$3"
  done
} | tee out/summary.txt
