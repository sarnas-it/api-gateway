<!-- generated — do not edit here; source: frontend/app/docs/api-gateway/quickstart/content.md -->

# Быстрый старт

Эта страница поднимает `api-gateway` с одним бэкендом и одним маршрутом, показывает запуск локально и в Docker, проверку ответа и метрик, а затем — первые шаги: второй сервис, JWT и роли.

## Требования

- Go 1.25+ (для локального запуска) или Docker (для запуска в контейнере).
- Любой HTTP-бэкенд на выбор. В примерах ниже используется `traefik/whoami` — крошечный сервис, который печатает детали запроса.

## Минимальный конфиг

Создайте `config.local.yaml` рядом с репозиторием. Один таргет, одно правило маршрутизации:

```yaml
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
```

`env: "dev"` включает мягкий режим CORS, удобный для локальной разработки. Если у таргета задан `path_prefix`, а секция `routing.rules` пустая, правило маршрутизации создаётся автоматически — но в быстром старте маршрут задан явно, так нагляднее.

## Запуск бэкенда

```bash
docker run --rm -p 9000:80 traefik/whoami
```

Подойдёт и любой другой HTTP-сервер на порту 9000: шлюз не зависит от типа бэкенда.

## Проверка конфигурации

Перед запуском конфиг можно проверить, не поднимая шлюз:

```bash
go run ./cmd/gateway/ -config config.local.yaml -check
# config OK: config.local.yaml
#   targets: 1, routing rules: 1, discovery: false, permissions: false
```

Флаг `-check` загружает и валидирует конфиг, печатает предупреждения и ошибки и завершается с кодом `0` (валиден) или `1` (ошибка) — удобно для CI и перед деплоем. `-strict` дополнительно считает ошибкой предупреждения о неизвестных ключах. В контейнере: `docker run --rm -v "$PWD/config.local.yaml:/etc/proxy/config.yaml" api-gateway -check`.

## Запуск локально

```bash
go mod download
go run ./cmd/gateway/ -config config.local.yaml
```

Шлюз слушает `:8080` и проксирует запросы на `http://127.0.0.1:9000`.

## Запуск в Docker

```bash
docker build -t api-gateway .
docker run --rm -p 8080:8080 \
  -v "$PWD/config.local.yaml:/etc/proxy/config.yaml:ro" \
  api-gateway
```

Образ собирается из `Dockerfile` в корне репозитория; бинарник читает конфиг из `/etc/proxy/config.yaml` по умолчанию.

## Проверка

Запрос через шлюз должен вернуть ответ бэкенда (у `whoami` — 200 и текст с заголовками):

```bash
curl -i http://localhost:8080/
```

Метрики включаются флагом `application.metrics_enabled: true`. После перезапуска:

```bash
curl -s http://localhost:8080/metrics
```

Трейсинг включается переменной окружения: укажите OTLP-эндпоинт коллектора.

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
OTEL_EXPORTER_OTLP_INSECURE=true \
go run ./cmd/gateway/ -config config.local.yaml
```

Без `OTEL_EXPORTER_OTLP_ENDPOINT` трейсинг выключен и на горячем пути ничего не стоит. Если конфиг содержит неизвестные ключи, шлюз не падает — он пишет предупреждение в лог с точечным путём ключа.

## Первые шаги

### Добавить второй сервис

Добавьте таргет и правило с более специфичным префиксом. Побеждает самое длинное совпадение префикса.

```yaml
targets:
  - name: "echo"
    url: "http://127.0.0.1:9000"
    timeout: 5s
  - name: "api"
    url: "http://127.0.0.1:9001"
    timeout: 10s

routing:
  rules:
    - path_prefix: "/"
      target_name: "echo"
    - path_prefix: "/api"
      target_name: "api"
```

### Добавить JWT

Задайте секрет и потребуйте токен на выбранном маршруте:

```yaml
jwt:
  secret_key: "change-me-to-a-secret-at-least-256-bits-long"
  algorithm: "HS256"
  validate_exp: true

routing:
  rules:
    - path_prefix: "/api"
      target_name: "api"
      auth:
        required: true
```

Запрос без заголовка `Authorization: Bearer <token>` получит 401. Токен также принимается из cookie `cml_access`.

### Добавить роль на маршрут

```yaml
routing:
  rules:
    - path_prefix: "/api/admin"
      target_name: "api"
      auth:
        required: true
        roles: ["admin"]
```

Роль берётся из claim `roles` токена; `roles` требует любую из перечисленных ролей, а `roles_all` — все. Если claim `roles` отсутствует или не является строкой либо массивом строк — 401.

## Дальше

- [Конфигурация](/docs/api-gateway/configuration) — все секции, типы, значения по умолчанию и подстановка `${VAR}`.
- [Сценарии](/docs/api-gateway/scenarios) — RBAC, аудит, CORS, ACME, discovery и другие рецепты.
