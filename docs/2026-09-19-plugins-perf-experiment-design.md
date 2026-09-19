# Проверка гипотезы: вынос функциональности api-gateway в плагины без потери производительности

Дата: 2026-09-19
Статус: согласован
Репозитории: `sarnas-it/api-gateway` (ветка `experiment/plugins-perf`), `sarnas-it/pluginrpc` (зависимость).

## 1. Контекст и цель

api-gateway — монолит: вся функциональность живёт в одном бинаре. Чтобы добавить/убрать
фичу, нужен PR и пересборка. Решение — выносить фичи в динамически подключаемые плагины
через библиотеку `pluginrpc` (четыре транспорта: `so`, `shared`, `fast`, `grpc`).

Задача — **проверить гипотезу**: можно ли выносить функциональность гейтвея в плагины
**без потери производительности**. Цель эксперимента — измерить стоимость plugin-границы
на реальном гейтвее и дать количественный ответ по каждой фиче и транспорту.

## 2. Критерий успеха

- **Пропускная способность**: разница `plugin` против `baseline` **не хуже ±2%** по req/s
  (на c50 и c300).
- **Латентность**: p99 не растёт за пределы шума измерения.
- **Аллокации/память**: фиксируем фактическую разницу, отдельный жёсткий порог не задаём.
- Важно: на ноутбуке прогоны шумные. Сначала измеряем **noise floor** (baseline vs baseline,
  N прогонов). Вывод «потери нет» делаем только если дельта `plugin↔baseline` **внутри
  спреда** и одновременно **в пределах ±2%**.
- Если `so`/`shared` (самые быстрые транспорты) дают просадку — более медленные (`fast`,
  `grpc`) не тестируем: смысла нет.

## 3. Скоуп

### Фичи (4)

1. **JWT-авторизация** (hot path): валидация токена + извлечение claims.
2. **Rate limiting** (hot path, stateful): per-route per-IP token bucket.
3. **Webhooks/NATS аудит** (вне hot path): публикация AuditEvent с батчингом.
4. **Discovery** (вне hot path): Docker service discovery, обновление конфига.

### Транспорты

Только самые быстрые, по правилу «если быстрые — узкое горло, медленные бессмысленны»:

| Фича | Транспорты (в порядке проверки) |
|---|---|
| JWT | `so` (direct), при просадке — `shared` |
| Rate limit | `shared`, при просадке — `fast` |
| Webhooks | `fast` |
| Discovery | `fast` |

`grpc` не включаем в прогоны.

## 4. Архитектура

Вся работа — в локальной ветке `experiment/plugins-perf` api-gateway (без PR/пуша).

Принцип: **логика фичи остаётся тем же кодом**. Плагин — тонкая обёртка над существующими
внутренностями гейтвея (`internal/...`). Измеряется чистая стоимость границы вызова, а не
разница двух реализаций.

```
api-gateway/
├── proto/features/
│   ├── authsvc.proto      # Validate(token, rule) → claims + signature
│   ├── ratelimit.proto    # Allow(routeKey, ip, rate, burst) → bool
│   ├── events.proto       # Publish(AuditEvent)
│   └── discovery.proto    # Watch() server-streaming targets
├── cmd/plugins/
│   ├── jwt/main.go        # обёртка над internal/jwtutil
│   ├── ratelimit/main.go  # обёртка над internal/proxy rate_limiter
│   ├── events/main.go     # обёртка над internal/proxy publisher + webhook_batcher
│   └── discovery/main.go  # обёртка над internal/discovery
├── internal/proxy/        # адаптеры: существующие интерфейсы поверх pluginrpc
├── benchmarks/2026-09-plugins-experiment/   # конфиги, скрипты, отчёт
└── config.local.example.yaml                # + секция plugins
```

Протобуф-контракты генерируем через `protoc` (есть protoc 29.3, protoc-gen-go,
protoc-gen-go-grpc). Плагины импортируют `internal/` гейтвея — они в том же модуле.

## 5. Конфиг

Обратно совместимый: при `plugins.enabled: false` или отсутствии секции фичи используется
встроенная реализация.

```yaml
plugins:
  enabled: true
  jwt:
    path: bin/plugins/jwt.so
    transport: so
  ratelimit:
    path: bin/plugins/ratelimit
    transport: shared
  webhooks:
    path: bin/plugins/events
    transport: fast
  discovery:
    path: bin/plugins/discovery
    transport: fast
```

## 6. Data flow (plugin-режим)

