#!/usr/bin/env bash
# Общие функции прогона бенчмарка: старт/стоп гейтвея, backend, wrk-прогон.
set -euo pipefail

GATEWAY_BIN=${GATEWAY_BIN:-./bin/api-gateway}
BACKEND_BIN=${BACKEND_BIN:-./bin/bench-backend}
SINK_BIN=${SINK_BIN:-./bin/bench-sink}
CONFIGS=${CONFIGS:-benchmarks/2026-09-plugins-experiment/configs}
RESULTS=${RESULTS:-benchmarks/2026-09-plugins-experiment/results}
mkdir -p "$RESULTS"

GW_PID=""
BACKEND_PID=""
SINK_PID=""

start_backend() {
  "$BACKEND_BIN" >/tmp/bench-backend.log 2>&1 &
  BACKEND_PID=$!
}

start_sink() {
  "$SINK_BIN" >/tmp/bench-sink.log 2>&1 &
  SINK_PID=$!
}

start_gateway() {
  local cfg="$1"
  "$GATEWAY_BIN" -config "$CONFIGS/$cfg" >/tmp/bench-gateway.log 2>&1 &
  GW_PID=$!
  for _ in $(seq 1 100); do
    if curl -s -o /dev/null "http://127.0.0.1:18080/"; then
      return 0
    fi
    sleep 0.1
  done
  echo "gateway did not start for $cfg; log:" >&2
  cat /tmp/bench-gateway.log >&2 || true
  return 1
}

stop_all() {
  local pids=()
  [ -n "$GW_PID" ] && pids+=("$GW_PID")
  [ -n "$SINK_PID" ] && pids+=("$SINK_PID")
  [ -n "$BACKEND_PID" ] && pids+=("$BACKEND_PID")
  GW_PID=""
  SINK_PID=""
  BACKEND_PID=""
  if [ ${#pids[@]} -eq 0 ]; then
    return 0
  fi
  kill "${pids[@]}" 2>/dev/null || true
  # Гейтвей останавливает плагины до 3с (StopTimeout), поэтому ждём реального
  # выхода, а не фиксированные 0.5с — иначе процессы переживают скрипт.
  for _ in $(seq 1 50); do
    local alive=""
    for p in "${pids[@]}"; do
      if kill -0 "$p" 2>/dev/null; then
        alive="$p"
      fi
    done
    if [ -z "$alive" ]; then
      break
    fi
    sleep 0.1
  done
  for p in "${pids[@]}"; do
    if kill -0 "$p" 2>/dev/null; then
      kill -KILL "$p" 2>/dev/null || true
    fi
  done
  wait "${pids[@]}" 2>/dev/null || true
}

# gen_jwt_token — HS256-токен с секретом JWT_SECRET (совпадает с secret_key
# в configs/*jwt*.yaml). Нужен потому, что wrk не шлёт Authorization: без
# токена JWT-сценарии мерят ветку 401, а не hot-path валидации/проксирования.
gen_jwt_token() {
  JWT_SECRET="${JWT_SECRET:-benchsecret}" python3 - <<'PY'
import base64
import hashlib
import hmac
import json
import os
import time


def b64(raw):
    return base64.urlsafe_b64encode(raw).rstrip(b"=")


header = b64(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
payload = b64(json.dumps({
    "id": "42",
    "email": "a@b.c",
    "roles": ["user"],
    "exp": int(time.time()) + 86400,
}, separators=(",", ":")).encode())
signing_input = header + b"." + payload
signature = b64(hmac.new(os.environ["JWT_SECRET"].encode(), signing_input, hashlib.sha256).digest())
print((signing_input + b"." + signature).decode())
PY
}

# init_csv <name> — заголовок CSV. Колонка conns разделяет прогоны c50/c300
# в одном файле (иначе второй прогон затирал бы первый).
init_csv() {
  echo "conns,reqs,p50,p99" > "$RESULTS/$1.csv"
}

# wrk_req <scenario> <connections> <threads> <duration> <runs> [out_name] — N
# прогонов, дописывает строки "conns,reqs,p50,p99" в CSV. Для JWT-сценариев
# добавляет заголовок Authorization: Bearer <JWT_TOKEN>.
wrk_req() {
  local scenario="$1" conns="$2" threads="$3" dur="$4" runs="$5"
  local out="$RESULTS/${6:-$scenario}.csv"
  local hdr=()
  case "$scenario" in
    *jwt*) hdr=(-H "Authorization: Bearer ${JWT_TOKEN:-}") ;;
  esac
  for _ in $(seq 1 "$runs"); do
    local line reqs p50 p99
    line=$(wrk -t"$threads" -c"$conns" -d"$dur" --latency ${hdr[@]+"${hdr[@]}"} "http://127.0.0.1:18080/" 2>/dev/null |
      awk '
        /Requests\/sec:/ { r=$2 }
        /^[[:space:]]*50%/ { p50=$2 }
        /^[[:space:]]*99%/ { p99=$2 }
        END { printf "%s,%s,%s\n", r, p50, p99 }
      ')
    IFS=, read -r reqs p50 p99 <<< "$line"
    if [ -z "$reqs" ] || [ -z "$p50" ] || [ -z "$p99" ]; then
      echo "wrk parse failed for $scenario (c$conns): got '$line'" >&2
    fi
    echo "$conns,$line" >> "$out"
  done
  echo "== $scenario (c$conns, $runs runs) =="
  awk -F, -v c="$conns" '$1==c && $2!="" {s+=$2; n++} END {if (n>0) printf "  avg req/s: %.0f\n", s/n}' "$out"
}
