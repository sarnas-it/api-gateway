# xgate module template

Минимальный HTTP-middleware модуль. Скопируйте каталог, поменяйте module path,
имя модуля и `Wrap`.

```bash
xgate build \
  --with code.sarnas.ru/xgate-modules/example=./modules/example \
  --output ./api-gateway-example
```

Модуль реализует `plugin.Module` и `plugin.HTTPMiddleware`. Для DNS-модуля
используйте `plugin.DNSProvider` и `plugin.RegisterDNS` (см.
`modules/dns/cloudflare`).
