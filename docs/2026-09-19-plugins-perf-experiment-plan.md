# Проверка гипотезы plugin-выноса фич api-gateway — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Проверить на реальном гейтвее, можно ли вынести JWT-авторизацию, rate limit, webhooks/NATS и discovery в плагины pluginrpc без потери производительности (критерий: Δ по req/s ≤ ±2% и внутри шума).

**Architecture:** Логика фич остаётся тем же кодом (`internal/jwtutil`, `internal/proxy`, `internal/discovery`); плагины — тонкие обёртки поверх proto-контрактов. Хост получает переключатель `plugins.enabled` в конфиге: при выключенном — прежнее поведение гейтвея не меняется. Бенчмарк: wrk (c50/c300) + Go-бенчмарк с benchmem на реальном `MultiProxy`.

**Tech Stack:** Go 1.26 (toolchain auto-switch), `github.com/sarnas-it/pluginrpc`, protoc 29.3 + protoc-gen-go + protoc-gen-go-grpc, gRPC/protobuf, wrk 4.1, `golang.org/x/time/rate`, NATS, zap.

**Worktree:** `~/GolandProjects/worktrees/api-gateway-experiment-plugins-perf` (ветка `experiment/plugins-perf`). Все команды — из этого каталога (`pwd` сверять перед правками).

## Global Constraints

- Вся работа — локальная ветка `experiment/plugins-perf`, **без PR/пуша**.
- Порог успеха: Δ req/s `plugin↔baseline` **≤ ±2%** и **внутри noise floor** (baseline-vs-baseline спред). p99 не растёт за шум.
- Транспорты: JWT → `so`, при просадке `shared`; rate limit → `shared`; webhooks → `fast`; discovery → `fast`. `grpc` НЕ тестируем.
- Логика фич не переписывается: плагин оборачивает существующий код (`internal/...`). Плагины живут в том же модуле (могут импортировать `internal/`).
- Baseline-режим (`plugins.enabled: false`) не должен менять поведение и hot-path гейтвея.
- Коммиты — Conventional Commits (`feat:`, `fix:`, `chore:`, `test:`, `docs:`), сфокусированные.
- Секреты в бенчмарк-конфигах — только несекретные (`benchsecret`), как в существующем сравнении.
- pluginrpc требует `go 1.26`; локальный go1.25.11 подтянет go1.26-тулчейн (GOTOOLCHAIN=auto; нужен сетевой доступ) — либо явно `go mod edit -go=1.26.0`.
- Сгенерированные пакеты: `internal/features/{authv1,ratelimitv1,eventsv1,discoveryv1}`.
- Для `.so` гейтвей собирается с cgo (НЕ `CGO_ENABLED=0`); локально gcc есть.
- Пути к плагинам в бенчмарк-конфигах: `bin/plugins/...` относительно корня репо.
- В тестах `internal/proxy` вместо несуществующего `testLogger` использовать `zap.NewNop()`.

---

### Task 1: Подключить pluginrpc и выровнять тулчейн

**Files:**
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: —
- Produces: зависимость `github.com/sarnas-it/pluginrpc` (дальше из неё берутся `pluginrpc.Start/Serve/SOServe/DirectService`).

- [ ] **Step 1: Проверить, что worktree на месте и на нужной ветке**

```bash
pwd  # должно быть ~/GolandProjects/worktrees/api-gateway-experiment-plugins-perf
git branch --show-current  # experiment/plugins-perf
```

- [ ] **Step 2: Добавить pluginrpc и поднять go-директиву**

```bash
go get github.com/sarnas-it/pluginrpc@latest
go mod edit -go=1.26.0
go mod tidy
```

Ожидаемо: grpc поднимется до ≥ v1.84.0, protobuf до ≥ v1.36.12; go.mod начнёт требовать `go 1.26.0` (тулчейн go1.26 подтянется автоматически).

- [ ] **Step 3: Собрать весь репозиторий**

```bash
go build ./...
```