- **JWT**: `proxyHandler.modifyRequest` зовёт `validator.ValidateToken(token, rule)` →
  адаптер шлёт pluginrpc `Validate` → плагин гоняет тот же `jwtutil.Validate` → возвращает
  claims + заголовки подписи. Ролевые проверки/заголовки собирает хост. Валидатор уже
  интерфейсный (`JWTValidator`) — точка внедрения одна.
- **Rate limit**: адаптер реализует интерфейс лимитера, состояние (IP→bucket) живёт в
  плагине. Хост передаёт параметры маршрута в каждом вызове (`Allow(routeKey, ip, rate,
  burst)`) — переживаем hot-reload без push-конфига.
- **Webhooks**: хост строит `AuditEvent` (существующий DTO) и ловит тело ответа; батчинг и
  NATS/HTTP-publish уезжают в плагин через `Publish(event)`.
- **Discovery**: хост держит открытый server-streaming `Watch`; плагин стримит `Result`'ы
  по Docker-событиям; хост сливает через существующий `onUpdate`-колбэк. `Provider`-интерфейс
  уже есть — адаптер под него.

## 7. Ошибки и деградация

- **Старт плагина не удался** → fail-fast при старте гейтвея с понятной ошибкой.
- **Падение в рантайме** (политика по фиче):
  - JWT, rate limit: **fail closed** (401/503) — нельзя пускать без проверки.
  - Webhooks: **fail open** — запрос проходит, событие теряется (лог).
  - Discovery: **fail open** — маршруты остаются на последнем известном состоянии.
- Перезапуск — `RestartLazy` + backoff pluginrpc; терминальные ошибки — через
  `Handle.Done()`/`Handle.Err()`.

## 8. Reload (SIGHUP)

Плагин получает конфиг только при старте (`PluginConfig`). При hot-reload с изменением
параметров фичи — перезапуск плагина. Это осознанный trade-off плагинизации: ~20ms
даунтайм фичи на reload (издержка `waitReady`-полла pluginrpc). В прогонах эксперимента
reload не гоняем, фиксируем в отчёте.

## 9. Тесты

- **Юнит**: адаптеры против заглушки conn (без реального плагина).
- **Smoke**: собрать JWT-плагин, запустить гейтвей с plugin-конфигом, прогнать curl
  (valid/invalid token) — проверка end-to-end работы границы.
- **Bench-сеть**: см. Секцию 10.

## 10. Бенчмарк-харнесс и методология

Каталог `benchmarks/2026-09-plugins-experiment/`:

```
├── configs/          # baseline.yaml + по конфигу на (фичу × транспорт)
├── scripts/          # run-bench.sh (общий) + per-feature прогоны
├── helpers/backend/  # мини Go-HTTP 200-бэкенд (без Docker)
├── bench_test.go     # Go-бенчмарки hot-path с benchmem
└── README.md         # методология + таблицы + вердикт
```

Методика:

- Один конфиг фич для всех режимов; отличаются только провайдеры (builtin vs plugin).
- `wrk` локально: c50 (`-t2 -c50 -d15s`) и c300 (`-t4 -c300 -d20s`), best-of-N с
  чередованием прогонов baseline/plugin (сглаживание дрейфа ноутбука).
- Noise floor: baseline vs baseline (N прогонов) → спред. Вывод «нет потери» — только если
  дельта внутри спреда и ≤2%.
- p99 — из wrk-латенси; allocs/op — из Go-бенчмарка; память — из `/proc`/pprof.

Сценарии:

| Режим | JWT | Rate limit | Webhooks | Discovery |
|---|---|---|---|---|
| baseline | builtin | builtin | builtin | builtin |
| plugin | so, (shared) | shared | fast | fast |

## 11. Отчёт и артефакты

1. Локальная ветка `experiment/plugins-perf` с кодом эксперимента (контракты, плагины,
   адаптеры, конфиги, скрипты, Go-бенчмарки).
2. `benchmarks/2026-09-plugins-experiment/README.md` — методология, hardware, noise floor,
   таблицы (baseline vs plugin: req/s c50/c300, Δ%, p99, allocs/op, память), вердикт по
   каждой фиче (**прошло**/**не прошло** с цифрой), вывод по гипотезе, рекомендация по
   транспорту для класса фич и условие перехода к Подходу 2 (реальный вынос кода из бинаря).

## 12. Ограничения и риски

- `.so` требует cgo и нестатической сборки — конфликт с текущим `CGO_ENABLED=0` scratch.
  Для JWT-теста собираем с cgo; сам факт — отдельный вывод эксперимента.
- `shared` — только Linux; ноутбук Linux, ок.
- Прогоны на ноутбуке шумные — выводы только на базе noise floor.
- Логика фич не переписывается — измеряем границу, а не «лучше/хуже реализация».