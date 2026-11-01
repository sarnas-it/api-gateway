# xgate-modules/apikey

Auth-коннектор: аутентификация статичными API-ключами из конфига шлюза.

```yaml
plugins:
  apikey:
    keys: "svc-a:s3cret:admin|ops,svc-b:k2:read"
    # header: "X-API-Key"   # по умолчанию
routing:
  rules:
    - path_prefix: "/api/keys"
      target_name: "api"
      auth:
        connector: "apikey"
        roles: ["admin"]
```

- Формат ключа: `id:secret[:role1|role2]`, записи через запятую.
- Сравнение constant-time по всем ключам; несовпадение — 401.
- `id` попадает в claims как `id`, роли — как `roles`; дальше работают
  `jwt.claim_mappings` / `headers.claim_to_header` / per-route `sign_v3`.
- Секреты держите в env (`${APIKEY_KEYS}`) или в приватном конфиге; в git их быть не должно.

`permissions`-заголовок требует числовой `id`: строковые идентификаторы
(`svc-a`) его пропускают, числовые работают как у JWT.

```bash
xgate build --src . \
  --with code.sarnas.ru/xgate-modules/apikey=./modules/apikey \
  --output ./api-gateway-apikey
```