Ожидаемо: успех. Если `go build` требует go1.26 и не может скачать тулчейн — остановиться и сообщить пользователю (нужен доступ к proxy.golang.org).

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add pluginrpc dependency, bump go directive to 1.26"
```

---

### Task 2: Протобуф-контракты фич и генерация стабов

**Files:**
- Create: `proto/features/authsvc.proto`, `proto/features/ratelimit.proto`, `proto/features/events.proto`, `proto/features/discovery.proto`
- Create: `internal/features/authv1/*.pb.go`, `internal/features/ratelimitv1/*.pb.go`, `internal/features/eventsv1/*.pb.go`, `internal/features/discoveryv1/*.pb.go` (генерируются)
- Modify: `Makefile` (target `gen-proto`)

**Interfaces:**
- Consumes: —
- Produces:
  - `authv1.AuthServiceClient/Server`, `authv1.ValidateRequest{Token string; Required bool; AnyRoles []string; AllRoles []string}`, `authv1.ValidateResponse{Ok bool; Error string; HasClaims bool; Claims map[string]string}`
  - `ratelimitv1.RateLimitServiceClient/Server`, `ratelimitv1.AllowRequest{RouteKey string; Ip string; Rate float64; Burst int64}`, `ratelimitv1.AllowResponse{Allowed bool}`
  - `eventsv1.EventServiceClient/Server`, `eventsv1.AuditEvent`, `eventsv1.PublishRequest{WebhookName string; Event *AuditEvent}`
  - `discoveryv1.DiscoveryServiceClient/Server`, `discoveryv1.Target`, `discoveryv1.Rule`, `discoveryv1.DiscoveryResult`, `discoveryv1.WatchRequest`

- [ ] **Step 1: Написать proto-файлы**

`proto/features/authsvc.proto`:
```proto
syntax = "proto3";
package features.auth.v1;
option go_package = "github.com/basili4-1982/api-gateway/internal/features/authv1";

message ValidateRequest {
  string token = 1;                 // значение заголовка Authorization целиком
  bool required = 2;                // эффективный jwt.required (с учётом rule.Auth.Required)
  repeated string any_roles = 3;    // rule.Auth.Roles
  repeated string all_roles = 4;    // rule.Auth.RolesAll
}

message ValidateResponse {
  bool ok = 1;                      // можно продолжать обработку (401 если false)
  string error = 2;                 // сообщение для 401
  bool has_claims = 3;              // токен валиден и claims извлечены
  map<string, string> claims = 4;   // результат ExtractClaims(claimMappings): claim → string
}

service AuthService {
  rpc Validate(ValidateRequest) returns (ValidateResponse);
}
```

`proto/features/ratelimit.proto`:
```proto
syntax = "proto3";
package features.ratelimit.v1;
option go_package = "github.com/basili4-1982/api-gateway/internal/features/ratelimitv1";

message AllowRequest {
  string route_key = 1;   // config.RuleRouteKey(rule)
  string ip = 2;          // clientIP из getClientIP
  double rate = 3;        // rule.RateLimit.RequestsPerSecond
  int64 burst = 4;        // rule.RateLimit.Burst
}

message AllowResponse {
  bool allowed = 1;
}

service RateLimitService {
  rpc Allow(AllowRequest) returns (AllowResponse);
}
```

`proto/features/events.proto`:
```proto
syntax = "proto3";
package features.events.v1;
option go_package = "github.com/basili4-1982/api-gateway/internal/features/eventsv1";

message AuditEvent {
  string method = 1;
  string path = 2;
  string query = 3;
  string user_id = 4;
  string user_email = 5;
  string user_roles = 6;
  string request_id = 7;
  int32 status_code = 8;
  int64 timestamp_nanos = 9;   // time.Time.UnixNano()
  map<string, string> headers = 10;
  bytes changes = 11;          // json.RawMessage
  bytes response_body = 12;    // json.RawMessage
}

message PublishRequest {
  string webhook_name = 1;
  AuditEvent event = 2;
}

message PublishResponse {}

service EventService {
  rpc Publish(PublishRequest) returns (PublishResponse);
}
```

`proto/features/discovery.proto`:
```proto
syntax = "proto3";
package features.discovery.v1;
option go_package = "github.com/basili4-1982/api-gateway/internal/features/discoveryv1";

message Target {
  string name = 1;
  string url = 2;
  string path_prefix = 3;
  bool strip_prefix = 4;
  int32 weight = 5;
  string health_check = 6;
}

message Rule {
  string host = 1;
  string path_prefix = 2;
  string target_name = 3;
  repeated string methods = 4;
  bool strip_path = 5;
}

message DiscoveryResult {
  repeated Target targets = 1;
  repeated Rule rules = 2;
}

message WatchRequest {}

service DiscoveryService {
  rpc Watch(WatchRequest) returns (stream DiscoveryResult);
}
```

- [ ] **Step 2: Добавить Makefile-таргет и сгенерировать стабы**

В конец `Makefile`:
```make
gen-proto:
	protoc -I proto/features --go_out=. --go_opt=module=github.com/basili4-1982/api-gateway \
		--go-grpc_out=. --go-grpc_opt=module=github.com/basili4-1982/api-gateway \
		proto/features/authsvc.proto proto/features/ratelimit.proto \
		proto/features/events.proto proto/features/discovery.proto
```
Запуск:
```bash
make gen-proto
go build ./...
```
Ожидаемо: появляются `internal/features/{authv1,ratelimitv1,eventsv1,discoveryv1}/*.pb.go`, `go build ./...` успешен.

- [ ] **Step 3: Commit**

```bash
git add proto/features internal/features Makefile
git commit -m "feat: proto-контракты и сгенерированные стабы фич-плагинов"
```

---

### Task 3: Схема конфига `plugins`

**Files:**
- Modify: `internal/config/config.go` (структуры `PluginsConfig`, `PluginSpec`, поле в `Config`, валидация)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: —
- Produces:
  - `config.Config.Plugins PluginsConfig` (yaml `plugins`)
  - `config.PluginsConfig{Enabled bool; JWT *PluginSpec; RateLimit *PluginSpec; Webhooks *PluginSpec; Discovery *PluginSpec}`
  - `config.PluginSpec{Path string; Transport string}` (`so|shared|fast|grpc`)

- [ ] **Step 1: Написать падающий тест**

В `internal/config/config_test.go` добавить:
```go
func TestPluginsValidation(t *testing.T) {
	t.Run("spec without path fails", func(t *testing.T) {
		cfg := minimalConfig()
		cfg.Plugins = PluginsConfig{Enabled: true, JWT: &PluginSpec{Transport: "so"}}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected error for empty plugin path")
		}
	})
	t.Run("disabled plugins skip validation", func(t *testing.T) {
		cfg := minimalConfig()
		cfg.Plugins = PluginsConfig{Enabled: false, JWT: &PluginSpec{Transport: "so"}}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
```
`minimalConfig()` — существующий хелпер в этом файле; если его нет — создать минимальный валидный `Config` по образцу существующих тестов.

- [ ] **Step 2: Запустить тест — убедиться, что падает**

```bash
go test ./internal/config/ -run TestPluginsValidation -v
```
Ожидаемо: `undefined: PluginsConfig` (компиляция не проходит).

- [ ] **Step 3: Реализовать схему**

В `internal/config/config.go` в `Config` добавить поле:
```go
	Plugins     PluginsConfig    `yaml:"plugins"`
```
Рядом с другими типами:
```go
// PluginsConfig конфигурация подключения фич как pluginrpc-плагинов.
type PluginsConfig struct {
	Enabled   bool        `yaml:"enabled"`
	JWT       *PluginSpec `yaml:"jwt,omitempty"`
	RateLimit *PluginSpec `yaml:"ratelimit,omitempty"`
	Webhooks  *PluginSpec `yaml:"webhooks,omitempty"`
	Discovery *PluginSpec `yaml:"discovery,omitempty"`
}

// PluginSpec описывает один плагин: путь к артефакту и транспорт.
type PluginSpec struct {
	Path      string `yaml:"path"`
	Transport string `yaml:"transport"` // so | shared | fast | grpc
}
```
В начале `validate()` (метод с проверками конфига) добавить:
```go
	if c.Plugins.Enabled {
		for name, spec := range map[string]*PluginSpec{
			"jwt": c.Plugins.JWT, "ratelimit": c.Plugins.RateLimit,
			"webhooks": c.Plugins.Webhooks, "discovery": c.Plugins.Discovery,
		} {
			if spec != nil && spec.Path == "" {
				return fmt.Errorf("plugins.%s.path is required", name)
			}
		}
	}
```

- [ ] **Step 4: Прогнать тесты конфига**

```bash
go test ./internal/config/
```
Ожидаемо: PASS (включая новый тест).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): секция plugins в конфиге гейтвея"
```

---

### Task 4: Вынести проверку ролей в `jwtutil.CheckRoles`

**Files:**
- Modify: `internal/jwtutil/jwt.go` (добавить `CheckRoles`), `internal/proxy/multi_proxy.go` (`checkRoles` делегирует, удалить `extractRoles`)
- Test: `internal/jwtutil/jwt_test.go`

**Interfaces:**
- Consumes: —
- Produces: `jwtutil.CheckRoles(claims jwt.MapClaims, anyOf, allOf []string) error` (fail-closed, семантика прежнего `proxy.checkRoles`)

- [ ] **Step 1: Написать падающий тест**

В `internal/jwtutil/jwt_test.go`:
```go
func TestCheckRoles(t *testing.T) {
	claims := jwt.MapClaims{"roles": []interface{}{"admin", "user"}}
	if err := CheckRoles(claims, []string{"user"}, nil); err != nil {
		t.Fatalf("any-of should pass: %v", err)
	}
	if err := CheckRoles(claims, nil, []string{"admin"}); err != nil {
		t.Fatalf("all-of should pass: %v", err)
	}
	if err := CheckRoles(claims, []string{"nobody"}, nil); err == nil {
		t.Fatal("any-of should fail")
	}
	if err := CheckRoles(claims, nil, []string{"admin", "root"}); err == nil {
		t.Fatal("all-of should fail")
	}
}

func TestCheckRolesStringRole(t *testing.T) {
	claims := jwt.MapClaims{"roles": "admin"}
	if err := CheckRoles(claims, []string{"admin"}, nil); err != nil {
		t.Fatalf("string role should pass: %v", err)
	}
}
```

- [ ] **Step 2: Запустить — убедиться, что падает**

```bash
go test ./internal/jwtutil/ -run TestCheckRoles -v
```
Ожидаемо: `undefined: CheckRoles`.

- [ ] **Step 3: Реализовать**

В `internal/jwtutil/jwt.go` (перенести тело из `proxy.checkRoles`/`extractRoles`):
```go
// CheckRoles проверяет роли из claims по схеме (any of anyOf) AND (all of allOf).
// Пустые оба списка — проверка не нужна. Отсутствующий/нестроковый/не-массивный
// claim или массив с нестроковыми элементами — ошибка (fail closed).
func CheckRoles(claims jwt.MapClaims, anyOf, allOf []string) error {
	if len(anyOf) == 0 && len(allOf) == 0 {
		return nil
	}
	roleSet, err := extractRoles(claims)
	if err != nil {
		return err
	}
	if len(anyOf) > 0 {
		found := false
		for _, required := range anyOf {
			if roleSet[required] {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("missing any required role: %s", strings.Join(anyOf, ", "))
		}
	}
	for _, required := range allOf {
		if !roleSet[required] {
			return fmt.Errorf("missing required role: %s", required)
		}
	}
	return nil
}

func extractRoles(claims jwt.MapClaims) (map[string]bool, error) {
	raw, ok := claims["roles"]
	if !ok {
		return nil, fmt.Errorf("missing roles claim")
	}
	switch v := raw.(type) {
	case string:
		return map[string]bool{v: true}, nil
	case []string:
		set := make(map[string]bool, len(v))
		for _, role := range v {
			set[role] = true
		}
		return set, nil
	case []interface{}:
		set := make(map[string]bool, len(v))
		for _, item := range v {
			role, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("invalid roles claim: non-string element %T", item)
			}
			set[role] = true
		}
		return set, nil
	default:
		return nil, fmt.Errorf("invalid roles claim format: %T", raw)
	}
}
```
В `internal/proxy/multi_proxy.go` заменить тело `checkRoles` на делегирование и удалить `extractRoles`:
```go
func (mp *MultiProxy) checkRoles(claims jwt.MapClaims, anyOf, allOf []string) error {
	return jwtutil.CheckRoles(claims, anyOf, allOf)
}
```
Удалить теперь неиспользуемую функцию `extractRoles` (иначе `go vet` ругнётся).

- [ ] **Step 4: Прогнать тесты**

```bash
go test ./internal/jwtutil/ ./internal/proxy/
```
Ожидаемо: PASS (старые тесты ролей в proxy тоже проходят — семантика не изменилась).

- [ ] **Step 5: Commit**

```bash
git add internal/jwtutil/jwt.go internal/jwtutil/jwt_test.go internal/proxy/multi_proxy.go
git commit -m "feat(jwtutil): вынести CheckRoles для переиспользования плагином"
```

---
### Task 5: Пакет `internal/plugins` — рантайм pluginrpc

**Files:**
- Create: `internal/plugins/plugins.go`
- Test: `internal/plugins/plugins_test.go`

**Interfaces:**
- Consumes: `config.Config`, `config.PluginsConfig`, pluginrpc
- Produces:
  - `type AuthVerifier interface { Validate(context.Context, *authv1.ValidateRequest) (*authv1.ValidateResponse, error) }`
  - `type Backend struct { Auth AuthVerifier; RateLimit ratelimitv1.RateLimitServiceClient; Events eventsv1.EventServiceClient; Discovery discoveryv1.DiscoveryServiceClient }`
  - `func Start(ctx context.Context, cfg *config.Config, log *zap.Logger) (*Backend, error)` — nil, если плагины выключены; стартует только сконфигурированные фичи; для `so` перед Start ставит `PLUGINRPC_CONFIG` из `cfg.JWT`.
  - `func (b *Backend) Stop(ctx context.Context)`
  - `func parseTransport(s string) (pluginrpc.Transport, error)`

- [ ] **Step 1: Написать падающий тест для parseTransport**

`internal/plugins/plugins_test.go`:
```go
package plugins

import (
	"testing"

	"github.com/sarnas-it/pluginrpc"
)

func TestParseTransport(t *testing.T) {
	cases := map[string]pluginrpc.Transport{
		"so": pluginrpc.TransportSO, "shared": pluginrpc.TransportShared,
		"fast": pluginrpc.TransportFast, "grpc": pluginrpc.TransportGRPC,
		"": pluginrpc.TransportGRPC,
	}
	for in, want := range cases {
		got, err := parseTransport(in)
		if err != nil {
			t.Fatalf("parseTransport(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("parseTransport(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := parseTransport("bogus"); err == nil {
		t.Fatal("expected error for unknown transport")
	}
}
```

- [ ] **Step 2: Запустить — убедиться, что падает**

```bash
go test ./internal/plugins/ -run TestParseTransport -v
```
Ожидаемо: `undefined: parseTransport`.

- [ ] **Step 3: Реализовать**

`internal/plugins/plugins.go`:
```go
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/features/authv1"
	"github.com/basili4-1982/api-gateway/internal/features/discoveryv1"
	"github.com/basili4-1982/api-gateway/internal/features/eventsv1"
	"github.com/basili4-1982/api-gateway/internal/features/ratelimitv1"
	"github.com/sarnas-it/pluginrpc"
	"go.uber.org/zap"
)

// AuthVerifier — единый интерфейс валидации JWT: реализуется и клиентом, и
// сервером (SO direct), поэтому host не различает транспорт.
type AuthVerifier interface {
	Validate(context.Context, *authv1.ValidateRequest) (*authv1.ValidateResponse, error)
}

// Backend — все запущенные плагины и их клиенты.
type Backend struct {
	Auth      AuthVerifier
	RateLimit ratelimitv1.RateLimitServiceClient
	Events    eventsv1.EventServiceClient
	Discovery discoveryv1.DiscoveryServiceClient

	handles []*pluginrpc.Handle
}

// Start запускает сконфигурированные плагины. Возвращает nil, если
// плагины выключены или нет ни одной сконфигурированной фичи.
func Start(ctx context.Context, cfg *config.Config, log *zap.Logger) (*Backend, error) {
	if cfg == nil || !cfg.Plugins.Enabled {
		return nil, nil
	}
	b := &Backend{}
	plog := slog.New(slog.NewTextHandler(os.Stderr, nil))

	start := func(name string, spec *config.PluginSpec, pluginCfg any) (*pluginrpc.Handle, error) {
		if spec == nil {
			return nil, nil
		}
		transport, err := parseTransport(spec.Transport)
		if err != nil {
			return nil, fmt.Errorf("plugins.%s: %w", name, err)
		}
		if transport == pluginrpc.TransportSO && pluginCfg != nil {
			// SO не доставляет PluginConfig: единственный канал — env до open.
			data, err := json.Marshal(pluginCfg)
			if err != nil {
				return nil, fmt.Errorf("plugins.%s: marshal config: %w", name, err)
			}
			if err := os.Setenv(pluginrpc.EnvConfig, string(data)); err != nil {
				return nil, fmt.Errorf("plugins.%s: setenv: %w", name, err)
			}
		}
		h, err := pluginrpc.Start(ctx, pluginrpc.Config{
			Path:         spec.Path,
			Name:         name,
			Transport:    transport,
			PluginConfig: pluginCfg,
			StartTimeout: 10 * time.Second,
			StopTimeout:  3 * time.Second,
			Logger:       plog,
		})
		if err != nil {
			return nil, fmt.Errorf("plugins.%s: start: %w", name, err)
		}
		return h, nil
	}

	if spec := cfg.Plugins.JWT; spec != nil {
		h, err := start("jwt", spec, cfg.JWT)
		if err != nil {
			return nil, err
		}
		b.handles = append(b.handles, h)
		if h.Info().Transport == "so" {
			direct, err := pluginrpc.DirectService[authv1.AuthServiceServer](h, "AuthService")
			if err != nil {
				return nil, fmt.Errorf("plugins.jwt: direct: %w", err)
			}
			b.Auth = direct
		} else {
			b.Auth = authv1.NewAuthServiceClient(h.Conn())
		}
	}
	if spec := cfg.Plugins.RateLimit; spec != nil {
		h, err := start("ratelimit", spec, nil)
		if err != nil {
			return nil, err
		}
		b.handles = append(b.handles, h)
		b.RateLimit = ratelimitv1.NewRateLimitServiceClient(h.Conn())
	}
	if spec := cfg.Plugins.Webhooks; spec != nil {
		h, err := start("webhooks", spec, cfg.Webhooks)
		if err != nil {
			return nil, err
		}
		b.handles = append(b.handles, h)
		b.Events = eventsv1.NewEventServiceClient(h.Conn())
	}
	if spec := cfg.Plugins.Discovery; spec != nil {
		h, err := start("discovery", spec, cfg.Discovery)
		if err != nil {
			return nil, err
		}
		b.handles = append(b.handles, h)
		b.Discovery = discoveryv1.NewDiscoveryServiceClient(h.Conn())
	}
	return b, nil
}

func (b *Backend) Stop(ctx context.Context) {
	if b == nil {
		return
	}
	for _, h := range b.handles {
		_ = h.Stop(ctx)
	}
}

func parseTransport(s string) (pluginrpc.Transport, error) {
	switch s {
	case "so":
		return pluginrpc.TransportSO, nil
	case "shared":
		return pluginrpc.TransportShared, nil
	case "fast":
		return pluginrpc.TransportFast, nil
	case "grpc", "":
		return pluginrpc.TransportGRPC, nil
	default:
		return 0, fmt.Errorf("unknown transport %q", s)
	}
}
```

- [ ] **Step 4: Прогнать тест и vet**

```bash
go test ./internal/plugins/
go vet ./internal/plugins/
```
Ожидаемо: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/
git commit -m "feat(plugins): рантайм pluginrpc-плагинов (Start/Stop, AuthVerifier)"
```

---

### Task 6: JWT-плагин (subprocess + .so)

**Files:**
- Create: `cmd/plugins/jwt/server.go`, `cmd/plugins/jwt/serve.go` (`//go:build !pluginmain`), `cmd/plugins/jwt/plugin.go` (`//go:build pluginmain`)
- Test: `cmd/plugins/jwt/plugin_test.go`

**Interfaces:**
- Consumes: `authv1`, `jwtutil`, `pluginrpc`, `config.JWTConfig`
- Produces:
  - экспортируемые символы `.so`: `AuthService authv1.AuthServiceServer`, `Name="jwt"`, `Version="0.1.0"`, `ProtocolVersion`, `Register(grpc.ServiceRegistrar)`
  - subprocess-бинарь: `pluginrpc.Serve`, конфиг `config.JWTConfig` из `pluginrpc.PluginConfig[config.JWTConfig]()`

- [ ] **Step 1: Написать тест, который собирает и гоняет плагин**

`cmd/plugins/jwt/plugin_test.go`:
```go
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/features/authv1"
	"github.com/sarnas-it/pluginrpc"
)

func buildSubprocess(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "jwt")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build plugin: %v\n%s", err, out)
	}
	return bin
}

func TestJWTServerValidate(t *testing.T) {
	bin := buildSubprocess(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	h, err := pluginrpc.Start(ctx, pluginrpc.Config{
		Path:         bin,
		Name:         "jwt",
		Transport:    pluginrpc.TransportGRPC,
		PluginConfig: config.JWTConfig{SecretKey: "benchsecret", Algorithm: "HS256", ValidateExp: true, ClaimMappings: []string{"id", "email"}},
		StartTimeout: 10 * time.Second,
		StopTimeout:  3 * time.Second,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
	})
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	defer h.Stop(context.Background())

	client := authv1.NewAuthServiceClient(h.Conn())
	resp, err := client.Validate(ctx, &authv1.ValidateRequest{Token: "not-a-token", Required: true})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if resp.GetOk() {
		t.Fatal("expected invalid token to be rejected")
	}
}
```

- [ ] **Step 2: Запустить — убедиться, что падает**

```bash
go test ./cmd/plugins/jwt/ -run TestJWTServerValidate -v
```
Ожидаемо: компиляция не проходит (нет пакета `main` с `func main`).

- [ ] **Step 3: Реализовать плагин**

`cmd/plugins/jwt/server.go` (без build-tag):
```go
package main

import (
	"context"
	"fmt"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/features/authv1"
	"github.com/basili4-1982/api-gateway/internal/jwtutil"
	"github.com/sarnas-it/pluginrpc"
	"google.golang.org/grpc"
)

type jwtServer struct {
	authv1.UnimplementedAuthServiceServer
	v             *jwtutil.JWTValidator
	claimMappings []string
}

func (s *jwtServer) Validate(_ context.Context, req *authv1.ValidateRequest) (*authv1.ValidateResponse, error) {
	if req.Token == "" {
		if req.Required {
			return &authv1.ValidateResponse{Ok: false, Error: "missing authorization token"}, nil
		}
		return &authv1.ValidateResponse{Ok: true}, nil
	}
	claims, err := s.v.ParseAndValidate(req.Token)
	if err != nil {
		if req.Required {
			return &authv1.ValidateResponse{Ok: false, Error: fmt.Sprintf("invalid token: %v", err)}, nil
		}
		return &authv1.ValidateResponse{Ok: true}, nil
	}
	if err := s.v.ValidateClaims(claims); err != nil {
		if req.Required {
			return &authv1.ValidateResponse{Ok: false, Error: fmt.Sprintf("invalid token claims: %v", err)}, nil
		}
		return &authv1.ValidateResponse{Ok: true}, nil
	}
	if err := jwtutil.CheckRoles(claims, req.AnyRoles, req.AllRoles); err != nil {
		return &authv1.ValidateResponse{Ok: false, Error: err.Error()}, nil
	}
	extracted := jwtutil.ExtractClaims(claims, s.claimMappings)
	resp := &authv1.ValidateResponse{Ok: true, HasClaims: true, Claims: make(map[string]string, len(extracted))}
	for k, v := range extracted {
		resp.Claims[k] = fmt.Sprintf("%v", v)
	}
	return resp, nil
}

func newServer() (*jwtServer, error) {
	jcfg, err := pluginrpc.PluginConfig[config.JWTConfig]()
	if err != nil {
		return nil, err
	}
	v, err := jwtutil.NewJWTValidator(
		jcfg.SecretKey, jcfg.Algorithm, jcfg.ValidateExp, jcfg.ValidateIss,
		jcfg.ExpectedIss, jcfg.ValidateAud, jcfg.ExpectedAud, jcfg.PublicKeyFile,
	)
	if err != nil {
		return nil, err
	}
	return &jwtServer{v: v, claimMappings: jcfg.ClaimMappings}, nil
}

func register(s grpc.ServiceRegistrar) {
	srv, err := newServer()
	if err != nil {
		panic(err)
	}
	authv1.RegisterAuthServiceServer(s, srv)
}
```

`cmd/plugins/jwt/serve.go` (`//go:build !pluginmain`):
```go
//go:build !pluginmain

package main

import (
	"fmt"
	"os"

	"github.com/sarnas-it/pluginrpc"
	"google.golang.org/grpc"
)

func main() {
	if err := pluginrpc.Serve(pluginrpc.ServeConfig{
		Name:    "jwt",
		Version: "0.1.0",
		Register: func(s grpc.ServiceRegistrar) {
			register(s)
		},
	}); err != nil {
		fmt.Fprintln(os.Stderr, "jwt plugin:", err)
		os.Exit(1)
	}
}
```

`cmd/plugins/jwt/plugin.go` (`//go:build pluginmain`):
```go
//go:build pluginmain

package main

import (
	"github.com/basili4-1982/api-gateway/internal/features/authv1"
	"github.com/sarnas-it/pluginrpc"
	"google.golang.org/grpc"
)

var (
	Name            = "jwt"
	Version         = "0.1.0"
	ProtocolVersion uint32
	AuthService     authv1.AuthServiceServer = mustServer()
)

func mustServer() authv1.AuthServiceServer {
	srv, err := newServer()
	if err != nil {
		panic(err)
	}
	return srv
}

func Register(s grpc.ServiceRegistrar) {
	authv1.RegisterAuthServiceServer(s, AuthService)
}

func init() {
	if err := pluginrpc.SOServe(pluginrpc.SOConfig{
		Name:            &Name,
		Version:         &Version,
		ProtocolVersion: &ProtocolVersion,
	}); err != nil {
		panic(err)
	}
}

func main() {}
```

- [ ] **Step 4: Прогнать тест**

```bash
go test ./cmd/plugins/jwt/ -v
```
Ожидаемо: PASS (тест собирает subprocess-плагин, стартует через gRPC-транспорт и проверяет отказ невалидного токена).

- [ ] **Step 5: Проверить сборку `.so`**

```bash
go build -buildmode=plugin -tags pluginmain -o /tmp/jwt.so ./cmd/plugins/jwt/
```
Ожидаемо: успех. Без cgo будет ошибка — тогда `CGO_ENABLED=1 go build ...` явно.

- [ ] **Step 6: Commit**

```bash
git add cmd/plugins/jwt/
git commit -m "feat(plugins): JWT-плагин (subprocess Serve + .so direct)"
```

---

### Task 7: Хост-адаптер JWT и проводка в `MultiProxy`

**Files:**
- Modify: `internal/proxy/multi_proxy.go` (поля `plugins`, `pluginCancel`, метод `Plugins()`, ветка в `modifyRequest`, `modifyRequestViaPlugin`, `NewMultiProxy`/`NewMultiProxyWithPlugins`, `Stop`)
- Test: `internal/proxy/plugin_jwt_test.go`

**Interfaces:**
- Consumes: `plugins.Backend`, `plugins.AuthVerifier`, `authv1`
- Produces:
  - `func (mp *MultiProxy) Plugins() *plugins.Backend`
  - `func NewMultiProxyWithPlugins(ctx context.Context, cfg *config.Config, logger *zap.Logger) (*MultiProxy, error)` — для тестов; при выключенных плагинах эквивалентен `NewMultiProxy`
  - `modifyRequest` при `mp.plugins.Auth != nil` идёт через `modifyRequestViaPlugin`

- [ ] **Step 1: Написать падающий тест (end-to-end через subprocess-плагин)**

`internal/proxy/plugin_jwt_test.go`:
```go
package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

func buildJWTPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "jwt")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/plugins/jwt")
	cmd.Dir = ".." // репозиторий — родитель internal/proxy
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build jwt plugin: %v\n%s", err, out)
	}
	return bin
}

func signToken(t *testing.T) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id": "42", "email": "a@b.c", "roles": []string{"user"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString([]byte("benchsecret"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMultiProxyJWTPlugin(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	bin := buildJWTPlugin(t)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{Enabled: true, JWT: &config.PluginSpec{Path: bin, Transport: "grpc"}},
		JWT:     config.JWTConfig{SecretKey: "benchsecret", Algorithm: "HS256", ValidateExp: true, Required: true},
		Targets: []config.TargetConfig{{Name: "b", URL: backend.URL}},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{{PathPrefix: "/", TargetName: "b"}}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mp, err := NewMultiProxyWithPlugins(ctx, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	defer mp.Stop(context.Background())

	tok := signToken(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	mp.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Authorization", "Bearer garbage")
	rec2 := httptest.NewRecorder()
	mp.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token: got %d, want 401", rec2.Code)
	}
}
```

- [ ] **Step 2: Запустить — убедиться, что падает**

```bash
go test ./internal/proxy/ -run TestMultiProxyJWTPlugin -v
```
Ожидаемо: `undefined: NewMultiProxyWithPlugins`.

- [ ] **Step 3: Реализовать проводку**

В `internal/proxy/multi_proxy.go`:

1) Добавить импорты `"errors"`, `"strconv"`, `"github.com/basili4-1982/api-gateway/internal/features/authv1"`, `"github.com/basili4-1982/api-gateway/internal/plugins"`.

2) Добавить поля после `publisher`:
```go
	plugins      *plugins.Backend
	pluginCancel context.CancelFunc
```

3) Переименовать тело `NewMultiProxy` в `newMultiProxy(cfg, logger, backend *plugins.Backend)` и добавить публичные конструкторы. **Порядок важен:** plugin-бэкенд стартует ДО построения хендлера и кладётся в `mp.plugins` сразу после создания `mp` — иначе `rebuildRouteConfigs` (rate limit) и блок publisher (webhooks) не увидят `mp.plugins`. Текущее тело `NewMultiProxy` (включая `rebuildRouteConfigs` и цепочку middleware) переносится в `newMultiProxy` без изменения логики, кроме: после `mp := &MultiProxy{...}` добавить `mp.plugins = backend` и `mp.pluginCancel = context.CancelFunc(func(){})`.
```go
// NewMultiProxy создает новый мульти-прокси сервер. Стартует plugin-бэкенд
// (context.Background), если в конфиге включены плагины.
func NewMultiProxy(cfg *config.Config, logger *zap.Logger) (*MultiProxy, error) {
	return NewMultiProxyWithPlugins(context.Background(), cfg, logger)
}

// NewMultiProxyWithPlugins — вариант NewMultiProxy с внешним ctx для pluginrpc.
func NewMultiProxyWithPlugins(ctx context.Context, cfg *config.Config, logger *zap.Logger) (*MultiProxy, error) {
	backend, err := plugins.Start(ctx, cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("start plugins: %w", err)
	}
	return newMultiProxy(cfg, logger, backend)
}

// newMultiProxy — исходное тело NewMultiProxy; backend передаётся до построения
// хендлера (нужен rebuildRouteConfigs и publisher'у).
func newMultiProxy(cfg *config.Config, logger *zap.Logger, backend *plugins.Backend) (*MultiProxy, error) {
	// ... исходное тело NewMultiProxy, с одной вставкой после создания mp:
	mp := &MultiProxy{
		targets:      make(map[string]*TargetProxy),
		routeByRule:  make(map[*config.RoutingRule]*RouteConfig),
		jwtValidator: jwtValidator,
		logger:       logger,
		metrics:      NewMetrics(cfg.MetricsEnabled),
	}
	mp.plugins = backend
	mp.pluginCancel = func() {}
	// ... дальше без изменений (initGlobalLimiter, createTargetProxy,
	// rebuildRouteConfigs, цепочка middleware, publisher-блок).
}

// Plugins возвращает запущенный plugin-бэкенд (nil, если плагины выключены).
func (mp *MultiProxy) Plugins() *plugins.Backend {
	return mp.plugins
}
```

4) В `modifyRequest` в самом начале добавить ветку:
```go
	if mp.plugins != nil && mp.plugins.Auth != nil {
		return mp.modifyRequestViaPlugin(r, targetCfg, rule)
	}
```

5) Добавить `modifyRequestViaPlugin` (копия логики `modifyRequest`, где блок JWT заменён на вызов плагина):
```go
func (mp *MultiProxy) modifyRequestViaPlugin(r *http.Request, targetCfg *config.TargetConfig, rule *config.RoutingRule) error {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		if c, err := r.Cookie("cml_access"); err == nil && c.Value != "" {
			authHeader = "Bearer " + c.Value
			r.Header.Set("Authorization", authHeader)
		}
	}

	cfg := mp.config.Load()
	authRequired := cfg.JWT.Required
	stripToken := cfg.Headers.StripAuthorization
	var anyRoles, allRoles []string
	if rule != nil && rule.Auth != nil {
		authRequired = rule.Auth.Required
		if rule.Auth.StripToken != nil {
			stripToken = *rule.Auth.StripToken
		}
		anyRoles, allRoles = rule.Auth.Roles, rule.Auth.RolesAll
	}

	if authHeader == "" && authRequired {
		return fmt.Errorf("missing authorization token")
	}

	if authHeader != "" {
		resp, err := mp.plugins.Auth.Validate(r.Context(), &authv1.ValidateRequest{
			Token: authHeader, Required: authRequired, AnyRoles: anyRoles, AllRoles: allRoles,
		})
		if err != nil {
			return fmt.Errorf("auth plugin error: %w", err)
		}
		if !resp.Ok {
			if resp.Error != "" {
				return errors.New(resp.Error)
			}
			return fmt.Errorf("authentication failed")
		}
		if resp.HasClaims {
			for claimName, headerName := range cfg.Headers.ClaimToHeader {
				if val, ok := resp.Claims[claimName]; ok {
					r.Header.Set(headerName, val)
				}
			}
			signHeader := cfg.Headers.SignHeader
			if signHeader != "" {
				if userIDStr, ok := resp.Claims["id"]; ok && userIDStr != "" {
					secret := cfg.Permissions.APIKey
					if secret != "" {
						mac := hmac.New(sha256.New, []byte(secret))
						mac.Write([]byte(userIDStr))
						sig := hex.EncodeToString(mac.Sum(nil))
						r.Header.Set(signHeader, sig)
					}
				}
			}
			if mp.permissionsManager != nil {
				if userIDStr, ok := resp.Claims["id"]; ok {
					if userID, err := strconv.Atoi(userIDStr); err == nil {
						if err := mp.permissionsManager.SetHeader(r, userID); err != nil {
							mp.logger.Warn("failed to set permissions header",
								zap.Int("user_id", userID), zap.Error(err))
						}
					}
				}
			}
		}
	}

	for header, value := range cfg.Headers.AddHeaders {
		r.Header.Set(header, value)
	}
	if stripToken {
		r.Header.Del("Authorization")
	}
	if clientIP := r.Header.Get("X-Forwarded-For"); clientIP == "" {
		r.Header.Set("X-Forwarded-For", r.RemoteAddr)
	}
	return nil
}
```

6) В `Stop` после `publisher.Close()` добавить:
```go
	if mp.pluginCancel != nil {
		mp.pluginCancel()
	}
	if mp.plugins != nil {
		mp.plugins.Stop(ctx)
	}
```

- [ ] **Step 4: Прогнать тесты proxy**

```bash
go test ./internal/proxy/ -run 'TestMultiProxyJWTPlugin' -v
go test ./internal/proxy/
```
Ожидаемо: PASS (новый и все старые).

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/multi_proxy.go internal/proxy/plugin_jwt_test.go
git commit -m "feat(proxy): JWT-валидация через плагин (modifyRequestViaPlugin)"
```

---
### Task 8: Rate-limit плагин и адаптер

**Files:**
- Create: `cmd/plugins/ratelimit/main.go`
- Modify: `internal/proxy/multi_proxy.go` (интерфейс `RateLimiter`, тип поля `RouteConfig.RateLimit`, `rebuildRouteConfigs`), `internal/proxy/plugin_ratelimit.go` (адаптер)
- Test: `internal/proxy/plugin_ratelimit_test.go`

**Interfaces:**
- Consumes: `ratelimitv1`, `proxy.NewIPRateLimiter`
- Produces:
  - `type RateLimiter interface { Allow(ip string) bool; Stop() }` (в пакете proxy)
  - `RouteConfig.RateLimit RateLimiter` (был `*IPRateLimiter`)
  - адаптер `pluginRateLimiter` реализует `RateLimiter` (fail closed)

- [ ] **Step 1: Написать падающий тест**

`internal/proxy/plugin_ratelimit_test.go`:
```go
package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"go.uber.org/zap"
)

func buildRateLimitPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "ratelimit")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/plugins/ratelimit")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build ratelimit plugin: %v\n%s", err, out)
	}
	return bin
}

func TestMultiProxyRateLimitPlugin(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	bin := buildRateLimitPlugin(t)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{Enabled: true, RateLimit: &config.PluginSpec{Path: bin, Transport: "grpc"}},
		Targets: []config.TargetConfig{{Name: "b", URL: backend.URL}},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{{
			PathPrefix: "/", TargetName: "b",
			RateLimit: &config.RateLimitRule{RequestsPerSecond: 1, Burst: 2},
		}}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mp, err := NewMultiProxyWithPlugins(ctx, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	defer mp.Stop(context.Background())

	statuses := make(map[int]int)
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		mp.ServeHTTP(rec, req)
		statuses[rec.Code]++
		time.Sleep(5 * time.Millisecond)
	}
	if statuses[http.StatusOK] == 0 {
		t.Fatalf("expected some 200s, got %v", statuses)
	}
	if statuses[http.StatusTooManyRequests] == 0 {
		t.Fatalf("expected some 429s (burst 2), got %v", statuses)
	}
}
```

- [ ] **Step 2: Запустить — убедиться, что падает**

```bash
go test ./internal/proxy/ -run TestMultiProxyRateLimitPlugin -v
```
Ожидаемо: компиляция не проходит (`cmd/plugins/ratelimit` не существует).

- [ ] **Step 3: Реализовать плагин**

`cmd/plugins/ratelimit/main.go`:
```go
package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/basili4-1982/api-gateway/internal/features/ratelimitv1"
	"github.com/basili4-1982/api-gateway/internal/proxy"
	"github.com/sarnas-it/pluginrpc"
	"google.golang.org/grpc"
)

type rateServer struct {
	ratelimitv1.UnimplementedRateLimitServiceServer
	mu       sync.Mutex
	limiters map[string]*proxy.IPRateLimiter
}

func (s *rateServer) Allow(_ context.Context, req *ratelimitv1.AllowRequest) (*ratelimitv1.AllowResponse, error) {
	s.mu.Lock()
	l := s.limiters[req.RouteKey]
	if l == nil {
		l = proxy.NewIPRateLimiter(req.Rate, int(req.Burst))
		s.limiters[req.RouteKey] = l
	}
	s.mu.Unlock()
	return &ratelimitv1.AllowResponse{Allowed: l.Allow(req.Ip)}, nil
}

func main() {
	if err := pluginrpc.Serve(pluginrpc.ServeConfig{
		Name:    "ratelimit",
		Version: "0.1.0",
		Register: func(s grpc.ServiceRegistrar) {
			ratelimitv1.RegisterRateLimitServiceServer(s, &rateServer{limiters: make(map[string]*proxy.IPRateLimiter)})
		},
	}); err != nil {
		fmt.Fprintln(os.Stderr, "ratelimit plugin:", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Реализовать адаптер и переключить тип поля**

`internal/proxy/plugin_ratelimit.go`:
```go
package proxy

import (
	"context"

	"github.com/basili4-1982/api-gateway/internal/features/ratelimitv1"
)

// RateLimiter — интерфейс лимитера роута. Реализуют встроенный IPRateLimiter
// и pluginRateLimiter (состояние в плагине).
type RateLimiter interface {
	Allow(ip string) bool
	Stop()
}

// pluginRateLimiter перекладывает решение о токене на плагин (fail closed).
type pluginRateLimiter struct {
	client ratelimitv1.RateLimitServiceClient
	key    string
	rate   float64
	burst  int
}

func newPluginRateLimiter(client ratelimitv1.RateLimitServiceClient, key string, rate float64, burst int) *pluginRateLimiter {
	return &pluginRateLimiter{client: client, key: key, rate: rate, burst: burst}
}

func (p *pluginRateLimiter) Allow(ip string) bool {
	resp, err := p.client.Allow(context.Background(), &ratelimitv1.AllowRequest{
		RouteKey: p.key, Ip: ip, Rate: p.rate, Burst: int64(p.burst),
	})
	if err != nil {
		return false
	}
	return resp.Allowed
}

func (p *pluginRateLimiter) Stop() {}
```

В `internal/proxy/multi_proxy.go`:
- `RouteConfig.RateLimit *IPRateLimiter` → `RouteConfig.RateLimit RateLimiter`.
- В `rebuildRouteConfigs` заменить блок создания лимитера:
```go
			if rule.RateLimit != nil {
				if mp.plugins != nil && mp.plugins.RateLimit != nil {
					rc.RateLimit = newPluginRateLimiter(mp.plugins.RateLimit, key, rule.RateLimit.RequestsPerSecond, rule.RateLimit.Burst)
				} else {
					rc.RateLimit = NewIPRateLimiter(rule.RateLimit.RequestsPerSecond, rule.RateLimit.Burst)
				}
			}
```
(`key := config.RuleRouteKey(*rule)` уже определён выше в цикле.)

- [ ] **Step 5: Прогнать тесты**

```bash
go test ./internal/proxy/ -run TestMultiProxyRateLimitPlugin -v
go test ./internal/proxy/ ./cmd/plugins/ratelimit/
```
Ожидаемо: PASS (200 и 429 присутствуют; старые тесты rate limit зелёные — тип поля не изменил поведение builtin).

- [ ] **Step 6: Commit**

```bash
git add cmd/plugins/ratelimit/ internal/proxy/plugin_ratelimit.go internal/proxy/multi_proxy.go internal/proxy/plugin_ratelimit_test.go
git commit -m "feat(plugins): rate-limit через плагин (stateful, fail closed)"
```

---
### Task 9: Events-плагин (webhooks/NATS доставка) и адаптер

**Files:**
- Create: `cmd/plugins/events/main.go`
- Modify: `internal/proxy/publisher.go` (метод `Deliver`, поле `eventsClient`, ветка в `publish`, `toProtoEvent`), `internal/proxy/multi_proxy.go` (установка `eventsClient`)
- Test: `internal/proxy/plugin_events_test.go`

**Interfaces:**
- Consumes: `eventsv1`, `proxy.Publisher`, `config.WebhookConfig`
- Produces:
  - `func (p *Publisher) Deliver(ctx context.Context, webhookName string, event AuditEvent)` — вызывает `publish` для вебхука по имени
  - `func (p *Publisher) setEventsClient(c eventsv1.EventServiceClient)`

- [ ] **Step 1: Написать падающий тест**

`internal/proxy/plugin_events_test.go`:
```go
package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"go.uber.org/zap"
)

func buildEventsPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "events")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/plugins/events")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build events plugin: %v\n%s", err, out)
	}
	return bin
}

func TestMultiProxyEventsPlugin(t *testing.T) {
	var received atomic.Int64
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	bin := buildEventsPlugin(t)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{Enabled: true, Webhooks: &config.PluginSpec{Path: bin, Transport: "grpc"}},
		Targets: []config.TargetConfig{{Name: "b", URL: backend.URL}},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{{PathPrefix: "/", TargetName: "b"}}},
		Webhooks: []config.WebhookConfig{{
			Name: "h", Transport: config.TransportWebhook, WebhookURL: sink.URL,
			Trigger: config.TriggerOnResponse, Async: true,
		}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mp, err := NewMultiProxyWithPlugins(ctx, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	defer mp.Stop(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mp.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if received.Load() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("webhook event not delivered to sink")
}
```

- [ ] **Step 2: Запустить — убедиться, что падает**

```bash
go test ./internal/proxy/ -run TestMultiProxyEventsPlugin -v
```
Ожидаемо: компиляция не проходит (`cmd/plugins/events` не существует).

- [ ] **Step 3: Реализовать плагин**

`cmd/plugins/events/main.go`:
```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/features/eventsv1"
	"github.com/basili4-1982/api-gateway/internal/proxy"
	"github.com/sarnas-it/pluginrpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type eventsServer struct {
	eventsv1.UnimplementedEventServiceServer
	pub *proxy.Publisher
}

func (s *eventsServer) Publish(ctx context.Context, req *eventsv1.PublishRequest) (*eventsv1.PublishResponse, error) {
	s.pub.Deliver(ctx, req.WebhookName, toAuditEvent(req.Event))
	return &eventsv1.PublishResponse{}, nil
}

func toAuditEvent(e *eventsv1.AuditEvent) proxy.AuditEvent {
	ev := proxy.AuditEvent{
		Method: e.Method, Path: e.Path, Query: e.Query,
		UserID: e.UserId, UserEmail: e.UserEmail, UserRoles: e.UserRoles,
		RequestID: e.RequestId, StatusCode: int(e.StatusCode),
		Timestamp: time.Unix(0, e.TimestampNanos),
		Headers:   e.Headers,
	}
	if len(e.Changes) > 0 {
		ev.Changes = json.RawMessage(e.Changes)
	}
	if len(e.ResponseBody) > 0 {
		ev.ResponseBody = json.RawMessage(e.ResponseBody)
	}
	return ev
}

func main() {
	whs, err := pluginrpc.PluginConfig[[]config.WebhookConfig]()
	if err != nil {
		fmt.Fprintln(os.Stderr, "events plugin:", err)
		os.Exit(1)
	}
	log, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pub, err := proxy.NewPublisher(&config.Config{Webhooks: whs}, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "events plugin:", err)
		os.Exit(1)
	}
	if err := pluginrpc.Serve(pluginrpc.ServeConfig{
		Name:    "webhooks",
		Version: "0.1.0",
		Register: func(s grpc.ServiceRegistrar) {
			eventsv1.RegisterEventServiceServer(s, &eventsServer{pub: pub})
		},
	}); err != nil {
		fmt.Fprintln(os.Stderr, "events plugin:", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Реализовать `Deliver`, `eventsClient`, `toProtoEvent` в Publisher**

В `internal/proxy/publisher.go`:
- Поле в `Publisher`: `eventsClient eventsv1.EventServiceClient` и импорт `"github.com/basili4-1982/api-gateway/internal/features/eventsv1"`.
- Методы:
```go
func (p *Publisher) setEventsClient(c eventsv1.EventServiceClient) {
	p.eventsClient = c
}

// Deliver отправляет уже построенное событие конкретному вебхуку по имени.
// Используется плагинами, у которых свой Publisher (доставка: батчинг/NATS/HTTP).
func (p *Publisher) Deliver(ctx context.Context, webhookName string, event AuditEvent) {
	for _, wh := range p.webhooks {
		if wh.Name == webhookName {
			p.publish(ctx, wh, event)
			return
		}
	}
}

func toProtoEvent(e AuditEvent) *eventsv1.AuditEvent {
	return &eventsv1.AuditEvent{
		Method: e.Method, Path: e.Path, Query: e.Query,
		UserId: e.UserID, UserEmail: e.UserEmail, UserRoles: e.UserRoles,
		RequestId: e.RequestID, StatusCode: int32(e.StatusCode),
		TimestampNanos: e.Timestamp.UnixNano(),
		Headers:        e.Headers,
		Changes:        e.Changes,
		ResponseBody:   e.ResponseBody,
	}
}
```
- В начале `publish`:
```go
	if p.eventsClient != nil {
		if _, err := p.eventsClient.Publish(ctx, &eventsv1.PublishRequest{
			WebhookName: wh.Name,
			Event:       toProtoEvent(event),
		}); err != nil {
			p.log.Error("failed to publish event via plugin", zap.Error(err), zap.String("webhook", wh.Name))
		}
		return
	}
```

В `internal/proxy/multi_proxy.go` после создания publisher (блок `if len(cfg.Webhooks) > 0`):
```go
		if mp.plugins != nil && mp.plugins.Events != nil {
			publisher.setEventsClient(mp.plugins.Events)
			logger.Info("Webhook delivery delegated to plugin")
		}
```

- [ ] **Step 5: Прогнать тесты**

```bash
go test ./internal/proxy/ -run TestMultiProxyEventsPlugin -v
go test ./internal/proxy/ ./cmd/plugins/events/
```
Ожидаемо: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/plugins/events/ internal/proxy/publisher.go internal/proxy/multi_proxy.go internal/proxy/plugin_events_test.go
git commit -m "feat(plugins): webhooks/NATS доставка через плагин (Publisher.Deliver)"
```

---
### Task 10: Discovery-плагин и host-провайдер

**Files:**
- Create: `cmd/plugins/discovery/main.go`, `internal/discovery/plugin_provider.go`
- Modify: `internal/discovery/docker_provider.go` (экспорт `NewDockerProvider`), `internal/discovery/manager.go` (экспорт `SetProvider`)
- Test: `internal/discovery/plugin_provider_test.go`

**Interfaces:**
- Consumes: `discoveryv1`, `discovery.Provider`, `config.DiscoveryConfig`
- Produces:
  - `func NewDockerProvider(host, apiVersion string, opts ParseOptions, debounce, resync time.Duration, log *zap.Logger) (Provider, error)` (обёртка над `newDockerProvider`)
  - `func (m *Manager) SetProvider(p Provider)` (обёртка над `setProvider`)
  - `func NewPluginProvider(client discoveryv1.DiscoveryServiceClient) Provider` — хост-сторона: `Watch`-стрим → `onResult`

- [ ] **Step 1: Написать падающий тест (маппинг proto ↔ Result)**

`internal/discovery/plugin_provider_test.go`:
```go
package discovery

import (
	"testing"

	"github.com/basili4-1982/api-gateway/internal/config"
)

func TestResultProtoRoundtrip(t *testing.T) {
	in := Result{
		Targets: []config.TargetConfig{{Name: "a", URL: "http://x:1", PathPrefix: "/api", StripPrefix: true}},
		Rules:   []config.RoutingRule{{Host: "h", PathPrefix: "/", TargetName: "a"}},
	}
	msg := resultToProto(in)
	out := resultFromProto(msg)
	if len(out.Targets) != 1 || out.Targets[0].Name != "a" {
		t.Fatalf("targets mismatch: %+v", out.Targets)
	}
	if len(out.Rules) != 1 || out.Rules[0].TargetName != "a" {
		t.Fatalf("rules mismatch: %+v", out.Rules)
	}
}
```

- [ ] **Step 2: Запустить — убедиться, что падает**

```bash
go test ./internal/discovery/ -run TestResultProtoRoundtrip -v
```
Ожидаемо: `undefined: resultToProto`.

- [ ] **Step 3: Экспортировать конструктор и SetProvider**

В `internal/discovery/docker_provider.go` (добавить импорт `"time"`, если нет):
```go
// NewDockerProvider создаёт Docker-провайдер. Обёртка над newDockerProvider,
// экспортированная для использования плагинами discovery.
func NewDockerProvider(host, apiVersion string, opts ParseOptions, debounce, resync time.Duration, log *zap.Logger) (Provider, error) {
	return newDockerProvider(host, apiVersion, opts, debounce, resync, log)
}
```

В `internal/discovery/manager.go`:
```go
// SetProvider заменяет провайдера discovery (например, на plugin-провайдер).
// Вызывать до Start.
func (m *Manager) SetProvider(p Provider) {
	m.setProvider(p)
}
```

- [ ] **Step 4: Реализовать маппинг и host-провайдер**

`internal/discovery/plugin_provider.go`:
```go
package discovery

import (
	"context"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/features/discoveryv1"
)

// pluginProvider — Provider поверх pluginrpc-стрима: хост тянет
// DiscoveryResult'ы и отдаёт их через onResult.
type pluginProvider struct {
	client discoveryv1.DiscoveryServiceClient
	cancel context.CancelFunc
}

func NewPluginProvider(client discoveryv1.DiscoveryServiceClient) Provider {
	return &pluginProvider{client: client}
}

func (p *pluginProvider) Start(ctx context.Context, onResult func(Result)) error {
	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	defer cancel()

	stream, err := p.client.Watch(ctx, &discoveryv1.WatchRequest{})
	if err != nil {
		return err
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		onResult(resultFromProto(msg))
	}
}

func (p *pluginProvider) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

func resultToProto(r Result) *discoveryv1.DiscoveryResult {
	out := &discoveryv1.DiscoveryResult{
		Targets: make([]*discoveryv1.Target, 0, len(r.Targets)),
		Rules:   make([]*discoveryv1.Rule, 0, len(r.Rules)),
	}
	for _, t := range r.Targets {
		wt := int32(0)
		if t.Weight != nil {
			wt = int32(*t.Weight)
		}
		out.Targets = append(out.Targets, &discoveryv1.Target{
			Name: t.Name, Url: t.URL, PathPrefix: t.PathPrefix,
			StripPrefix: t.StripPrefix, Weight: wt, HealthCheck: t.HealthCheck,
		})
	}
	for _, rl := range r.Rules {
		out.Rules = append(out.Rules, &discoveryv1.Rule{
			Host: rl.Host, PathPrefix: rl.PathPrefix, TargetName: rl.TargetName,
			Methods: rl.Methods, StripPath: rl.StripPath,
		})
	}
	return out
}

func resultFromProto(m *discoveryv1.DiscoveryResult) Result {
	out := Result{
		Targets: make([]config.TargetConfig, 0, len(m.Targets)),
		Rules:   make([]config.RoutingRule, 0, len(m.Rules)),
	}
	for _, t := range m.Targets {
		var w *int
		if t.Weight != 0 {
			wi := int(t.Weight)
			w = &wi
		}
		out.Targets = append(out.Targets, config.TargetConfig{
			Name: t.Name, Url: t.Url, PathPrefix: t.PathPrefix,
			StripPrefix: t.StripPrefix, Weight: w, HealthCheck: t.HealthCheck,
		})
	}
	for _, rl := range m.Rules {
		out.Rules = append(out.Rules, config.RoutingRule{
			Host: rl.Host, PathPrefix: rl.PathPrefix, TargetName: rl.TargetName,
			Methods: rl.Methods, StripPath: rl.StripPath,
		})
	}
	return out
}
```

- [ ] **Step 5: Реализовать discovery-плагин**

`cmd/plugins/discovery/main.go`:
```go
package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/discovery"
	"github.com/basili4-1982/api-gateway/internal/features/discoveryv1"
	"github.com/sarnas-it/pluginrpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type discoveryServer struct {
	discoveryv1.UnimplementedDiscoveryServiceServer
	mu   sync.Mutex
	dcfg *config.DiscoveryConfig
}

func (s *discoveryServer) Watch(_ *discoveryv1.WatchRequest, stream grpc.ServerStreamingServer[discoveryv1.DiscoveryResult]) error {
	log, _ := zap.NewProduction()
	provider, err := discovery.NewDockerProvider(
		s.dcfg.Host, s.dcfg.APIVersion,
		discovery.ParseOptions{
			LabelPrefix:       s.dcfg.LabelPrefix,
			ServiceNameLabels: s.dcfg.ServiceNameLabels,
			DefaultTimeout:    s.dcfg.DefaultTimeout,
			Network:           s.dcfg.Network,
		},
		s.dcfg.Debounce, s.dcfg.ResyncInterval, log,
	)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	defer provider.Stop()
	return provider.Start(ctx, func(r discovery.Result) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if serr := stream.Send(discoveryResultToProto(r)); serr != nil {
			cancel()
		}
	})
}

func discoveryResultToProto(r discovery.Result) *discoveryv1.DiscoveryResult {
	out := &discoveryv1.DiscoveryResult{
		Targets: make([]*discoveryv1.Target, 0, len(r.Targets)),
		Rules:   make([]*discoveryv1.Rule, 0, len(r.Rules)),
	}
	for _, t := range r.Targets {
		wt := int32(0)
		if t.Weight != nil {
			wt = int32(*t.Weight)
		}
		out.Targets = append(out.Targets, &discoveryv1.Target{
			Name: t.Name, Url: t.URL, PathPrefix: t.PathPrefix,
			StripPrefix: t.StripPrefix, Weight: wt, HealthCheck: t.HealthCheck,
		})
	}
	for _, rl := range r.Rules {
		out.Rules = append(out.Rules, &discoveryv1.Rule{
			Host: rl.Host, PathPrefix: rl.PathPrefix, TargetName: rl.TargetName,
			Methods: rl.Methods, StripPath: rl.StripPath,
		})
	}
	return out
}

func main() {
	dcfg, err := pluginrpc.PluginConfig[config.DiscoveryConfig]()
	if err != nil {
		fmt.Fprintln(os.Stderr, "discovery plugin:", err)
		os.Exit(1)
	}
	if err := pluginrpc.Serve(pluginrpc.ServeConfig{
		Name:    "discovery",
		Version: "0.1.0",
		Register: func(s grpc.ServiceRegistrar) {
			discoveryv1.RegisterDiscoveryServiceServer(s, &discoveryServer{dcfg: &dcfg})
		},
	}); err != nil {
		fmt.Fprintln(os.Stderr, "discovery plugin:", err)
		os.Exit(1)
	}
}
```
Примечание: `discoveryResultToProto` продублирована в пакете `main` (в `internal/discovery` она unexported) — допустимо для эксперимента.

- [ ] **Step 6: Прогнать тесты и сборку**

```bash
go test ./internal/discovery/
go build ./cmd/plugins/discovery/
```
Ожидаемо: PASS / успешная сборка.

- [ ] **Step 7: Commit**

```bash
git add cmd/plugins/discovery/ internal/discovery/plugin_provider.go internal/discovery/docker_provider.go internal/discovery/manager.go internal/discovery/plugin_provider_test.go
git commit -m "feat(plugins): discovery через плагин (Watch-stream, host-провайдер)"
```

---

### Task 11: Проводка в `cmd/main.go` и корректный shutdown

**Files:**
- Modify: `cmd/main.go`

**Interfaces:**
- Consumes: `proxy.MultiProxy.Plugins()`, `discovery.NewPluginProvider`, `discovery.Manager.SetProvider`
- Produces: гейтвей с discovery-плагином; остановка плагинов при shutdown (уже в `mp.Stop` из Task 7).

- [ ] **Step 1: Проводка discovery-плагина**

Заменить в `cmd/main.go` блок создания менеджера (строки 77–83) на:
```go
	var mgr *discovery.Manager
	if be := p.Plugins(); be != nil && be.Discovery != nil {
		noDisc := *cfg
		noDisc.Discovery = nil // менеджер не строит встроенный docker-провайдер
		mgr, err = discovery.NewManager(&noDisc, log, func(updated *config.Config) error {
			return p.Reload(updated)
		})
		if err != nil {
			log.Error("Failed to create discovery manager", zap.Error(err))
			os.Exit(1)
		}
		mgr.SetProvider(discovery.NewPluginProvider(be.Discovery))
	} else {
		mgr, err = discovery.NewManager(cfg, log, func(updated *config.Config) error {
			return p.Reload(updated)
		})
		if err != nil {
			log.Error("Failed to create discovery manager", zap.Error(err))
			os.Exit(1)
		}
	}
```

- [ ] **Step 2: Проверка сборки и vet**

```bash
go build ./...
go vet ./...
```
Ожидаемо: успех.

- [ ] **Step 3: Commit**

```bash
git add cmd/main.go
git commit -m "feat(cmd): проводка discovery-плагина в main"
```

---
### Task 12: Smoke-тест (build + end-to-end)

**Files:**
- Create: `scripts/smoke-plugins.sh`
- Create: `benchmarks/2026-09-plugins-experiment/configs/smoke-jwt-so.yaml`

**Interfaces:**
- Consumes: готовые плагины, `make build`
- Produces: ручная проверка end-to-end работы plugin-режима (включая `.so`).

- [ ] **Step 1: Написать конфиг для smoke**

`benchmarks/2026-09-plugins-experiment/configs/smoke-jwt-so.yaml`:
```yaml
server:
  port: 18080
application:
  env: "prod"
  health_check: false
  circuit_breaker: false
  metrics_enabled: false
plugins:
  enabled: true
  jwt:
    path: bin/plugins/jwt.so
    transport: so
jwt:
  required: true
  secret_key: "benchsecret"
  algorithm: "HS256"
  validate_exp: true
targets:
  - name: "backend"
    url: "http://127.0.0.1:19901"
    timeout: 30s
routing:
  rules:
    - path_prefix: "/"
      target_name: "backend"
logging:
  level: "error"
  access_log: false
```

- [ ] **Step 2: Написать скрипт smoke**

`scripts/smoke-plugins.sh`:
```bash
#!/usr/bin/env bash
# Smoke-тест plugin-режима: собирает плагины, запускает гейтвей с .so-JWT и
# прогоняет валидный/невалидный токен.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> build plugins"
make plugin-build
make plugin-so-build

echo "==> start fake backend on :19901"
python3 - <<'PY' &
from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers(); self.wfile.write(b"ok")
    def log_message(self, *a): pass
HTTPServer(("127.0.0.1", 19901), H).serve_forever()
PY
BACKEND_PID=$!
trap "kill $BACKEND_PID 2>/dev/null || true" EXIT
sleep 0.5

echo "==> start gateway (jwt .so)"
./bin/api-gateway -config benchmarks/2026-09-plugins-experiment/configs/smoke-jwt-so.yaml &
GW_PID=$!
trap "kill $GW_PID $BACKEND_PID 2>/dev/null || true" EXIT
for i in $(seq 1 50); do
  if curl -s -o /dev/null http://127.0.0.1:18080/; then break; fi
  sleep 0.2
done

TOKEN=$(python3 - <<'PY'
import hmac, hashlib, base64, json, time
def b64(b): return base64.urlsafe_b64encode(b).rstrip(b"=")
h = b64(json.dumps({"alg":"HS256","typ":"JWT"},separators=(",",":")).encode())
p = b64(json.dumps({"id":"42","exp":int(time.time())+3600},separators=(",",":")).encode())
sig = b64(hmac.new(b"benchsecret", h+b"."+p, hashlib.sha256).digest())
print((h+b"."+p+b"."+sig).decode())
PY
)

echo "==> valid token"
code=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $TOKEN" http://127.0.0.1:18080/)
test "$code" = "200" || { echo "FAIL valid token: got $code"; exit 1; }

echo "==> invalid token"
code=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer garbage" http://127.0.0.1:18080/)
test "$code" = "401" || { echo "FAIL invalid token: got $code"; exit 1; }

echo "SMOKE OK"
```

- [ ] **Step 3: Прогнать smoke**

```bash
chmod +x scripts/smoke-plugins.sh
make build
./scripts/smoke-plugins.sh
```
Ожидаемо: `SMOKE OK` (валидный токен → 200, невалидный → 401). Если `.so` не грузится без cgo — собрать `bin/api-gateway` с `CGO_ENABLED=1` (на машине дефолт).

- [ ] **Step 4: Commit**

```bash
git add scripts/smoke-plugins.sh benchmarks/2026-09-plugins-experiment/configs/smoke-jwt-so.yaml
git commit -m "test: smoke-проверка plugin-режима гейтвея (JWT .so)"
```

---
### Task 13: Бенчмарк-харнесс — конфиги и helper'ы

**Files:**
- Create: `benchmarks/2026-09-plugins-experiment/helpers/backend/main.go`
- Create: `benchmarks/2026-09-plugins-experiment/helpers/webhook/main.go`
- Create: `benchmarks/2026-09-plugins-experiment/configs/{baseline-jwt,plugin-jwt-so,plugin-jwt-shared,baseline-ratelimit,plugin-ratelimit-shared,baseline-webhooks,plugin-webhooks-fast,baseline-discovery,plugin-discovery-fast}.yaml`

**Interfaces:**
- Consumes: —
- Produces: конфиги сценариев (транспорты — см. Global Constraints), бэкенд `:19901`, вебхук-синк `:19902`.

- [ ] **Step 1: Хелперы**

`benchmarks/2026-09-plugins-experiment/helpers/backend/main.go`:
```go
package main

import (
	"fmt"
	"net/http"
)

func main() {
	// ~200-байтовый ответ, чтобы метрика отражала накладные расходы гейтвея.
	body := make([]byte, 200)
	for i := range body {
		body[i] = 'a'
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	fmt.Println("backend on :19901")
	panic(http.ListenAndServe("127.0.0.1:19901", nil))
}
```

`benchmarks/2026-09-plugins-experiment/helpers/webhook/main.go`:
```go
package main

import (
	"fmt"
	"io"
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})
	fmt.Println("webhook sink on :19902")
	panic(http.ListenAndServe("127.0.0.1:19902", nil))
}
```

- [ ] **Step 2: Конфиги**

`configs/baseline-jwt.yaml` (образец; остальные — по образцу из существующего сравнения + секция `plugins`):
```yaml
server:
  port: 18080
application:
  env: "prod"
  health_check: false
  circuit_breaker: false
  metrics_enabled: false
jwt:
  required: true
  secret_key: "benchsecret"
  algorithm: "HS256"
  validate_exp: true
targets:
  - name: "backend"
    url: "http://127.0.0.1:19901"
    timeout: 30s
routing:
  rules:
    - path_prefix: "/"
      target_name: "backend"
logging:
  level: "error"
  access_log: false
```

Создать по образцу:
- `baseline-jwt.yaml` (выше), `plugin-jwt-so.yaml` (та же + `plugins.jwt {path: bin/plugins/jwt.so, transport: so}`), `plugin-jwt-shared.yaml` (та же + `plugins.jwt {path: bin/plugins/jwt, transport: shared}`).
- `baseline-ratelimit.yaml` = baseline-jwt БЕЗ jwt + правило с `rate_limit: {requests_per_second: 100000, burst: 100000}` (никогда не срабатывает); `plugin-ratelimit-shared.yaml` = то же + `plugins.ratelimit {path: bin/plugins/ratelimit, transport: shared}`.
- `baseline-webhooks.yaml` = baseline-jwt БЕЗ jwt + вебхук `{name: h, transport: webhook, webhook_url: http://127.0.0.1:19902/, trigger: on_response, async: true, batch_size: 20, flush_interval: 200ms}`; `plugin-webhooks-fast.yaml` = то же + `plugins.webhooks {path: bin/plugins/events, transport: fast}`.
- `baseline-discovery.yaml` = baseline-jwt БЕЗ jwt + `discovery: {enabled: true, host: "unix:///var/run/docker.sock", label_prefix: gateway, debounce: 1s, resync_interval: 1m}`; `plugin-discovery-fast.yaml` = то же + `plugins.discovery {path: bin/plugins/discovery, transport: fast}`.

- [ ] **Step 3: Проверить, что все конфиги валидны**

```bash
go run ./cmd/ -config benchmarks/2026-09-plugins-experiment/configs/baseline-jwt.yaml -check
for f in benchmarks/2026-09-plugins-experiment/configs/plugin-*.yaml; do
  go run ./cmd/ -config "$f" -check
done
```
Ожидаемо: exit 0 для всех (discovery-конфиги проходят check без docker).

- [ ] **Step 4: Commit**

```bash
git add benchmarks/2026-09-plugins-experiment/helpers benchmarks/2026-09-plugins-experiment/configs
git commit -m "test: конфиги и helper'ы бенчмарк-сценариев плагинов"
```

---
### Task 14: Бенчмарк-харнесс — wrk-скрипт

**Files:**
- Create: `benchmarks/2026-09-plugins-experiment/scripts/run-bench.sh`
- Create: `benchmarks/2026-09-plugins-experiment/scripts/common.sh`

**Interfaces:**
- Consumes: конфиги из Task 13, `bin/api-gateway`, `bin/plugins/*`, wrk
- Produces: CSV-вывод `benchmarks/2026-09-plugins-experiment/results/<scenario>.csv` с прогонами wrk (req/s + латентность).

- [ ] **Step 1: Написать `common.sh`**

`benchmarks/2026-09-plugins-experiment/scripts/common.sh`:
```bash
#!/usr/bin/env bash
# Общие функции прогона бенчмарка: старт/стоп гейтвея, backend, wrk-прогон.
set -euo pipefail

GATEWAY_BIN=${GATEWAY_BIN:-./bin/api-gateway}
BACKEND_BIN=${BACKEND_BIN:-./bin/bench-backend}
SINK_BIN=${SINK_BIN:-./bin/bench-sink}
CONFIGS=${CONFIGS:-benchmarks/2026-09-plugins-experiment/configs}
RESULTS=${RESULTS:-benchmarks/2026-09-plugins-experiment/results}
mkdir -p "$RESULTS"

GW_PID=""
BACKEND_PID=""
SINK_PID=""

start_backend() {
  "$BACKEND_BIN" >/tmp/bench-backend.log 2>&1 &
  BACKEND_PID=$!
}

start_sink() {
  "$SINK_BIN" >/tmp/bench-sink.log 2>&1 &
  SINK_PID=$!
}

start_gateway() {
  local cfg="$1"
  "$GATEWAY_BIN" -config "$CONFIGS/$cfg" >/tmp/bench-gateway.log 2>&1 &
  GW_PID=$!
  for _ in $(seq 1 100); do
    if curl -s -o /dev/null "http://127.0.0.1:18080/"; then
      return 0
    fi
    sleep 0.1
  done
  echo "gateway did not start for $cfg; log:" >&2
  cat /tmp/bench-gateway.log >&2 || true
  return 1
}

stop_all() {
  [ -n "$GW_PID" ] && kill "$GW_PID" 2>/dev/null || true
  [ -n "$SINK_PID" ] && kill "$SINK_PID" 2>/dev/null || true
  [ -n "$BACKEND_PID" ] && kill "$BACKEND_PID" 2>/dev/null || true
  sleep 0.5
}

# wrk_req <scenario> <connections> <threads> <duration> <runs> — N прогонов,
# печатает строки "reqs,lat_p50,lat_p99" в CSV.
wrk_req() {
  local scenario="$1" conns="$2" threads="$3" dur="$4" runs="$5"
  local out="$RESULTS/$scenario.csv"
  : > "$out"
  for _ in $(seq 1 "$runs"); do
    local line
    line=$(wrk -t"$threads" -c"$conns" -d"$dur" "http://127.0.0.1:18080/" 2>/dev/null |
      awk '
        /Requests\/sec:/ { r=$2 }
        /Latency/ { getline }
        /p50/ { p50=$2 }
        /p99/ { p99=$2 }
        END { printf "%s,%s,%s\n", r, p50, p99 }
      ')
    echo "$line" >> "$out"
  done
  echo "== $scenario (c$conns, $runs runs) =="
  awk -F, '{s+=$1} END {printf "  avg req/s: %.0f\n", s/NR}' "$out"
}
```

- [ ] **Step 2: Написать `run-bench.sh`**

`benchmarks/2026-09-plugins-experiment/scripts/run-bench.sh`:
```bash
#!/usr/bin/env bash
# Прогон бенчмарка: baseline vs plugin по сценариям. На ноутбуке прогоны
# шумные, поэтому best-of-N (по умолчанию 5) с чередованием не нужно —
# шум фиксируем отдельно (baseline vs baseline) и сравниваем медианы.
set -euo pipefail
cd "$(dirname "$0")/../../.."
source benchmarks/2026-09-plugins-experiment/scripts/common.sh

RUNS=${RUNS:-5}
DUR=${DUR:-15s}
C50="${C50:--t2 -c50}"; C300="${C300:--t4 -c300}"

echo "==> build gateway and helpers"
make build
go build -o bin/bench-backend ./benchmarks/2026-09-plugins-experiment/helpers/backend/
go build -o bin/bench-sink ./benchmarks/2026-09-plugins-experiment/helpers/webhook/
make plugin-build
make plugin-so-build

# wrk в этом скрипте зовётся с фиксированными параметрами; сценарии ниже
# используют common.wrk_req (см. Task 14, Step 1).
run_scenario() {
  local scenario="$1"
  stop_all
  case "$scenario" in
    *webhooks*) start_sink ;;
  esac
  start_backend
  start_gateway "$scenario.yaml"
  wrk_req "$scenario" 50 2 "$DUR" "$RUNS"
  wrk_req "$scenario" 300 4 "20s" "$RUNS"
  stop_all
}

# 0) Noise floor: baseline против самого себя (спред).
run_scenario baseline-jwt

# 1) JWT: builtin vs so vs shared.
run_scenario baseline-jwt
run_scenario plugin-jwt-so
run_scenario plugin-jwt-shared

# 2) Rate limit: builtin vs shared.
run_scenario baseline-ratelimit
run_scenario plugin-ratelimit-shared

# 3) Webhooks: builtin vs fast.
run_scenario baseline-webhooks
run_scenario plugin-webhooks-fast

# 4) Discovery: builtin vs fast (вне hot-path).
run_scenario baseline-discovery
run_scenario plugin-discovery-fast

echo "DONE. Results in benchmarks/2026-09-plugins-experiment/results/"
```

- [ ] **Step 3: Проверить, что скрипт запускается (короткий smoke-прогон)**

```bash
chmod +x benchmarks/2026-09-plugins-experiment/scripts/common.sh benchmarks/2026-09-plugins-experiment/scripts/run-bench.sh
RUNS=2 DUR=5s ./benchmarks/2026-09-plugins-experiment/scripts/run-bench.sh
```
Ожидаемо: скрипт отрабатывает все сценарии, `results/*.csv` появляются. Внимание: полный прогон (RUNS=5, DUR=15s) займёт существенное время — запускать осознанно (Task 16).

- [ ] **Step 4: Commit**

```bash
git add benchmarks/2026-09-plugins-experiment/scripts
git commit -m "test: wrk-скрипты бенчмарка плагинов"
```

---
### Task 15: Go-бенчмарк hot-path с benchmem (аллокации)

**Files:**
- Create: `benchmarks/2026-09-plugins-experiment/bench_test.go`

**Interfaces:**
- Consumes: `config.Load`, `proxy.NewMultiProxy`, плагины, `jwt`
- Produces: `BenchmarkGateway` — прогоняет запросы через реальный `MultiProxy`, конфиг из `GATEWAY_BENCH_CONFIG`; печатает ns/op, B/op, allocs/op. Меряет также p50/p99 латентность через ручной сбор статистики.

- [ ] **Step 1: Написать бенчмарк**

`benchmarks/2026-09-plugins-experiment/bench_test.go`:
```go
package experiment

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/proxy"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

// BenchmarkGateway гоняет реальный MultiProxy с конфигом из GATEWAY_BENCH_CONFIG.
// Запуск:
//
//	GATEWAY_BENCH_CONFIG=configs/baseline-jwt.yaml go test -bench BenchmarkGateway -benchmem -count=5 ./benchmarks/2026-09-plugins-experiment/
func BenchmarkGateway(b *testing.B) {
	path := os.Getenv("GATEWAY_BENCH_CONFIG")
	if path == "" {
		b.Skip("GATEWAY_BENCH_CONFIG not set")
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		b.Fatalf("load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mp, err := proxy.NewMultiProxy(cfg, zap.NewNop())
	if err != nil {
		b.Fatalf("new proxy: %v", err)
	}
	defer mp.Stop(context.Background())

	// Токен для сценариев с JWT (в конфигах без JWT он игнорируется).
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id": "42", "email": "a@b.c", "exp": time.Now().Add(time.Hour).Unix(),
	})
	signed, _ := tok.SignedString([]byte("benchsecret"))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+signed)

	// Прогрев.
	for i := 0; i < 1000; i++ {
		rec := httptest.NewRecorder()
		mp.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("warmup: unexpected status %d", rec.Code)
		}
	}

	b.ResetTimer()
	var lat []time.Duration
	b.RunParallel(func(pb *testing.PB) {
		start := time.Now()
		for pb.Next() {
			rec := httptest.NewRecorder()
			mp.ServeHTTP(rec, req)
			lat = append(lat, time.Since(start))
			if rec.Code != http.StatusOK {
				b.Fatalf("unexpected status %d", rec.Code)
			}
		}
	})
	b.StopTimer()

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	if n := len(lat); n > 0 {
		fmt.Fprintf(b, "  p50=%s p99=%s\n", lat[n/2], lat[n*99/100])
	}
}
```
Примечание: `append` в `RunParallel` без мьютекса — для латентности грубо, но для бенчмарка-прикидки допустимо (только чтение после завершения). Если нужна строгость — заменить на атомарный счётчик + duration-массив с мьютексом.

- [ ] **Step 2: Проверить сборку и запустить для одного сценария**

```bash
GATEWAY_BENCH_CONFIG=benchmarks/2026-09-plugins-experiment/configs/baseline-jwt.yaml go test -run '^$' -bench BenchmarkGateway -benchmem -count=3 -benchtime=2s ./benchmarks/2026-09-plugins-experiment/
```
Ожидаемо: успешный прогон, вывод ns/op, B/op, allocs/op. (Backend должен быть запущен — см. Task 14/16.)

- [ ] **Step 3: Commit**

```bash
git add benchmarks/2026-09-plugins-experiment/bench_test.go
git commit -m "test: Go-бенчмарк hot-path гейтвея с benchmem (аллокации)"
```

---

### Task 16: Прогнать бенчмарки и собрать цифры

**Files:**
- Create: `benchmarks/2026-09-plugins-experiment/results/` (CSV wrk + заметки прогона)

**Interfaces:**
- Consumes: Task 14 (wrk), Task 15 (Go-bench)
- Produces: сырые числа для отчёта (Task 17).

- [ ] **Step 1: Полный wrk-прогон**

```bash
RUNS=5 DUR=15s ./benchmarks/2026-09-plugins-experiment/scripts/run-bench.sh
```
Ожидаемо: `results/baseline-jwt.csv`, `results/plugin-jwt-so.csv`, `results/plugin-jwt-shared.csv`, `results/baseline-ratelimit.csv`, `results/plugin-ratelimit-shared.csv`, `results/baseline-webhooks.csv`, `results/plugin-webhooks-fast.csv`, `results/baseline-discovery.csv`, `results/plugin-discovery-fast.csv`.

- [ ] **Step 2: Go-бенчмарк по каждому сценарию (для allocs)**

```bash
for c in baseline-jwt plugin-jwt-so plugin-jwt-shared; do
  GATEWAY_BENCH_CONFIG=benchmarks/2026-09-plugins-experiment/configs/$c.yaml \
    go test -run '^$' -bench BenchmarkGateway -benchmem -count=5 -benchtime=2s \
    ./benchmarks/2026-09-plugins-experiment/ 2>&1 | tee benchmarks/2026-09-plugins-experiment/results/$c.bench
done
```
(Backend поднят из wrk-скрипта; при необходимости запустить отдельно `./bin/bench-backend &`.)

- [ ] **Step 3: Зафиксировать итоговые цифры**

Свести медианы по `results/*.csv` и `*.bench` в `benchmarks/2026-09-plugins-experiment/results/summary.md` (промежуточно; финальный формат — отчёт Task 17).

- [ ] **Step 4: Commit**

```bash
git add benchmarks/2026-09-plugins-experiment/results/
git commit -m "test: результаты прогона бенчмарка плагинов"
```

---

### Task 17: Отчёт

**Files:**
- Create: `benchmarks/2026-09-plugins-experiment/README.md`

**Interfaces:**
- Consumes: результаты Task 16
- Produces: финальный отчёт с методологией, таблицами и вердиктом по гипотезе.

- [ ] **Step 1: Написать отчёт**

Структура `benchmarks/2026-09-plugins-experiment/README.md` (обязательные разделы):

1. **Методология**: конфиги, wrk-параметры (c50/c300), best-of-N, noise floor, критерий ±2% и «внутри спреда»; hardware (ноутбук), версии Go/wrk.
2. **Noise floor**: таблица baseline-vs-baseline (c50/c300, медианы, min-max) — спред в %.
3. **Таблицы по фичам** (для каждой из 4): baseline vs plugin-транспорт — req/s c50, req/s c300, Δ% (против baseline), p50/p99, allocs/op, память.
4. **Вердикт по фиче**: `ПРОШЛО` (Δ в пределах ±2% и внутри спреда) / `НЕ ПРОШЛО` (с цифрой Δ%).
5. **Вывод по гипотезе**: какие фичи можно выносить и на каком транспорте; какие нет (hot-path subprocess); рекомендация по перехода на Подход 2 (реальный вынос кода из бинаря) и его условия.
6. **Зафиксированные trade-off'ы**: reload = перезапуск плагина (~20ms), секрет JWT в env плагина, `.so` требует cgo/нестатической сборки, SO-конфиг через env.

- [ ] **Step 2: Проверить отчёт на полноту и согласовать**

Прочитать отчёт свежим взглядом: есть ли все 4 фичи, все транспорты, все метрики, вердикты с цифрами, раздел про ограничения. Поправить при необходимости.

- [ ] **Step 3: Commit**

```bash
git add benchmarks/2026-09-plugins-experiment/README.md
git commit -m "docs: отчёт эксперимента по выносу фич гейтвея в плагины"
```

---
