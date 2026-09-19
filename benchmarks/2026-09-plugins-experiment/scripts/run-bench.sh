#!/usr/bin/env bash
# Прогон бенчмарка: baseline vs plugin по сценариям. На ноутбуке прогоны
# шумные, поэтому best-of-N (по умолчанию 5) с чередованием не нужно —
# шум фиксируем отдельно (baseline vs baseline) и сравниваем медианы.
set -euo pipefail
cd "$(dirname "$0")/../../.."
source benchmarks/2026-09-plugins-experiment/scripts/common.sh

RUNS=${RUNS:-5}
DUR=${DUR:-15s}
C50="${C50:--t2 -c50}"; C300="${C300:--t4 -c300}"

echo "==> build gateway and helpers"
make build
go build -o bin/bench-backend ./benchmarks/2026-09-plugins-experiment/helpers/backend/
go build -o bin/bench-sink ./benchmarks/2026-09-plugins-experiment/helpers/webhook/
make plugin-build
make plugin-so-build

# JWT-сценарии требуют валидный токен: wrk сам Authorization не шлёт, а
# jwt.required: true вернёт 401, и бенчмарк померит отказ, а не hot-path.
JWT_TOKEN="$(gen_jwt_token)"
export JWT_TOKEN
echo "==> JWT token generated (${#JWT_TOKEN} chars)"

# wrk в этом скрипте зовётся с фиксированными параметрами; сценарии ниже
# используют common.wrk_req (см. Task 14, Step 1).
# run_scenario <scenario> [out_name]
run_scenario() {
  local scenario="$1"
  local outname="${2:-$scenario}"
  stop_all
  case "$scenario" in
    *webhooks*) start_sink ;;
  esac
  start_backend
  start_gateway "$scenario.yaml"
  init_csv "$outname"
  wrk_req "$scenario" 50 2 "$DUR" "$RUNS" "$outname"
  wrk_req "$scenario" 300 4 "20s" "$RUNS" "$outname"
  stop_all
}

trap 'stop_all' EXIT

# 0) Noise floor: baseline против самого себя (спред) — отдельный CSV.
run_scenario baseline-jwt baseline-jwt-noise

# 1) JWT: builtin vs so vs shared.
run_scenario baseline-jwt
run_scenario plugin-jwt-so
run_scenario plugin-jwt-shared

# 2) Rate limit: builtin vs shared.
run_scenario baseline-ratelimit
run_scenario plugin-ratelimit-shared

# 3) Webhooks: builtin vs fast.
run_scenario baseline-webhooks
run_scenario plugin-webhooks-fast

# 4) Discovery: builtin vs fast (вне hot-path).
run_scenario baseline-discovery
run_scenario plugin-discovery-fast

echo "DONE. Results in benchmarks/2026-09-plugins-experiment/results/"
