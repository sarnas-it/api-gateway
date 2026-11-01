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
  --with code.sarnas.ru/xgate-modules/dns-cloudflare=./modules/dns/cloudflare \
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
  - path: code.sarnas.ru/xgate-modules/dns-cloudflare
    replacement: ./modules/dns/cloudflare
  - path: github.com/acme/gateway-module
    version: v1.0.0
    replacement: ../gateway-module
```

```bash
xgate build --manifest xgate.yaml
xgate list --manifest xgate.yaml
```

После сборки рядом пишется `xgate.lock` с `source_sha`, выбранными модулями и
полным транзитивным графом (`dependencies` из `go list -m all`); `--lock`
меняет путь, `--no-lock` отключает запись. `xgate list` показывает манифест,
зафиксированные версии и число зависимостей.

## Как написать модуль

Модуль — обычный Go-пакет, который регистрируется в `init()`:

```go
package mymodule

import "code.sarnas.ru/api-gateway/pkg/plugin"

type module struct{ header string }

func (*module) Name() string { return "my.module" }

func (m *module) Configure(cfg map[string]string) error {
	m.header = cfg["header"]
	return nil
}

func init() { plugin.Register(&module{}) }
```

```yaml
plugins:
  my.module:
    header: "X-Tenant"
```

Дальше модуль реализует нужный интерфейс xgate (DNS-провайдер, auth-коннектор,
discovery, HTTP-middleware). Интерфейсы живут в `pkg/plugin`; ядро
проксирования, JWT, RBAC, rate limit и аудит остаются встроенными.

Модули с `Wrap(http.Handler) http.Handler` применяются к публичному обработчику
при старте: цепочка оборачивает HTTP-, HTTPS- и HTTP/3-листенеры. Модуль,
зарегистрированный первым, оказывается снаружи. Секции `plugins:` читаются один
раз перед применением цепочки, поэтому порядок такой: `Configure` → `Wrap` →
листенеры.

### Auth-коннектор

`plugin.AuthConnector` — аутентификатор, который маршрут выбирает через
`auth.connector`. Коннектор возвращает claims (`id`, опционально `email` и
`roles`), пустой результат означает «identity нет» и запрос получает 401:

```go
func (m *module) Authenticate(r *http.Request) (map[string]any, error) {
	key := r.Header.Get("X-API-Key")
	if key == "" || !m.valid(key) {
		return nil, nil
	}
	return map[string]any{"id": "svc-a", "roles": []string{"admin"}}, nil
}
```

```yaml
routing:
  rules:
    - path_prefix: "/api/keys"
      target_name: "api"
      auth:
        connector: "my.module"   # имя из Name()
        roles: ["admin"]
```

Готовый пример — `modules/apikey` (статичные API-ключи из `plugins.apikey.keys`).

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
Индекс готовых модулей и шаблон — [`modules.md`](modules.md), `modules/example`.
