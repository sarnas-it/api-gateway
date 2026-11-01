<!-- generated — do not edit here; source: frontend/app/docs/api-gateway/scenarios/content.md -->

# Сценарии

Готовые рецепты в формате «задача → конфиг → результат → оговорки». Фрагменты рассчитаны на вставку в `config.local.yaml`; полное описание полей — в [справочнике](/docs/api-gateway/configuration).

## 1. Публичный сервис плюс JWT на выбранных маршрутах

**Задача.** Открыть логин и регистрацию без токена, а остальной API закрыть JWT.

```yaml
jwt:
  secret_key: "${JWT_SECRET}"
  algorithm: "HS256"
  validate_exp: true

targets:
  - name: "auth-api"
    url: "http://127.0.0.1:9001"
  - name: "client-api"
    url: "http://127.0.0.1:9002"

routing:
  rules:
    - path_prefix: "/api/v1/auth/login"
      target_name: "auth-api"
      methods: ["POST"]
      auth:
        required: false
    - path_prefix: "/api/v1/auth"
      target_name: "auth-api"
      auth:
        required: true
    - path_prefix: "/api/v1/client"
      target_name: "client-api"
      auth:
        required: true
```

**Результат.** `/api/v1/auth/login` доступен анонимно; остальные маршруты требуют валидный токен в `Authorization: Bearer` или cookie `cml_access`. Невалидный токен на публичном маршруте не блокирует запрос.

**Оговорки.** Правило выбирается по самому длинному совпадающему префиксу, поэтому специфичный `/login` должен идти отдельным правилом. Предъявленный токен проверяется всегда, даже если маршрут не требует auth.

## 2. RBAC из claims токена на маршрут

**Задача.** Пускать в админский API пользователей с ролью `admin` или `support`, но только если у них есть обе роли `staff` и `mfa`.

```yaml
jwt:
  secret_key: "${JWT_SECRET}"
  claim_mappings: ["id", "roles"]

routing:
  rules:
    - path_prefix: "/api/v1/admin"
      target_name: "admin-api"
      auth:
        required: true
        roles: ["admin", "support"]   # достаточно любой из двух
        roles_all: ["staff", "mfa"]   # и при этом нужны обе
```

**Результат.** Запрос проходит, если в claim `roles` есть `admin` или `support` **и** одновременно есть `staff` и `mfa`. Claim `roles` может быть массивом строк или строкой.

**Оговорки.** `roles` — это «любая из», `roles_all` — «все из»; если заданы оба, условия объединяются через И. Если claim `roles` отсутствует или не является строкой либо массивом строк — 401.

## 3. Аудит изменений: webhook только на мутирующие методы

**Задача.** Отправлять событие в аудит на каждый `POST`, `PUT`, `PATCH`, `DELETE`.

```yaml
webhooks:
  - name: "audit-mutations"
    transport: "webhook"
    webhook_url: "https://audit.example/events"
    trigger: "on_response"
    methods: ["POST", "PUT", "PATCH", "DELETE"]
```

**Результат.** На каждый ответ мутирующего запроса уходит POST с JSON-событием (`method`, `path`, `user_id`, `status_code`, `changes`).

**Оговорки.** `changes` заполняется телом запроса, только если это JSON-объект короче 64 KiB; чтение тела ограничено `server.max_request_body_size`. Для объёмного аудита добавьте батчинг (сценарий 4).

## 4. Аудит админских маршрутов: и чтение, и запись

**Задача.** Собрать полный аудит админки — включая чтения, а не только изменения. Это флагманский сценарий: видно, кто что смотрел и кто что изменил.

```yaml
targets:
  - name: "admin-api"
    url: "http://127.0.0.1:9003"

routing:
  rules:
    - path_prefix: "/api/v1/admin"
      target_name: "admin-api"
      auth:
        required: true
        roles: ["admin"]

webhooks:
  - name: "admin-audit"
    transport: "webhook"
    webhook_url: "https://audit.example/admin"
    trigger: "on_response"
    include_request_body: true
    include_response_body: true
    batch_size: 1000
    flush_interval: 100ms
```

**Результат.** Любой запрос к `/api/v1/admin` (в том числе `GET`) порождает событие с телом запроса и телом JSON-ответа. События копятся пачками и уходят одним POST `{"count":N,"events":[...]}`.

