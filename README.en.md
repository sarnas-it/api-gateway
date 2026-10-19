# API Gateway

[![CI](https://github.com/sarnas-it/api-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/sarnas-it/api-gateway/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**A small API gateway in Go with built-in JWT auth, RBAC and rate limiting.**
One binary (or a `scratch` image), one YAML config, no plugins and no separate
auth service. It runs in production at [sarnas.ru](https://sarnas.ru).

Русская версия: [README.md](README.md)

## Benchmark

Proxy-only, one route to an instant backend, 2 CPU cores and 768 MiB per gateway,
`wrk` with 300 connections. Best of five runs; run-to-run noise is about ±10 %,
so compare relative gaps, not absolute numbers.

| | `api-gateway` | Others |
|---|---:|---:|
| Proxying, c300 | **24,068 req/s** | Nginx 27,216 · Kong 25,322 · Envoy 25,272 · Traefik 24,811 |
| Cost of built-in JWT | **−15.1 %** | Envoy `jwt_authn` −19.7 %, Kong `jwt` −28.9 % |
| External synchronous auth | n/a | Nginx `auth_request` −89.5 %, Traefik `forwardAuth` −93.8 % |
| Peak memory, c300 | 121.5 MiB | 15.8 (Nginx) – 314.1 (Kong) MiB |

What this does and does not show: `api-gateway` is about 12 % behind Nginx on
raw proxying and within 3–5 % of Traefik, Envoy and Kong. Validating a JWT
in-process is cheap everywhere; the expensive pattern is a network hop to an
auth service on every request. The backend answers instantly and there is no
TLS, so the numbers measure gateway overhead, not a real workload.

Full methodology, configs and reproduction scripts:
[benchmarks/2026-09-gateway-comparison](benchmarks/2026-09-gateway-comparison/README.md)
(the report itself is in Russian; configs and scripts are language-neutral).

## Features

- Routing by path prefix and by `Host` (including wildcards like `*.example.com`)
- Per-route JWT auth and role checks (`auth.roles` = any of, `auth.roles_all` = all of)
- Per-route token-bucket rate limiting plus a global limit
- Health checks and a circuit breaker that trips on transport errors only
- Weighted round-robin over healthy targets that share a route
- Docker/Podman service discovery by container labels, Traefik-style
- CORS allowlist, Basic Auth, JWT claims forwarded as headers
- Static files / SPA serving, webhooks and NATS events
- Graceful shutdown, structured logs (Zap), metrics, OpenTelemetry tracing
- `-check` / `-strict` flags to validate a config in CI before deploy

## Quick start

```bash
# 1. Any backend on :9000
docker run -d --rm --network host --name echo traefik/whoami --port 9000

# 2. Config: one target, one route
cat > config.yaml <<'YAML'
application:
  env: "dev"
server:
  port: 8080
targets:
  - name: "echo"
    url: "http://127.0.0.1:9000"
    timeout: 5s
routing:
  rules:
    - path_prefix: "/"
      target_name: "echo"
jwt:
  secret_key: "change-me-to-a-secure-secret-at-least-256-bits-long"
  required: false
YAML

# 3. Gateway
git clone https://github.com/sarnas-it/api-gateway && cd api-gateway
docker build -t api-gateway .
docker run -d --rm --network host --name gw \
  -v $(pwd)/../config.yaml:/etc/proxy/config.yaml:ro \
  api-gateway

curl -i localhost:8080/
```

Docs (Russian): [quick start](docs/api-gateway-quickstart.md),
[configuration reference](docs/api-gateway-configuration.md),
[scenarios](docs/api-gateway-scenarios.md). The fully commented example config is
[`config.local.example.yaml`](config.local.example.yaml).

## When to use it, and when not to

Good fit: a small gateway in front of a handful of services, with JWT and roles
per route, rate limiting, a circuit breaker and label-based discovery.

Not a fit: Kubernetes Ingress / Gateway API, a plugin ecosystem like Kong's,
automatic TLS certificates, gRPC-specific features. Use Traefik, Envoy or Kong
for those.

## Contributing

Issues and PRs are welcome, see [CONTRIBUTING.md](CONTRIBUTING.md).
Tasks marked `good first issue` are a good place to start.

## License

MIT
