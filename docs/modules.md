# Индекс xgate-модулей

| Модуль | Тип | Конфиг | Статус |
|---|---|---|---|
| `code.sarnas.ru/xgate-modules/dns-cloudflare` | DNS-01 | `api_token` / `CF_API_TOKEN` | ✅ сборка/тесты |
| `code.sarnas.ru/xgate-modules/dns-desec` | DNS-01 | `token` / `DESEC_TOKEN` | ✅ сборка/тесты |
| `code.sarnas.ru/xgate-modules/dns-digitalocean` | DNS-01 | `api_token` / `DO_API_TOKEN` | ✅ сборка/тесты |
| `code.sarnas.ru/xgate-modules/dns-route53` | DNS-01 | `region`, `profile`, `access_key_id`, `secret_access_key`, `session_token`, `max_retries` | ✅ сборка/тесты |
| `code.sarnas.ru/xgate-modules/example` | HTTP middleware | — | шаблон |

## Как добавить модуль

1. Скопировать `modules/example` (или `modules/dns/cloudflare` для DNS).
2. Заменить module path в `go.mod` и имя в `Name()`.
3. Реализовать `plugin.HTTPMiddleware` или `plugin.DNSProvider`.
4. Зарегистрировать в `init()`.
5. Добавить модуль в `xgate.example.yaml`/CI и в эту таблицу.

Локальная сборка использует `replacement` на каталог модуля. После публикации
модуля с тегом `replacement` не нужен.

Параметры модуля задаются в `plugins.<name>` конфига шлюза; модуль читает их
в `Configure(map[string]string)`. Неизвестное имя — ошибка запуска. HTTP-middleware
модули (`Wrap`) оборачивают публичный обработчик HTTP/HTTPS/HTTP3; первый
зарегистрированный — снаружи.