**Оговорки.** Тело ответа собирается только для JSON и ограничено 64 KiB. Батчинг включает backpressure: при переполнении очереди (8192 события) постановка блокируется, потерь нет, но запросы замедляются. Тело запроса попадает в `changes`, только если это JSON.

## 5. Permission-сервис с кешем

**Задача.** Подмешивать в запрос эффективные разрешения пользователя, не дёргая сервис на каждый запрос.

```yaml
jwt:
  secret_key: "${JWT_SECRET}"
  claim_mappings: ["id"]

permissions:
  enabled: true
  service_url: "http://permissions:8080"
  method: "GET"                                          # по умолчанию
  path: "/api/v1/users/{user_id}/effective-permissions"  # по умолчанию; {user_id} обязателен
  api_key_header: "X-API-Key"                            # по умолчанию
  cache_ttl: 300s
  header_name: "X-User-Permissions"
  api_key: "${PERMISSIONS_KEY}"
  invalidate_token: "${INVALIDATE_TOKEN}"
```

**Что должен реализовать permission-сервис.** Эндпоинт, который шлюз вызывает при промахе кеша. Метод, путь и заголовок ключа настраиваются полями `permissions.method`, `permissions.path` и `permissions.api_key_header`; по умолчанию:

```
GET /api/v1/users/{user_id}/effective-permissions
X-API-Key: ${PERMISSIONS_KEY}     # отправляется, если permissions.api_key задан
```

Плейсхолдер `{user_id}` в `permissions.path` обязателен. Если эти поля не заданы, поведение прежнее — существующий permission-сервис менять не нужно.

Ответ — HTTP 200 с JSON-объектом; шлюз использует только поле `permissions`, остальные опциональны:

```json
{
  "user_id": 42,
  "permissions": ["orders:read", "orders:write", "reports:view"],
  "inherited_from_role": ["orders:read", "reports:view"],
  "direct_allowed": ["orders:write"],
  "direct_denied": ["admin:all"]
}
```

Любой не-200 (401/403/404/5xx) или невалидный JSON считается ошибкой: заголовок не выставится, но запрос до бэкенда дойдёт.

**Результат.** Бэкенд получает `X-User-Permissions` со списком разрешений через запятую, но только если список непуст. Ответы кешируются по `user_id` на `cache_ttl`. Инвалидация — `POST /_cache/permissions/invalidate` с `X-Invalidate-Token` (опционально `?user_id=…`).

**Оговорки.** Модуль требует claim `id` в `jwt.claim_mappings`, приводимый к целому (JSON-число, `int` или числовая строка); иначе заголовок не выставляется, а запрос идёт дальше без 401. При ошибке сервиса заголовок тоже не выставляется. Эндпоинт инвалидации обрабатывается до Basic Auth, поэтому `basic_auth` его не защищает — доступ ограничен только `X-Invalidate-Token`. После смены прав пользователя вызывайте инвалидацию, иначе до истечения `cache_ttl` будет отдаваться устаревший набор.

## 6. Рейт-лимитинг на маршрут и глобальный

**Задача.** Ограничить логин жёстче остального API и задать общий потолок процесса.

```yaml
routing:
  global_limit:
    requests_per_second: 1000
    burst: 2000
  rules:
    - path_prefix: "/api/v1/auth/login"
      target_name: "auth-api"
      methods: ["POST"]
      rate_limit:
        requests_per_second: 5
        burst: 10
    - path_prefix: "/api/v1/client"
      target_name: "client-api"
      rate_limit:
        requests_per_second: 100
        burst: 200
```

**Результат.** Логин ограничен 5 rps на IP, клиентский API — 100 rps на IP, весь процесс — 1000 rps.

**Оговорки.** Per-route лимит считается на IP клиента и отдаёт 429 без `Retry-After`; глобальный — на процесс и отдаёт `Retry-After: 1`. IP берётся из последнего значения `X-Forwarded-For`, иначе из `RemoteAddr`.

## 7. CORS для SPA

**Задача.** Разрешить браузерному приложению на конкретном домене обращаться к API с credentials.

