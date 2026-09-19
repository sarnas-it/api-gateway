#!/usr/bin/env bash
# Smoke-тест plugin-режима: собирает плагины, запускает гейтвей с .so-JWT и
# прогоняет валидный/невалидный токен.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> build plugins"
make plugin-build
make plugin-so-build

echo "==> start fake backend on :19901"
python3 - <<'PY' &
from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers(); self.wfile.write(b"ok")
    def log_message(self, *a): pass
HTTPServer(("127.0.0.1", 19901), H).serve_forever()
PY
BACKEND_PID=$!
trap "kill $BACKEND_PID 2>/dev/null || true" EXIT
sleep 0.5

echo "==> start gateway (jwt .so)"
./bin/api-gateway -config benchmarks/2026-09-plugins-experiment/configs/smoke-jwt-so.yaml &
GW_PID=$!
trap "kill $GW_PID $BACKEND_PID 2>/dev/null || true" EXIT
for i in $(seq 1 50); do
  if curl -s -o /dev/null http://127.0.0.1:18080/; then break; fi
  sleep 0.2
done

TOKEN=$(python3 - <<'PY'
import hmac, hashlib, base64, json, time
def b64(b): return base64.urlsafe_b64encode(b).rstrip(b"=")
h = b64(json.dumps({"alg":"HS256","typ":"JWT"},separators=(",",":")).encode())
p = b64(json.dumps({"id":"42","exp":int(time.time())+3600},separators=(",",":")).encode())
sig = b64(hmac.new(b"benchsecret", h+b"."+p, hashlib.sha256).digest())
print((h+b"."+p+b"."+sig).decode())
PY
)

echo "==> valid token"
code=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $TOKEN" http://127.0.0.1:18080/)
test "$code" = "200" || { echo "FAIL valid token: got $code"; exit 1; }

echo "==> invalid token"
code=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer garbage" http://127.0.0.1:18080/)
test "$code" = "401" || { echo "FAIL invalid token: got $code"; exit 1; }

echo "SMOKE OK"
