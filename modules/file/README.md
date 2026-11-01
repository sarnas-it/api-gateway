# xgate-modules/discovery-file

Discovery-модуль: читает сервисы из JSON-файла и опрашивает его на изменения.
Нужен там, где реестр сервисов уже выгружается в файл, а Docker API недоступен.

```yaml
discovery:
  enabled: true
  module: "file"
  default_timeout: 5s
  state_file: /var/lib/api-gateway/discovery-state.json
plugins:
  file:
    path: /etc/api-gateway/services.json
    interval: 2s
```

Формат файла:

```json
[
  {"name": "api-1", "address": "10.0.0.5:8080", "path_prefix": "/api", "strip_path": true},
  {"name": "api-2", "address": "https://api2.internal", "path_prefix": "/api"}
]
```

- `address` — `host:port` (схема `http`) или полный URL.
- `path_prefix` по умолчанию `/<name>`.
- Файл читается атомарно: битый или полузаписанный JSON игнорируется, остаётся
  последнее удачное состояние. Событие отправляется только при изменении содержимого.
- Изменение `discovery.module` требует рестарта шлюза; SIGHUP перечитывает
  только статический конфиг.

```bash
xgate build --src . \
  --with code.sarnas.ru/xgate-modules/discovery-file=./modules/file \
  --output ./api-gateway-file-discovery
```
