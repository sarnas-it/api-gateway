# Участие в разработке

Issues и PR приветствуются. Если задача нетривиальная, сначала заведите issue и
коротко опишите, что хотите изменить.

## Сборка и проверки

```bash
go mod download
make build          # бинарник в bin/api-gateway
make test           # go test ./...
make lint           # golangci-lint
make all            # fmt + vet + lint + build + test
```

Перед PR должны проходить `make lint` и `make test`.

## Конфиг и документация

Любое изменение схемы конфига сопровождайте обновлением
[`config.local.example.yaml`](config.local.example.yaml) и справочника
[`docs/api-gateway-configuration.md`](docs/api-gateway-configuration.md). Свой
конфиг проверяйте флагом `-check` (`-strict` считает ошибкой неизвестные ключи):

```bash
go run ./cmd/ -config config.yaml -check -strict
```

## Коммиты и PR

- Conventional Commits: `feat:`, `fix:`, `docs:`, `chore:`, `test:`.
- Одна тема на один PR. Работа идёт через ветку от `master`, прямых коммитов в
  `master` нет.
- В описании PR: что изменилось по поведению, какие проверки прогнаны, затронуты
  ли формат конфига или метрики.
- Не коммитьте секреты, `.env` и рантайм-артефакты.

## Бенчмарки

Каталоги в `benchmarks/` фиксируют один прогон и не редактируются. Для нового
замера создайте новый датированный каталог. Числа в README берите только из
`results/summary.csv` соответствующего отчёта.

## Где что лежит

- `cmd/` — точка входа и `-check`, `cmd/dashboard` — read-only дашборд
- `internal/` — прокси, JWT, маршрутизация, discovery
- `docs/` — документация (синхронизируется с сайтом sarnas.ru/docs)
- `benchmarks/` — воспроизводимые отчёты
