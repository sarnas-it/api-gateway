# API Gateway

[![CI](https://github.com/sarnas-it/api-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/sarnas-it/api-gateway/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8.svg)](go.mod)

**Лёгкий API-шлюз на Go со встроенными JWT, RBAC и rate limiting.** Один бинарник
(или образ `scratch`), один YAML-конфиг, никаких плагинов и отдельного
auth-сервиса. Работает в проде на [sarnas.ru](https://sarnas.ru).

English version: [README.en.md](README.en.md)

| Что измерили | `api-gateway` | Nginx / Traefik / Envoy / Kong |
|---|---:|---:|
| Проксирование, c300 | **24 068 req/s** | 24 811 – 27 216 req/s |
| Цена встроенного JWT | **−15.1 %** | Envoy −19.7 %, Kong −28.9 % |
| Внешняя синхронная аутентификация | — | Nginx `auth_request` −89.5 %, Traefik `forwardAuth` −93.8 % |
| Пик памяти, c300 | 121.5 MiB | 15.8 – 314.1 MiB |

Методология, конфиги и скрипты воспроизведения —
[в отчёте о бенчмарке](benchmarks/2026-09-gateway-comparison/README.md).
Числа получены на проксировании мгновенного backend без TLS, разброс между
прогонами около ±10 %: сравнивайте относительные разрывы, не абсолютные значения.

## Запуск за минуту

```bash
# 1. Любой backend на :9000
docker run -d --rm --network host --name echo traefik/whoami --port 9000

# 2. Конфиг: один таргет, один маршрут
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
  required: false   # JWT включается per-route, см. docs/api-gateway-scenarios.md
YAML

# 3. Шлюз
git clone https://github.com/sarnas-it/api-gateway && cd api-gateway
docker build -t api-gateway .
docker run -d --rm --network host --name gw \
  -v $(pwd)/../config.yaml:/etc/proxy/config.yaml:ro \
  api-gateway

curl -i localhost:8080/   # ответ whoami, проксированный шлюзом
```

Разбор запуска, JWT и роли — в
[быстром старте](docs/api-gateway-quickstart.md).

## Когда он подойдёт, а когда нет

Подойдёт, если нужен небольшой шлюз перед несколькими сервисами: JWT и роли на
уровне маршрута, rate limiting, circuit breaker, взвешенная балансировка и
service discovery по labels Docker/Podman.

Не подойдёт, если вам нужен Kubernetes Ingress/Gateway API, плагинная экосистема
уровня Kong, TLS-терминация с автоматическими сертификатами или gRPC-специфичные
функции. Для этого берите Traefik, Envoy или Kong.

## Возможности

- **Маршрутизация по префиксу пути** на несколько бэкенд-сервисов, с поддержкой маршрутизации по `Host` (в т.ч. wildcard-домены `*.example.com`)
- **JWT-аутентификация по маршруту** — часть маршрутов требует токен, часть публична
- **Ролевой доступ** — проверка ролей из claims JWT для конкретного маршрута: `auth.roles` — достаточно любой из перечисленных, `auth.roles_all` — нужны все
- **Rate limiting по маршруту** — token bucket на IP, плюс глобальный лимит
- **Health checks и circuit breaker** — автоматическая проверка доступности таргетов, разрыв цепи только на ошибках транспорта
- **Взвешенная балансировка** — таргеты, делящие один роут (`host`, `path_prefix`, `methods`), образуют пул и распределяются weighted round-robin по здоровым кандидатам; в пул входят как статические, так и обнаруженные таргеты
- **CORS** — настраиваемый allowlist источников, отдельное поведение для dev-режима
- **Basic Auth** — с constant-time сравнением хэшей, для служебных путей
- **Проброс claims в заголовки** — маппинг полей JWT в HTTP-заголовки для бэкендов
- **Раздача статики / SPA** — с fallback на `index.html` и поддержкой flat-HTML экспорта (Next.js)
- **Service discovery** — обнаружение бэкендов по labels контейнеров (Docker/Podman), в стиле Traefik
- **Вебхуки/NATS** — публикация событий `on_request`/`on_response`
- **Graceful shutdown**, структурированное логирование (Zap), метрики, трейсинг (OpenTelemetry)

## Документация

- [Быстрый старт](docs/api-gateway-quickstart.md) — минимальный конфиг, запуск локально и в Docker, проверка.
- [Конфигурация](docs/api-gateway-configuration.md) — полный справочник: все секции и поля, значения по умолчанию, примеры.
- [Сценарии](docs/api-gateway-scenarios.md) — типовые задачи: авторизация, RBAC, аудит, права, discovery, балансировка.

Онлайн-версия с навигацией: [sarnas.ru/docs/api-gateway](https://sarnas.ru/docs/api-gateway).

## Производительность и воспроизводимый бенчмарк

На проксировании «один роут → backend» (proxy-only, 300 соединений) `api-gateway`
показывает **24 068 req/s** — на **11.6 %** ниже Nginx (27 216 req/s) и в пределах
3–5 % от Traefik, Envoy и Kong. Встроенная JWT-аутентификация стоит около **15 %**
пропускной способности, тогда как внешние синхронные схемы Nginx `auth_request` и
Traefik `forwardAuth` — **89–94 %**. Пик памяти под c300 — 121.5 MiB.

Полный отчёт с методологией, таблицами, конфигами и скриптами воспроизведения:
[`benchmarks/2026-09-gateway-comparison/README.md`](benchmarks/2026-09-gateway-comparison/README.md).

## Быстрый старт

```bash
# Скопировать конфиг
cp config.local.example.yaml config.local.yaml
# Отредактировать config.local.yaml — указать свои сервисы и секрет для JWT

# Проверить конфиг, не запуская гейтвей
go run ./cmd/ -config config.local.yaml -check

# Запуск
go run ./cmd/ -config config.local.yaml
```

### Проверка конфигурации

Флаг `-check` загружает и валидирует конфиг, печатает предупреждения и ошибки и завершается с кодом `0` (валиден) или `1` (ошибка). Гейтвей при этом не запускается — удобно для CI и перед деплоем.

```bash
api-gateway -config /etc/proxy/config.yaml -check
# config OK: /etc/proxy/config.yaml
#   targets: 3, routing rules: 8, discovery: true, permissions: false
```

Флаг `-strict` дополнительно считает ошибкой предупреждения о неизвестных ключах:

```bash
api-gateway -config /etc/proxy/config.yaml -check -strict
```

В контейнере: `docker run --rm -v ./config.yaml:/etc/proxy/config.yaml ghcr.io/sarnas-it/api-gateway:latest -check`.

## Конфигурация

Полный справочник по всем полям — в [документации](docs/api-gateway-configuration.md); рабочий пример со всеми секциями — в [`config.local.example.yaml`](config.local.example.yaml).

### Пример маршрутов

| Путь | Таргет | Auth | Роли |
|---|---|---|---|
| `POST /api/v1/auth/login` | auth-api | Нет | — |
| `POST /api/v1/auth/register` | auth-api | Нет | — |
| `POST /api/v1/auth/refresh` | auth-api | Нет | — |
| `/api/v1/auth/*` | auth-api | Да | — |
| `/api/v1/client/*` | client-api | Да | user, admin |
| `/api/v1/admin/*` | admin-api | Да | admin |
| `/api/v1/storage/public/*` | storage-api | Нет | — |
| `/api/v1/storage/*` | storage-api | Да | — |

> Примечание: per-route `rate_limit` применяется к запросу. В более ранних
> сборках из-за ошибки в сопоставлении правила лимит не срабатывал — исправлено.

## Service discovery (Docker/Podman)

Гейтвей умеет находить бэкенды по labels контейнеров — как Docker-провайдер
Traefik, только без отдельного auth-сервиса и плагинного маркетплейса. Он
подключается к Docker/Podman API через unix socket, находит контейнеры с
маркером `gateway.enable=true`, строит из их labels таргеты и правила роутинга
и применяет их к уже работающему прокси. Обнаруженное **дополняет** статический
конфиг. При конфликте имени таргета побеждает статика (обнаруженный таргет и его
правила отбрасываются). Если обнаруженный сервис регистрирует тот же роут
`(host, path_prefix, methods)`, что и статика, но указывает на другой таргет, он
добавляется в общий пул, а не перекрывается статикой.

Таргет адресуется по имени сервиса (`http://<name>:<port>`), поэтому несколько
реплик одного сервиса раскидывает встроенный DNS Docker/Podman.

Гейтвей балансирует сам: если несколько правил маршрутизации совпадают по
`(host, path_prefix, methods)` и указывают на разные таргеты, они образуют один
пул, и запросы распределяются между здоровыми кандидатами взвешенным
round-robin. В пул входят как статические, так и обнаруженные таргеты — например,
статический `a` и обнаруженный `b` на одном роуте делят нагрузку. Вес задаётся
полем `weight` таргета (по умолчанию `1`); `weight: 0` или отрицательный
исключает таргет из пула.

Несколько реплик **одного** сервиса (одинаковое имя таргета) по-прежнему
раскидывает DNS: discovery дедуплицирует их в один таргет.

### Конфигурация

```yaml
discovery:
  enabled: true
  provider: docker            # docker | podman (один и тот же клиент)
  host: "unix:///var/run/docker.sock"
  api_version: "v1.41"        # "" = запросы без версии
  label_prefix: "gateway"
  service_name_labels:
    - "com.docker.compose.service"
    - "io.podman.compose.service"
  network: ""                 # учитывать только контейнеры этой сети
  debounce: 500ms             # дебаунс событий контейнеров
  resync_interval: 5m         # периодический ре-синк
  default_timeout: 30s
  state_file: /var/lib/api-gateway/discovery-state.json  # фолбэк ("" = выкл.)
```

| поле | по умолчанию | смысл |
|---|---|---|
| `enabled` | `false` | включить discovery |
| `provider` | `docker` | `docker` или `podman` |
| `host` | `unix:///var/run/docker.sock` | socket Docker/Podman API |
| `api_version` | `v1.41` | префикс версии API; `""` — без версии |
| `label_prefix` | `gateway` | префикс labels |
| `service_name_labels` | compose-сервис | цепочка фолбэков имени таргета |
| `network` | `""` | фильтр по сети (пусто — все) |
| `debounce` | `500ms` | задержка перед ре-синком по событиям |
| `resync_interval` | `5m` | период полного ре-синка |
| `default_timeout` | `30s` | таймаут таргета по умолчанию |
| `state_file` | `/var/lib/api-gateway/discovery-state.json` | файл последнего удачного результата (аварийный фолбэк); `""` отключает |

### Аварийный фолбэк

Последний удачный результат discovery сохраняется в `state_file` (атомарная
запись) и применяется при старте гейтвея — поэтому маршруты переживают рестарт,
даже если Docker/Podman недоступен. Для этого каталог `state_file` должен быть
writable (смонтируйте volume). Пустая строка отключает персист.

Ограничение: самый первый холодный старт без файла состояния и с недоступным
Docker/Podman останется без маршрутов, пока провайдер не подключится. Начиная
со второго запуска фолбэк работает.

### Labels

Маркер: `gateway.enable=true` (`true`/`1`/`yes`, регистронезависимо).

Таргет (один на контейнер):

| label | обяз. | смысл | по умолчанию |
|---|---|---|---|
| `gateway.enable` | да | опт-ин контейнера | — |
| `gateway.name` | нет | имя таргета | `service_name_labels`, иначе имя контейнера |
| `gateway.port` | да* | порт | *единственный exposed-порт TCP |
| `gateway.scheme` | нет | `http`/`https` | `http` |
| `gateway.timeout` | нет | таймаут запроса к цели | `discovery.default_timeout` |
| `gateway.health` | нет | health-путь или полный URL | — |
| `gateway.weight` | нет | вес таргета (`0` или отрицательный исключает) | `1` |

Роутеры (0..N на контейнер). Поля задаются коротко (`gateway.<field>` → роутер
`default`) или именованно (`gateway.router.<id>.<field>`):

| поле | смысл | по умолчанию |
|---|---|---|
| `host` | host-правило (wildcard `*.example.com`) | — |
| `path_prefix` | префикс пути | — |
| `methods` | HTTP-методы через запятую | все |
| `strip_path` | срезать префикс при проксировании | `false` |
| `auth.required` | требовать JWT | глобальный `jwt.required` |
| `auth.roles` | роли через запятую: достаточно **любой** из них | — |
| `auth.roles_all` | роли через запятую: нужны **все** | — |
| `auth.strip_token` | удалять `Authorization` | глобальный |
| `rate_limit.rps` | запросов/сек (token bucket) | — |
| `rate_limit.burst` | burst | — |

Роутер без `host` и без `path_prefix` пропускается (нечем матчить).

### Примеры

Один роут (короткая форма):

```yaml
labels:
  gateway.enable: "true"
  gateway.port: "8085"
  gateway.path_prefix: "/api/blog"
```

Несколько роутов с auth и rate-limit (именованная форма):

```yaml
labels:
  gateway.enable: "true"
  gateway.name: "blog"
  gateway.port: "8085"
  gateway.health: "/health"

  gateway.router.public.path_prefix: "/api/blog"
  gateway.router.public.auth.required: "false"

  gateway.router.admin.path_prefix: "/api/admin/blog"
  gateway.router.admin.strip_path: "true"
  gateway.router.admin.methods: "GET,POST"
  gateway.router.admin.auth.required: "true"
  gateway.router.admin.auth.roles: "admin"
  gateway.router.admin.rate_limit.rps: "20"
  gateway.router.admin.rate_limit.burst: "40"
```

Полный пример compose — в [`examples/docker-compose.labels.yml`](examples/docker-compose.labels.yml).
Пошаговый перевод сервиса со статики на labels — в
[`docs/service-discovery-migration.md`](docs/service-discovery-migration.md).

### Podman

Podman отдаёт Docker-совместимый REST API, поэтому используется тот же клиент:

```yaml
discovery:
  provider: podman
  host: "unix:///run/podman/podman.sock"              # rootful
  # host: "unix:///run/user/1000/podman/podman.sock"  # rootless
```

`podman-compose` кладёт имя сервиса в `io.podman.compose.service` — он уже в
`service_name_labels` по умолчанию.

### Безопасность

Socket монтируется **read-only**, клиент делает только `GET` (список контейнеров
и события). Но read-only socket всё равно даёт широкий доступ к Docker daemon;
для чувствительных окружений используйте socket-proxy или удалённый API по TLS.
В labels не храните секреты — они видны через `docker inspect`.

## Docker

```bash
docker build -t api-gateway .
docker run -p 8080:8080 \
  -v $(pwd)/config.local.yaml:/etc/proxy/config.yaml \
  api-gateway
```

## Дашборд

Отдельный read-only бинарник `cmd/dashboard` показывает сводку конфигурации,
последнее сохранённое состояние service discovery и живые метрики гейтвея.
Гейтвей при этом не изменяется и не требует перезапуска.

```bash
# Сборка и запуск локально
make dashboard
make dashboard-run \
  CONFIG=config.local.yaml \
  DASHBOARD_DISCOVERY_STATE=/var/lib/api-gateway/discovery-state.json
```

Эндпоинты: `GET /` — HTML-обзор, `GET /api/status` — тот же снимок в JSON,
`GET /healthz` — `200 ok`.

### Флаги

| Флаг | По умолчанию | Назначение |
|------|--------------|------------|
| `-config` | `/etc/proxy/config.yaml` | конфиг гейтвея для сводки |
| `-discovery-state` | пусто | файл `discovery.state_file`; пусто — панель выключена |
| `-metrics-url` | `http://127.0.0.1:8080/metrics` | `/metrics` гейтвея; пусто — панель выключена |
| `-listen` | `127.0.0.1:8081` | адрес дашборда |
| `-refresh` | `5s` | интервал авто-обновления HTML |
| `-basic-auth` | пусто | `user:password`; защищает все маршруты |

### Источники данных и деградация

Каждый запрос перечитывает источники заново. Если один из них недоступен
(нет файла, битый JSON, гейтвей не отвечает), страница всё равно отрисовывается
с баннером ошибки, а остальные панели продолжают работать. Дашборд стартует
даже при невалидном конфиге гейтвея.

Секреты (`jwt.secret_key`, `basic_auth.password`, `permissions.api_key`,
`permissions.invalidate_token`) никогда не отображаются; URL с userinfo
редактируются. Учётные данные из URL не попадают и в текст ошибок панели.

### Панель метрик

Панель метрик работает, только если у гейтвея включён
`application.metrics_enabled: true` и `-metrics-url` дашборда указывает на
доступный `/metrics`. Если `metrics_enabled` выключен, `/metrics` не отвечает,
и панель показывает ошибку.

Если у гейтвея включён глобальный `basic_auth`, он защищает и `/metrics`.
Тогда либо добавьте `/metrics` в `basic_auth.skip_paths`, либо разрешите IP
дашборда в `application.metrics_allowed_ips` (можно и то, и другое). Иначе
запрос дашборда получит `401`, и панель покажет ошибку.

```yaml
application:
  metrics_enabled: true
  metrics_allowed_ips: ["10.0.0.5"]   # IP дашборда
basic_auth:
  enabled: true
  username: admin
  password: ${GATEWAY_BASIC_AUTH_PASSWORD}
  skip_paths: ["/health", "/metrics"] # либо пропуск Basic Auth для /metrics
```

По умолчанию дашборд слушает loopback. В контейнере задайте `-listen :8081` и
ограничьте сетевой доступ либо включите `-basic-auth`.

### Docker Compose

Дашборд читает тот же конфиг гейтвея, но **мягко** (`config.LoadLenient`):
переменные `${VAR}` подставляются best-effort, а нераскрытые (или пустые)
остаются литералом `${VAR}` и не считаются ошибкой. Валидация при этом не
запускается, поэтому конфиг без таргетов всё равно отображается. Процессу
дашборда **не нужны переменные окружения гейтвея**.

Нераскрытые литералы `${VAR}` не показываются: секретные поля
(`jwt.secret_key`, `basic_auth.password`, `permissions.api_key`,
`permissions.invalidate_token`) в сводку вообще не копируются, а URL с
userinfo редактируются.

```yaml
services:
  api-gateway:
    image: api-gateway
    volumes:
      - ./config.yaml:/etc/proxy/config.yaml:ro
      - gateway-state:/var/lib/api-gateway

  dashboard:
    build:
      context: .
      dockerfile: Dockerfile.dashboard
    # Переменные окружения гейтвея не требуются: конфиг читается мягко,
    # нераскрытые ${VAR} остаются литералами и не отображаются.
    command:
      - -config=/etc/proxy/config.yaml
      - -discovery-state=/var/lib/api-gateway/discovery-state.json
      - -metrics-url=http://api-gateway:8080/metrics
      - -listen=:8081
      - -basic-auth=admin:${DASHBOARD_PASSWORD}
    ports:
      - "127.0.0.1:8081:8081"
    volumes:
      - ./config.yaml:/etc/proxy/config.yaml:ro
      - gateway-state:/var/lib/api-gateway:ro

volumes:
  gateway-state:
```

Образ собирается отдельно: `docker build -f Dockerfile.dashboard -t api-gateway-dashboard .`

## Разработка

```bash
# Установить зависимости
go mod download

# Запуск с локальным конфигом
go run ./cmd/ -config config.local.yaml

# Линт
make lint

# Тесты
make test
```

## Лицензия

MIT
