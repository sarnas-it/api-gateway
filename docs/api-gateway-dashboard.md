<!-- generated — do not edit here; source: frontend/app/docs/api-gateway/dashboard/content.md -->

# Дашборд

Дашборд — отдельный read-only обзор `api-gateway` в стиле Traefik: маршруты, таргеты, состояние discovery, сводка конфигурации и живые метрики на одной странице. Это **отдельный бинарник и контейнер** (`cmd/dashboard`, образ `ghcr.io/sarnas-it/api-gateway-dashboard`), а не часть бинарника шлюза. Дашборд ничего не меняет: редактирования и действий в нём нет.

## Что показывает

- **Маршруты** — правила маршрутизации из конфига с таргетами и признаком обнаружения.
- **Таргеты** — бэкенды, их URL, таймауты и веса.
- **Discovery** — состояние обнаружения и последний удачный результат из state-файла.
- **Сводка конфигурации** — порт, TLS, discovery, permissions и счётчики сущностей.
- **Метрики** — значения из `/metrics` шлюза (панель отключается, если URL не задан).

## Источники данных

Дашборд только читает и собирает данные из трёх мест:

- **Файл конфигурации шлюза** — тот же YAML, что читает `api-gateway`. Разбирается мягко: нераскрытые `${VAR}` остаются литералами (дашборд не падает), а секреты никогда не рендерятся.
- **Файл состояния discovery** — `discovery.state_file` шлюза (последний удачный результат).
- **Эндпоинт `/metrics`** шлюза — если панель метрик включена.

## Флаги запуска

| Флаг | По умолчанию | Смысл |
|---|---|---|
| `-config` | `/etc/proxy/config.yaml` | Путь к конфигу шлюза |
| `-discovery-state` | пусто | Путь к state-файлу discovery; пусто — панель discovery выключена |
| `-metrics-url` | `http://127.0.0.1:8080/metrics` | URL `/metrics` шлюза |
| `-listen` | `127.0.0.1:8081` | Адрес и порт HTTP-сервера дашборда |
| `-refresh` | `5s` | Период автообновления страницы |
| `-basic-auth` | — | `user:password` для Basic Auth; необязателен |

## Эндпоинты

| Метод и путь | Назначение |
|---|---|
| `GET /` | HTML-обзор с автообновлением |
| `GET /api/status` | То же в формате JSON |
| `GET /healthz` | Проверка живости |

## Безопасность

- Секреты из конфига **не рендерятся**.
- По умолчанию дашборд слушает loopback (`127.0.0.1:8081`) и наружу не смотрит.
- Доступ можно закрыть Basic Auth (`-basic-auth user:password`).
- Панель метрик работает, только если у шлюза включены `application.metrics_enabled: true` и IP/сеть дашборда перечислены в `application.metrics_allowed_ips`. Поддерживаются CIDR, например `172.31.0.0/24`.
- Шлюз отдаёт `/metrics` по HTTP только разрешённым IP — для остальных HTTP-порт редиректит на HTTPS.

## Развёртывание

Сервис дашборда монтирует конфиг шлюза и volume состояния **только для чтения**, входит в общую внутреннюю сеть со шлюзом и указывает `-metrics-url` на его адрес:

```yaml
services:
  api-gateway:
    # ...
    networks:
      default: {}
      gateway-metrics:
        ipv4_address: 172.31.0.2

  dashboard:
    image: ghcr.io/sarnas-it/api-gateway-dashboard:latest
    command:
      - -config=/etc/proxy/config.yaml
      - -discovery-state=/var/lib/api-gateway/discovery-state.json
      - -metrics-url=http://172.31.0.2:80/metrics
      - -listen=:8081
    volumes:
      - ./api-gateway-config.yaml:/etc/proxy/config.yaml:ro
      - api-gateway-state:/var/lib/api-gateway:ro
    ports:
      - "127.0.0.1:8081:8081"
    networks:
      - default
      - gateway-metrics
    depends_on:
      - api-gateway
    restart: unless-stopped

networks:
  gateway-metrics:
    ipam:
      config:
        - subnet: 172.31.0.0/24

volumes:
  api-gateway-state:
```

В конфиге шлюза этому соответствует:

```yaml
application:
  metrics_enabled: true
  metrics_allowed_ips:
    - "172.31.0.0/24"
```

## Доступ

Порт опубликован на loopback, поэтому проще всего открыть дашборд через SSH-туннель:

```bash
ssh -N -L 8081:127.0.0.1:8081 <host>
# затем откройте http://localhost:8081
```

Чтобы выставить дашборд наружу, поставьте его за шлюзом и включите Basic Auth (`-basic-auth`).

## Сборка

```bash
make dashboard
```

Собирается по `Dockerfile.dashboard`; можно и просто запустить опубликованный образ `ghcr.io/sarnas-it/api-gateway-dashboard`.
