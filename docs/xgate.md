# xgate — сборка шлюза с compile-time модулями

`xgate` — аналог `xcaddy` для api-gateway. Он собирает бинарник с выбранными
Go-модулями: генерирует `main.go` с blank-импортами, разрешает зависимости и
запускает `go build`. Рантайм-плагинов нет — на горячем пути нулевой overhead.

## Сборка самого xgate

```bash
go build -o bin/xgate ./cmd/xgate
```

Обычный шлюз без плагинов собирается как раньше:

```bash
go build -o bin/api-gateway ./cmd/gateway
```

## Использование

```bash
xgate build \
  --src . \
  --with code.sarnas.ru/api-gateway/plugins/dns/cloudflare \
  --with github.com/acme/gateway-auth-oidc=../gateway-auth-oidc \
  --output ./api-gateway-custom
```

В репозитории уже есть модули: `cloudflare`, `desec`, `digitalocean`, `route53`.

```yaml
tls:
  dns_provider: "cloudflare"
  dns_config:
    api_token: "${CF_API_TOKEN}"
```

Параметры остальных: `desec` — `token`/`DESEC_TOKEN`; `digitalocean` — `api_token`/`DO_API_TOKEN`; `route53` — `region`, `profile`, `access_key_id`, `secret_access_key`, `session_token`, `max_retries` (пустые значения берутся из AWS env).

Флаги:

| Флаг | Смысл |
|---|---|
| `--src` | корень исходников шлюза (по умолчанию `.`) |
| `--output` | путь к бинарнику |
| `--with module[@version][=replacement]` | добавить модуль; можно повторять |
| `--tags` | дополнительные build tags |
| `--keep` | оставить временный каталог сборки |

`--with` без версии берёт latest; `=../path` подставляет локальный форк.
Сборка идёт во временном Go-модуле с `replace` на локальный исходник шлюза,
поэтому `go.mod` рабочего репозитория не меняется.

### Манифест и lock

Вместо десятка `--with` можно держать `xgate.yaml` (пример — `xgate.example.yaml`):

```yaml
gateway:
  source: .
  output: ./bin/api-gateway-custom
modules:
  - path: code.sarnas.ru/api-gateway/plugins/dns/cloudflare
  - path: github.com/acme/gateway-module
    version: v1.0.0
    replacement: ../gateway-module
```

```bash
xgate build --manifest xgate.yaml
xgate list --manifest xgate.yaml
```

После сборки рядом пишется `xgate.lock` с `source_sha` и выбранными модулями;
`--lock` меняет путь, `--no-lock` отключает запись. `xgate list` показывает
манифест и, если lock существует, зафиксированные версии.

## Как написать модуль

Модуль — обычный Go-пакет, который регистрируется в `init()`:

```go
package mymodule

import "code.sarnas.ru/api-gateway/pkg/plugin"

type module struct{}

func (module) Name() string { return "my.module" }

func init() { plugin.Register(module{}) }
```

Дальше модуль реализует нужный интерфейс xgate (DNS-провайдер, auth-коннектор,
discovery, HTTP-middleware). Интерфейсы живут в `pkg/plugin`; ядро
проксирования, JWT, RBAC, rate limit и аудит остаются встроенными.

## Совместимость с Caddy

Caddy-модули **не подключаются напрямую**: они завязаны на `caddy.Context`,
`caddyhttp` и lifecycle Caddy. Вместо этого xgate использует нижние слои:

- DNS — `libdns`-провайдеры (Cloudflare, Route53, reg.ru и т.д.);
- сертификаты — `certmagic`;
- HTTP-middleware — `http.Handler`/`func(http.Handler) http.Handler`;
- метрики/трейсинг — Prometheus/OpenTelemetry.

Caddy-only модуль при необходимости портируется в xgate-модуль или выносится
в отдельный Caddy-фронт.

Стратегия экосистемы и интерфейсы — [`plugin-ecosystem.md`](plugin-ecosystem.md).