```yaml
headers:
  cors:
    enabled: true
    allowed_origins: ["https://app.example"]
    allowed_methods: ["GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"]
    allowed_headers: ["Content-Type", "Authorization", "X-Request-ID"]
    expose_headers: ["X-Request-ID"]
    max_age: 86400
```

**Результат.** Запросы с origin `https://app.example` получают `Access-Control-Allow-Origin` с этим origin и `Allow-Credentials: true`; preflight `OPTIONS` отвечает 200.

**Оговорки.** При `allowed_origins: ["*"]` возвращается `*` **без** credentials — браузер не отправит куки. Точный origin в allowlist обязателен, если нужны credentials. Любой `OPTIONS` обрабатывается как preflight.

## 8. Basic Auth для внутренних путей

**Задача.** Закрыть служебные эндпоинты логином и паролем.

```yaml
basic_auth:
  enabled: true
  username: "internal"
  password: "${BASIC_PASS}"
  skip_paths: ["/health"]
```

**Результат.** Все запросы требуют Basic Auth, кроме `/health` и его подпутей. Сравнение — constant-time.

**Оговорки.** Basic Auth стоит во внешнем слое и защищает также `/metrics`, статику и эндпоинт инвалидации кеша. Пароль хранится в открытом виде в конфиге — подставляйте из окружения.

## 9. Статика и SPA fallback

**Задача.** Отдавать собранный SPA с fallback на `index.html`, а `/api` проксировать.

```yaml
static:
  skip_prefixes: ["/api"]
  apps:
    - path_prefix: "/"
      root_dir: "/app/static"
      index_file: "index.html"
      max_age: 3600

targets:
  - name: "api"
    url: "http://127.0.0.1:9001"
    path_prefix: "/api"
```

**Результат.** Реальные файлы отдаются напрямую, несуществующие пути — `index.html`; `/api/*` уходит в прокси.

**Оговорки.** Статика обрабатывается до проксирования. Если маршрут совпал с правилом, у которого задан `Host`, статика пропускается — так host-based роутинг не перекрывается файлами. Плоские `.html` (`about.html` для `/about`) поддерживаются.

## 10. Автоматический TLS через ACME

**Задача.** Получить сертификаты Let's Encrypt и редиректить HTTP на HTTPS.

```yaml
tls:
  enabled: true
  port: 443
  http_port: 80
  domains:
    - "api.example.com"
    - "www.example.com"
  email: "admin@example.com"
  cache_dir: "/var/lib/api-gateway/certs"
  staging: false
  redirect_http: true
```

**Результат.** HTTPS-сервер на 443, HTTP на 80 обслуживает ACME-challenge и редиректит на HTTPS. `/health` и `/ready` на HTTP отвечают 200.

**Оговорки.** Сначала проверяйте на `staging: true` — это реальный staging CA с отдельным кешем. `directory_url` перекрывает выбор CA по `staging`. Домен и email обязательны при включении. Wildcard-домены (`*.example.com`) не поддерживаются: `autocert` использует HTTP-01/TLS-ALPN-01, для wildcard нужен DNS-01 (`certmagic`).

## 11. Service discovery по labels

**Задача.** Поднимать и убирать бэкенды без правки конфига шлюза.

```yaml
discovery:
  enabled: true
  provider: docker
  host: "unix:///var/run/docker.sock"
  label_prefix: "gateway"
  network: ""
  debounce: 500ms
  resync_interval: 5m
  state_file: "/var/lib/api-gateway/discovery-state.json"
```

```yaml
services:
  blog:
    image: ghcr.io/sarnas-it/blog:latest
    labels:
      gateway.enable: "true"
      gateway.port: "8085"
      gateway.health: "/health"
      gateway.router.public.path_prefix: "/api/blog"
      gateway.router.public.auth.required: "false"
      gateway.router.admin.path_prefix: "/api/admin/blog"
      gateway.router.admin.auth.required: "true"
      gateway.router.admin.auth.roles: "admin"
```

**Результат.** Шлюз сам строит таргеты и правила из labels. Обнаруженное дополняет статику; при конфликте имени побеждает статика.

**Оговорки.** Socket монтируется read-only; клиент делает только GET. Маркер — `gateway.enable: "true"` (`true`/`1`/`yes`). Для рестарта при недоступном Docker каталог `state_file` должен быть writable. В labels не храните секреты — они видны через `docker inspect`.

