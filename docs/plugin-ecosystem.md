# Экосистема xgate: как получить широкий охват без Caddy ABI

Цель — не копировать Caddy-модули, а переиспользовать их **нижние слои**. Это
даёт большую часть экосистемы без `caddy.Context`, Caddyfile и lifecycle Caddy.

## Слои

| Слой | Что используем | Что получаем |
|---|---|---|
| DNS-01 | `libdns` + `certmagic` | Cloudflare, Route53, reg.ru, deSEC, GCP, Azure и десятки провайдеров |
| Сертификаты | `certmagic` | ACME, DNS-01, storage, renewal |
| HTTP-middleware | `http.Handler` / `func(http.Handler) http.Handler` | любые Go-middleware, JWT, CORS, compression |
| Метрики/трейсинг | Prometheus + OpenTelemetry | стандартные коллекторы и дашборды |
| Конфиг | YAML + env | без Caddyfile/JSON-адаптера |

## Интерфейсы xgate

- `plugin.Module` — имя и регистрация.
- `plugin.DNSProvider` — `New(map[string]string) (libdns.RecordAppender, libdns.RecordDeleter, error)`.
- `plugin.HTTPMiddleware` — `Wrap(http.Handler) http.Handler`.
- `plugin.AuthConnector` — `Authenticate(*http.Request) (map[string]any, error)`;
  включается на маршруте через `auth.connector: <name>`.
- Дальше: `DiscoveryProvider`, `Storage`.

DNS-модули: `modules/dns/{cloudflare,desec,digitalocean,route53}` — отдельные Go-модули.
Auth-коннектор: `modules/apikey` (статичные ключи, роли из конфига).
`tls.dns_provider: <name>` работает только в бинарнике, собранном с этим
модулем через `xgate --with code.sarnas.ru/xgate-modules/dns-<name>=./modules/dns/<name>`.

DNS-провайдер Caddy (`dns.providers.*`) почти всегда обёртка над `libdns`:
берём `github.com/libdns/<provider>` и пишем тонкий xgate-модуль, который его
регистрирует. Caddy-код не импортируется.

## Что не совместимо

- Caddy HTTP handlers (`caddyhttp.MiddlewareHandler`) — нужен адаптер или порт.
- Caddyfile-директивы — у нас YAML; полный адаптер не планируется.
- Caddy apps (L4, DNS-сервер) — другая модель, вне xgate.
- JWT/RBAC/rate-limit плагины Caddy — портируются в xgate-модули, а не
  подключаются как есть.

## Правила, чтобы не повторить xcaddy-боли

- Версии модулей фиксируются в `go.mod`/lock; xgate не тянет latest вслепую.
- Плагины не допускаются на горячий путь auth/проксирования без ревью.
- Каждый модуль проходит `xgate build` + `go test` в CI.
- Caddy-only модуль — отдельное решение: порт, адаптер или Caddy-фронт.