## 12. Взвешенная балансировка таргетов

**Задача.** Разделить трафик между канареечной и стабильной версиями сервиса 1:4.

```yaml
targets:
  - name: "api-stable"
    url: "http://127.0.0.1:9001"
    weight: 4
  - name: "api-canary"
    url: "http://127.0.0.1:9002"
    weight: 1

routing:
  rules:
    - path_prefix: "/api"
      target_name: "api-stable"
    - path_prefix: "/api"
      target_name: "api-canary"
```

**Результат.** Правила совпадают по `(host, path_prefix, methods)` и образуют пул; запросы идут взвешенным round-robin по здоровым таргетам в пропорции 4:1.

**Оговорки.** Все правила пула обязаны совпадать по `auth`, `strip_path` и `rate_limit`. `weight` не задан — это 1; `weight: 0` или отрицательный **исключает** таргет. Нездоровые таргеты пропускаются; если здоровых нет — 503.

## 13. Проброс claims в заголовки

**Задача.** Передать бэкенду идентификатор, email и роли пользователя отдельными заголовками.

```yaml
jwt:
  secret_key: "${JWT_SECRET}"
  claim_mappings: ["id", "email", "roles"]

headers:
  strip_authorization: true
  claim_to_header:
    id: "X-User-ID"
    email: "X-User-Email"
    roles: "X-User-Roles"
  add_headers:
    X-Gateway-Version: "1.0.0"
```

**Результат.** Бэкенд получает `X-User-ID`, `X-User-Email`, `X-User-Roles` из токена и `X-Gateway-Version`; `Authorization` удаляется.

**Оговорки.** Маппинг применяется только к извлечённым `claim_mappings`. Если `claim_mappings` пуст, по умолчанию извлекается `sub` и маппится в `X-User-ID`. HMAC-подпись (`headers.sign_header`) требует `permissions.api_key` и claim `id`.

## 14. Health checks и circuit breaker

**Задача.** Не отправлять трафик на упавший бэкенд и разрывать цепь на ошибках транспорта.

```yaml
application:
  health_check: true
  circuit_breaker: true

targets:
  - name: "api-a"
    url: "http://127.0.0.1:9001"
    health_check: "http://127.0.0.1:9001/health"
    weight: 1
  - name: "api-b"
    url: "http://127.0.0.1:9002"
    health_check: "http://127.0.0.1:9002/health"
    weight: 1

routing:
  rules:
    - path_prefix: "/api"
      target_name: "api-a"
    - path_prefix: "/api"
      target_name: "api-b"
```

**Результат.** Health-пробы идут раз в 30 секунд с таймаутом 5 секунд. Нездоровые таргеты исключаются из пула. Circuit breaker открывается после 3 ошибок транспорта, через 30 секунд пропускает один пробный запрос.

**Оговорки.** Любой HTTP-ответ, включая 5xx, считается признаком исправного транспорта — цепь рвут только ошибки соединения. `health_check` у таргета работает, только если `application.health_check: true`. Если здоровых таргетов в пуле нет — 503.

## 15. Метрики и трейсинг

**Задача.** Снимать метрики шлюза и отправлять трейсы в OTLP-коллектор.

```yaml
application:
  metrics_enabled: true
  metrics_allowed_ips: ["10.0.0.5"]
```

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318 \
OTEL_EXPORTER_OTLP_INSECURE=true \
./api-gateway -config /etc/proxy/config.yaml
```

**Результат.** `/metrics` отдаёт счётчики в формате `expvar` (`gateway_requests_total`, `gateway_request_duration_ms`, `gateway_rate_limit_denials_total`, `gateway_target_up`, `gateway_active_requests`). Трейсы уходят по OTLP HTTP; заголовок `traceparent` пропагируется в бэкенд.

**Оговорки.** Метрики выключены по умолчанию — их сбор стоит работы на каждый запрос. При заданном `metrics_allowed_ips` `/metrics` доступен только этим IP. Без `OTEL_EXPORTER_OTLP_ENDPOINT` трейсинг выключен. pprof включается отдельно переменной `PPROF_ADDR`.
