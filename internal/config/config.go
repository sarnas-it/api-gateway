package config

import (
	"fmt"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config представляет основную структуру конфигурации
type Config struct {
	App         `yaml:"application"`
	Server      ServerConfig      `yaml:"server"`
	TLS         *TLSConfig        `yaml:"tls,omitempty"`
	Static      *StaticConfig     `yaml:"static,omitempty"`
	Targets     []TargetConfig    `yaml:"targets"`
	JWT         JWTConfig         `yaml:"jwt"`
	BasicAuth   BasicAuthConfig   `yaml:"basic_auth"`
	Logging     LoggingConfig     `yaml:"logging"`
	Headers     HeadersConfig     `yaml:"headers"`
	Routing     RoutingConfig     `yaml:"routing"`
	Permissions PermissionsConfig `yaml:"permissions"`
	Identity    IdentityConfig    `yaml:"identity"`
	Webhooks    []WebhookConfig   `yaml:"webhooks"`
	Discovery   *DiscoveryConfig  `yaml:"discovery,omitempty"`
}

// DiscoveryConfig конфигурация service discovery (Docker/Podman по labels).
type DiscoveryConfig struct {
	Enabled           bool          `yaml:"enabled"`
	Provider          string        `yaml:"provider"`
	Host              string        `yaml:"host"`
	APIVersion        string        `yaml:"api_version"`
	LabelPrefix       string        `yaml:"label_prefix"`
	ServiceNameLabels []string      `yaml:"service_name_labels"`
	Network           string        `yaml:"network"`
	Debounce          time.Duration `yaml:"debounce"`
	ResyncInterval    time.Duration `yaml:"resync_interval"`
	DefaultTimeout    time.Duration `yaml:"default_timeout"`
	// StateFile — путь к файлу с последним удачным результатом discovery
	// (аварийный фолбэк: маршруты переживают рестарт при недоступном Docker).
	// Пустая строка отключает персист.
	StateFile string `yaml:"state_file"`
}

// TLSConfig конфигурация TLS с автосертификатами (Let's Encrypt)
type TLSConfig struct {
	Enabled      bool     `yaml:"enabled"`       // включить HTTPS
	Port         int      `yaml:"port"`          // HTTPS порт (по умолчанию 443)
	HTTPPort     int      `yaml:"http_port"`     // HTTP порт для redirect (по умолчанию 80)
	Domains      []string `yaml:"domains"`       // домены для сертификатов
	Email        string   `yaml:"email"`         // email для Let's Encrypt (обязательно)
	CacheDir     string   `yaml:"cache_dir"`     // директория для кеша сертификатов
	Staging      bool     `yaml:"staging"`       // true = staging CA, false = production Let's Encrypt
	RedirectHTTP bool     `yaml:"redirect_http"` // автоматический redirect HTTP → HTTPS
	// DirectoryURL — необязательный ACME directory URL. Если задан, перекрывает
	// выбор CA по staging. Пусто = staging ? Let's Encrypt staging : production.
	DirectoryURL string `yaml:"directory_url"`
}

// StaticApp конфигурация SPA фронтенда
type StaticApp struct {
	PathPrefix string `yaml:"path_prefix"` // URL путь (например / или /admin)
	RootDir    string `yaml:"root_dir"`    // директория со статикой
	IndexFile  string `yaml:"index_file"`  // fallback для SPA (по умолчанию index.html)
	MaxAge     int    `yaml:"max_age"`     // Cache-Control max-age в секундах
}

// StaticConfig конфигурация раздачи статических SPA
type StaticConfig struct {
	Apps         []StaticApp `yaml:"apps"`
	SkipPrefixes []string    `yaml:"skip_prefixes,omitempty"` // пути, которые НЕ отдавать статикой (например /api)
}
type App struct {
	Env               string   `yaml:"env"`
	HealthCheck       bool     `yaml:"health_check"`
	CircuitBreaker    bool     `yaml:"circuit_breaker"`
	MetricsEnabled    bool     `yaml:"metrics_enabled"` // сбор метрик и /metrics эндпоинт; выкл. по умолчанию (доп. накладные расходы на запрос)
	MetricsAllowedIPs []string `yaml:"metrics_allowed_ips"`
	// MaxIdleConnsPerHost — размер пула keep-alive соединений к каждому таргету.
	// Должен быть не меньше пиковой конкурентности к таргету, иначе транспорт
	// постоянно переоткрывает соединения (CPU уходит в connect) и RPS падает
	// под нагрузкой. 0 → дефолт (1000).
	MaxIdleConnsPerHost int `yaml:"max_idle_conns_per_host"`
}

// ServerConfig конфигурация HTTP сервера
type ServerConfig struct {
	Port         int           `yaml:"port"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	IdleTimeout  time.Duration `yaml:"idle_timeout"`
	// MaxRequestBodySize — лимит тела запроса в байтах.
	// nil (ключ не задан) → значение по умолчанию 10 MiB;
	// 0 → без лимита; >0 → лимит в байтах.
	MaxRequestBodySize *int64 `yaml:"max_request_body_size"`
}

// defaultMaxRequestBodySize применяется, когда max_request_body_size не задан.
const defaultMaxRequestBodySize int64 = 10 << 20 // 10 MiB

// EffectiveMaxRequestBodySize возвращает действующий лимит тела запроса в
// байтах. nil трактуется как значение по умолчанию (10 MiB); значение <= 0
// означает отсутствие лимита.
func (s ServerConfig) EffectiveMaxRequestBodySize() int64 {
	if s.MaxRequestBodySize == nil {
		return defaultMaxRequestBodySize
	}
	return *s.MaxRequestBodySize
}

// TargetConfig конфигурация целевого сервера
type TargetConfig struct {
	Name        string        `yaml:"name"`         // уникальное имя таргета
	URL         string        `yaml:"url"`          // например: http://localhost:9001
	Timeout     time.Duration `yaml:"timeout"`      // таймаут для запросов к цели
	PathPrefix  string        `yaml:"path_prefix"`  // какой путь проксировать (опционально)
	StripPrefix bool          `yaml:"strip_prefix"` // удалять префикс при проксировании
	// Weight — вес таргета при взвешенной балансировке внутри одного route-пула
	// (правила с одинаковыми host, path_prefix и methods). nil (ключ не задан) →
	// значение по умолчанию 1; 0 или отрицательное → таргет исключается из
	// выбора; >0 → вес.
	Weight      *int   `yaml:"weight"`
	HealthCheck string `yaml:"health_check"` // URL для проверки здоровья
}

// EffectiveWeight возвращает действующий вес таргета: nil трактуется как 1
// (значение по умолчанию). Явные 0 и отрицательные значения сохраняются —
// вызывающая сторона исключает такие таргеты из выбора.
func (t TargetConfig) EffectiveWeight() int {
	if t.Weight == nil {
		return 1
	}
	return *t.Weight
}

// RoutingConfig конфигурация маршрутизации
type RoutingConfig struct {
	Rules       []RoutingRule  `yaml:"rules"`                  // правила маршрутизации
	GlobalLimit *RateLimitRule `yaml:"global_limit,omitempty"` // глобальный лимит для всех запросов
}

// RoutingRule правило маршрутизации
type RoutingRule struct {
	Host       string         `yaml:"host,omitempty"`       // домен для матчинга (опционально)
	PathPrefix string         `yaml:"path_prefix"`          // путь для матчинга
	TargetName string         `yaml:"target_name"`          // имя таргета
	Methods    []string       `yaml:"methods"`              // HTTP методы (опционально)
	StripPath  bool           `yaml:"strip_path"`           // удалять префикс при проксировании
	Auth       *AuthRule      `yaml:"auth,omitempty"`       // per-route auth конфиг
	RateLimit  *RateLimitRule `yaml:"rate_limit,omitempty"` // per-route rate limit
}

// AuthRule конфигурация аутентификации для роута
type AuthRule struct {
	Required   bool     `yaml:"required"`              // требовать ли JWT
	Roles      []string `yaml:"roles,omitempty"`       // хотя бы одна из ролей (any-of)
	RolesAll   []string `yaml:"roles_all,omitempty"`   // все перечисленные роли (all-of)
	StripToken *bool    `yaml:"strip_token,omitempty"` // удалять токен (наследует глобальный если не указан)
}

// BasicAuthConfig конфигурация Basic аутентификации
type BasicAuthConfig struct {
	Enabled   bool     `yaml:"enabled"`
	Username  string   `yaml:"username"`
	Password  string   `yaml:"password"`
	SkipPaths []string `yaml:"skip_paths"` // пути, которые не требуют Basic Auth (с поддержкой префиксов)
}

// RateLimitRule конфигурация rate limiting для роута
type RateLimitRule struct {
	RequestsPerSecond float64 `yaml:"requests_per_second"` // запросов в секунду
	Burst             int     `yaml:"burst"`               // burst размер
}

// JWTConfig конфигурация валидации JWT
type JWTConfig struct {
	SecretKey     string   `yaml:"secret_key"`      // симметричный ключ (HMAC)
	PublicKeyFile string   `yaml:"public_key_file"` // для RSA/ECDSA
	Algorithm     string   `yaml:"algorithm"`       // HS256, RS256 и т.д.
	ValidateExp   bool     `yaml:"validate_exp"`    // проверять срок действия
	ValidateIss   bool     `yaml:"validate_iss"`    // проверять issuer
	ExpectedIss   string   `yaml:"expected_iss"`    // ожидаемый issuer
	ValidateAud   bool     `yaml:"validate_aud"`    // проверять audience
	ExpectedAud   string   `yaml:"expected_aud"`    // ожидаемый audience
	ClaimMappings []string `yaml:"claim_mappings"`  // какие claims извлекать
	Required      bool     `yaml:"required"`        // требовать ли JWT
}

// CORSConfig настройки CORS
type CORSConfig struct {
	Enabled        bool     `yaml:"enabled"`
	AllowedOrigins []string `yaml:"allowed_origins"`
	AllowedMethods []string `yaml:"allowed_methods"`
	AllowedHeaders []string `yaml:"allowed_headers"`
	ExposeHeaders  []string `yaml:"expose_headers"`
	MaxAge         int      `yaml:"max_age"`
}

// PermissionsConfig конфигурация модуля разрешений (permission-service)
type PermissionsConfig struct {
	Enabled         bool          `yaml:"enabled"`
	ServiceURL      string        `yaml:"service_url"`
	Method          string        `yaml:"method"`
	Path            string        `yaml:"path"`
	CacheTTL        time.Duration `yaml:"cache_ttl"`
	HeaderName      string        `yaml:"header_name"`
	InvalidateToken string        `yaml:"invalidate_token"`
	APIKey          string        `yaml:"api_key"`
	APIKeyHeader    string        `yaml:"api_key_header"`
}

const (
	defaultPermissionsMethod       = "GET"
	defaultPermissionsPath         = "/api/v1/users/{user_id}/effective-permissions"
	defaultPermissionsAPIKeyHeader = "X-API-Key"
)

// IdentityConfig — федерация внешней идентичности (внешние JWT-провайдеры)
type IdentityConfig struct {
	Enabled    bool                     `yaml:"enabled"`
	Selection  []string                 `yaml:"selection"` // пусто = все enabled
	UserLookup IdentityUserLookupConfig `yaml:"user_lookup"`
	Providers  []IdentityProviderConfig `yaml:"providers"`
}

// IdentityUserLookupConfig — резолв email → наш пользователь через passport
type IdentityUserLookupConfig struct {
	ServiceURL string        `yaml:"service_url"`
	HMACSecret string        `yaml:"hmac_secret"`
	CacheTTL   time.Duration `yaml:"cache_ttl"`
}

// IdentityProviderConfig — один доверенный внешний JWT-провайдер
type IdentityProviderConfig struct {
	Name          string `yaml:"name"`
	Enabled       bool   `yaml:"enabled"`
	Issuer        string `yaml:"issuer"`
	Algorithm     string `yaml:"algorithm"`
	PublicKeyFile string `yaml:"public_key_file"`
	SecretKey     string `yaml:"secret_key"`
	ValidateExp   bool   `yaml:"validate_exp"`
	ValidateIss   bool   `yaml:"validate_iss"`
	ExpectedIss   string `yaml:"expected_iss"`
	EmailClaim    string `yaml:"email_claim"`
}

// HeadersConfig конфигурация заголовков
type HeadersConfig struct {
	StripAuthorization bool              `yaml:"strip_authorization"`
	ClaimToHeader      map[string]string `yaml:"claim_to_header"`
	AddHeaders         map[string]string `yaml:"add_headers"`
	SignHeader         string            `yaml:"sign_header"`
	CORS               *CORSConfig       `yaml:"cors,omitempty"`
}

// WebhookTrigger тип триггера для вебхука
type WebhookTrigger string

const (
	TriggerOnRequest  WebhookTrigger = "on_request"
	TriggerOnResponse WebhookTrigger = "on_response"
)

// WebhookTransport тип транспорта для вебхука
type WebhookTransport string

const (
	TransportNATS    WebhookTransport = "nats"
	TransportWebhook WebhookTransport = "webhook"
)

// WebhookConfig конфигурация вебхука/NATS публикации событий
type WebhookConfig struct {
	Name          string           `yaml:"name"`
	Transport     WebhookTransport `yaml:"transport"`
	NATSURL       string           `yaml:"nats_url"`
	Subject       string           `yaml:"subject"`
	WebhookURL    string           `yaml:"webhook_url"`
	Trigger       WebhookTrigger   `yaml:"trigger"`
	Methods       []string         `yaml:"methods"`
	OnStatusCodes []int            `yaml:"on_status_codes"`
	ExcludePaths  []string         `yaml:"exclude_paths"`
	Async         bool             `yaml:"async"`
	// IncludeRequestBody — включать в событие тело запроса (поле changes).
	// nil (не задано) = true: прежнее поведение (тело публиковалось всегда).
	// Явное false выключает публикацию тела запроса для этого вебхука.
	IncludeRequestBody *bool `yaml:"include_request_body"`
	// IncludeResponseBody — включать в событие тело ответа (поле response_body).
	// Тело собирается только для JSON-ответов и ограничено по размеру.
	// По умолчанию выключено — включение настраивается явно.
	IncludeResponseBody bool `yaml:"include_response_body"`
	// BatchSize включает батчинг HTTP-вебхуков: события копятся и уходят одним
	// POST телом {"count":N,"events":[...]}. 0/1 — по одному событию (старый формат).
	BatchSize int `yaml:"batch_size"`
	// FlushInterval — максимальная задержка перед отправкой неполной пачки.
	// По умолчанию 200ms (используется только при BatchSize > 1).
	FlushInterval time.Duration `yaml:"flush_interval"`
}

// IncludeRequestBodyEnabled возвращает эффективное значение include_request_body
// (nil трактуется как true — обратная совместимость).
func (w WebhookConfig) IncludeRequestBodyEnabled() bool {
	if w.IncludeRequestBody == nil {
		return true
	}
	return *w.IncludeRequestBody
}

// LoggingConfig конфигурация логирования
type LoggingConfig struct {
	Level     string `yaml:"level"`      // debug, info, warn, error
	Format    string `yaml:"format"`     // json, console или text (алиас console)
	AccessLog bool   `yaml:"access_log"` // построчный лог каждого запроса; выкл. по умолчанию (аллокации на каждый запрос)
}

// Load загружает конфигурацию из файла. Второе возвращаемое значение —
// предупреждения о неизвестных YAML-ключах с точечными путями (например
// "headers.forward_headers"); вызывающая сторона должна залогировать их.
// Неизвестные ключи не являются ошибкой — конфиг по-прежнему загружается.
func Load(path string) (*Config, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Подстановка переменных окружения ${VAR_NAME}. Если переменная не задана
	// или пуста, конфигурация считается невалидной: молчаливый фолбэк на
	// литерал "${VAR}" опасен (его можно случайно использовать как секрет).
	// Экранирование литерала — "$${VAR}".
	var missing []string
	seenMissing := make(map[string]bool)
	resolved := os.Expand(string(data), func(key string) string {
		if key == "$" {
			return "$"
		}
		val, ok := os.LookupEnv(key)
		if !ok || val == "" {
			if !seenMissing[key] {
				seenMissing[key] = true
				missing = append(missing, key)
			}
			return ""
		}
		return val
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, nil, fmt.Errorf("config references undefined environment variable(s): %s", strings.Join(missing, ", "))
	}

	cfg, warnings, err := parseConfig(resolved)
	if err != nil {
		return nil, nil, err
	}

	// Валидация конфигурации
	if err := cfg.validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, warnings, nil
}

// LoadLenient загружает конфигурацию «мягко». Отличия от Load:
//   - отсутствующие или пустые переменные окружения не считаются ошибкой —
//     соответствующий литерал "${VAR}" остаётся в тексте как есть;
//   - validate() не вызывается, поэтому конфиг без таргетов всё равно
//     разбирается.
//
// Предупреждения о неизвестных YAML-ключах и значения по умолчанию
// применяются так же, как у Load. Режим нужен read-only дашборду, который не
// должен требовать секретные переменные окружения гейтвея.
func LoadLenient(path string) (*Config, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Best-effort подстановка: не заданные (или пустые) переменные остаются
	// литералом "${VAR}". Экранирование "$${VAR}" по-прежнему даёт "${VAR}".
	resolved := os.Expand(string(data), func(key string) string {
		if key == "$" {
			return "$"
		}
		val, ok := os.LookupEnv(key)
		if !ok || val == "" {
			return "${" + key + "}"
		}
		return val
	})

	cfg, warnings, err := parseConfig(resolved)
	if err != nil {
		return nil, nil, err
	}
	return cfg, warnings, nil
}

// parseConfig разбирает YAML, собирает предупреждения о неизвестных ключах и
// применяет значения по умолчанию. Валидация здесь не выполняется — это
// ответственность вызывающей стороны (Load).
func parseConfig(resolved string) (*Config, []string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(resolved), &root); err != nil {
		return nil, nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	var cfg Config
	if root.Kind != 0 {
		if err := root.Decode(&cfg); err != nil {
			return nil, nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	warnings := unknownYAMLKeys(&root)

	// Устанавливаем значения по умолчанию
	cfg.setDefaults()

	return &cfg, warnings, nil
}

// unknownYAMLKeys обходит дерево YAML и возвращает точечные пути ключей, для
// которых нет соответствующего поля в структуре Config (по yaml-тегам).
func unknownYAMLKeys(root *yaml.Node) []string {
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	var warnings []string
	seen := make(map[string]bool)
	collectUnknownKeys(root.Content[0], reflect.TypeOf(Config{}), "", &warnings, seen)
	return warnings
}

// collectUnknownKeys рекурсивно сверяет узлы YAML со структурой typ.
// path — точечный путь от корня; warnings пополняется неизвестными ключами.
func collectUnknownKeys(node *yaml.Node, typ reflect.Type, path string, warnings *[]string, seen map[string]bool) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	switch typ.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode {
			return
		}
		fields := yamlFields(typ)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			fieldType, ok := fields[key]
			if !ok {
				if !seen[childPath] {
					seen[childPath] = true
					*warnings = append(*warnings, childPath)
				}
				continue
			}
			collectUnknownKeys(node.Content[i+1], fieldType, childPath, warnings, seen)
		}
	case reflect.Slice, reflect.Array:
		if node.Kind != yaml.SequenceNode {
			return
		}
		elem := typ.Elem()
		for _, item := range node.Content {
			collectUnknownKeys(item, elem, path, warnings, seen)
		}
	}
	// Map и скаляры: ключи карт произвольны, у скаляров вложенности нет.
}

// yamlFields строит карту "yaml-имя поля → тип поля" для структуры,
// разворачивая встроенные (anonymous/inline) структуры.
func yamlFields(typ reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name, opts, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			if field.Anonymous || strings.Contains(opts, "inline") {
				embedded := field.Type
				for embedded.Kind() == reflect.Pointer {
					embedded = embedded.Elem()
				}
				if embedded.Kind() == reflect.Struct {
					for k, v := range yamlFields(embedded) {
						fields[k] = v
					}
				}
			}
			continue
		}
		fields[name] = field.Type
	}
	return fields
}

// setDefaults устанавливает значения по умолчанию
func (c *Config) setDefaults() {
	if c.MaxIdleConnsPerHost <= 0 {
		c.MaxIdleConnsPerHost = 1000
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8080
	}
	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = 5 * time.Second
	}
	if c.Server.WriteTimeout == 0 {
		c.Server.WriteTimeout = 10 * time.Second
	}
	if c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = 120 * time.Second
	}
	if c.Server.MaxRequestBodySize == nil {
		// Ключ не задан → дефолт 10 MiB. Явный 0 остаётся без лимита.
		size := defaultMaxRequestBodySize
		c.Server.MaxRequestBodySize = &size
	}

	if c.TLS != nil && c.TLS.Enabled {
		if c.TLS.Port == 0 {
			c.TLS.Port = 443
		}
		if c.TLS.HTTPPort == 0 {
			c.TLS.HTTPPort = 80
		}
		if c.TLS.CacheDir == "" {
			c.TLS.CacheDir = "/var/lib/api-gateway/certs"
		}
	}

	if c.Static != nil {
		for i := range c.Static.Apps {
			if c.Static.Apps[i].IndexFile == "" {
				c.Static.Apps[i].IndexFile = "index.html"
			}
		}
	}

	// Устанавливаем таймауты для таргетов, если не заданы
	for i := range c.Targets {
		if c.Targets[i].Timeout == 0 {
			c.Targets[i].Timeout = 30 * time.Second
		}
		if c.Targets[i].Weight == nil {
			// Ключ не задан → дефолт 1. Явный 0 (или отрицательный)
			// сохраняется и исключает таргет из выбора.
			weight := 1
			c.Targets[i].Weight = &weight
		}
	}

	if c.JWT.Algorithm == "" {
		c.JWT.Algorithm = "HS256"
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	// Формат логирования нормализуем один раз: JSON, Console и т.п. должны
	// приниматься наравне со строчными.
	c.Logging.Format = strings.ToLower(strings.TrimSpace(c.Logging.Format))
	if c.Logging.Format == "" {
		c.Logging.Format = "text"
	}
	if len(c.JWT.ClaimMappings) == 0 {
		// По умолчанию извлекаем sub как user_id
		c.JWT.ClaimMappings = []string{"sub"}
		if c.Headers.ClaimToHeader == nil {
			c.Headers.ClaimToHeader = make(map[string]string)
		}
		if _, ok := c.Headers.ClaimToHeader["sub"]; !ok {
			c.Headers.ClaimToHeader["sub"] = "X-User-ID"
		}
	}

	if c.Identity.UserLookup.CacheTTL == 0 {
		c.Identity.UserLookup.CacheTTL = 300 * time.Second
	}
	for i := range c.Identity.Providers {
		if c.Identity.Providers[i].Algorithm == "" {
			c.Identity.Providers[i].Algorithm = "HS256"
		}
		if c.Identity.Providers[i].EmailClaim == "" {
			c.Identity.Providers[i].EmailClaim = "email"
		}
	}

	// Создаем правила маршрутизации из таргетов, если не заданы явно
	if len(c.Routing.Rules) == 0 {
		for _, target := range c.Targets {
			if target.PathPrefix != "" {
				c.Routing.Rules = append(c.Routing.Rules, RoutingRule{
					PathPrefix: target.PathPrefix,
					TargetName: target.Name,
					StripPath:  target.StripPrefix,
				})
			}
		}
	}

	if c.Permissions.Method == "" {
		c.Permissions.Method = defaultPermissionsMethod
	}
	if c.Permissions.Path == "" {
		c.Permissions.Path = defaultPermissionsPath
	}
	if c.Permissions.APIKeyHeader == "" {
		c.Permissions.APIKeyHeader = defaultPermissionsAPIKeyHeader
	}

	// Дефолты discovery применяются, только когда discovery включён; пустая
	// секция `discovery:` с `enabled: true` получает все значения ниже.
	if c.Discovery != nil && c.Discovery.Enabled {
		d := c.Discovery
		if d.Provider == "" {
			d.Provider = "docker"
		}
		if d.Host == "" {
			d.Host = "unix:///var/run/docker.sock"
		}
		if d.APIVersion == "" {
			d.APIVersion = "v1.41"
		}
		if d.LabelPrefix == "" {
			d.LabelPrefix = "gateway"
		}
		if len(d.ServiceNameLabels) == 0 {
			d.ServiceNameLabels = []string{
				"com.docker.compose.service",
				"io.podman.compose.service",
			}
		}
		if d.Debounce == 0 {
			d.Debounce = 500 * time.Millisecond
		}
		if d.ResyncInterval == 0 {
			d.ResyncInterval = 5 * time.Minute
		}
		if d.DefaultTimeout == 0 {
			d.DefaultTimeout = 30 * time.Second
		}
		if d.StateFile == "" {
			d.StateFile = "/var/lib/api-gateway/discovery-state.json"
		}
	}
}

// Validate проверяет корректность конфигурации (публичная обёртка для discovery).
func (c *Config) Validate() error {
	return c.validate()
}

// validHTTPMethodToken reports whether m is a non-empty HTTP method token,
// rejecting whitespace and control characters.
func validHTTPMethodToken(m string) bool {
	if m == "" {
		return false
	}
	for _, r := range m {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// validate проверяет корректность конфигурации
func (c *Config) validate() error {
	discoveryEnabled := c.Discovery != nil && c.Discovery.Enabled
	if discoveryEnabled {
		switch c.Discovery.Provider {
		case "docker", "podman":
		default:
			return fmt.Errorf("discovery.provider must be docker or podman, got %q", c.Discovery.Provider)
		}
	}
	if len(c.Targets) == 0 && !discoveryEnabled {
		return fmt.Errorf("at least one target is required")
	}

	// Проверяем уникальность имен таргетов
	targetNames := make(map[string]bool)
	for _, target := range c.Targets {
		if target.Name == "" {
			return fmt.Errorf("target name is required")
		}
		if targetNames[target.Name] {
			return fmt.Errorf("duplicate target name: %s", target.Name)
		}
		targetNames[target.Name] = true

		if target.URL == "" {
			return fmt.Errorf("target.url is required for target %s", target.Name)
		}
		if _, err := url.Parse(target.URL); err != nil {
			return fmt.Errorf("target.url is invalid for target %s: %w", target.Name, err)
		}
	}

	// Проверяем правила маршрутизации
	poolPolicy := make(map[string]RoutingRule)
	for _, rule := range c.Routing.Rules {
		if rule.PathPrefix == "" {
			return fmt.Errorf("routing rule path_prefix is required")
		}
		if !targetNames[rule.TargetName] {
			return fmt.Errorf("routing rule references unknown target: %s", rule.TargetName)
		}
		// Правила, совпадающие по (host, path_prefix, methods), делят один пул
		// балансировки. Политика пула (auth, strip_path, rate_limit) должна
		// совпадать, иначе поведение запроса зависело бы от выбранного таргета.
		key := RuleRouteKey(rule)
		if first, ok := poolPolicy[key]; ok {
			if first.StripPath != rule.StripPath ||
				!reflect.DeepEqual(first.Auth, rule.Auth) ||
				!reflect.DeepEqual(first.RateLimit, rule.RateLimit) {
				return fmt.Errorf("routing rules for host %q path_prefix %q methods %v share a target pool but differ in auth, strip_path or rate_limit",
					rule.Host, rule.PathPrefix, rule.Methods)
			}
			continue
		}
		poolPolicy[key] = rule
	}

	// Проверяем формат логирования. Пустое значение допустимо для
	// программно собранных конфигов — setDefaults подставляет "text".
	// Нормализуем регистр, чтобы "JSON"/"Console" принимались и сохранялись
	// в каноническом виде.
	format := strings.ToLower(strings.TrimSpace(c.Logging.Format))
	if format != "" {
		switch format {
		case "console", "text", "json":
			c.Logging.Format = format
		default:
			return fmt.Errorf("logging.format must be one of console, text, json, got %q", c.Logging.Format)
		}
	}

	// Проверяем TLS конфигурацию
	if c.TLS != nil && c.TLS.Enabled {
		if len(c.TLS.Domains) == 0 {
			return fmt.Errorf("at least one domain is required when TLS is enabled")
		}
		if c.TLS.Email == "" {
			return fmt.Errorf("email is required for Let's Encrypt registration")
		}
	}

	// Проверяем JWT конфигурацию
	if c.JWT.Required {
		c.JWT.ValidateExp = true
	}

	if c.Permissions.CacheTTL == 0 {
		c.Permissions.CacheTTL = 300 * time.Second
	}
	if c.Permissions.HeaderName == "" {
		c.Permissions.HeaderName = "X-User-Permissions"
	}
	if c.Permissions.InvalidateToken == "" && c.Permissions.APIKey != "" {
		c.Permissions.InvalidateToken = c.Permissions.APIKey
	}

	if c.Permissions.Enabled {
		if c.Permissions.ServiceURL == "" {
			return fmt.Errorf("permissions.service_url is required when permissions.enabled is true")
		}
		if !strings.Contains(c.Permissions.Path, "{user_id}") {
			return fmt.Errorf("permissions.path must contain the {user_id} placeholder, got %q", c.Permissions.Path)
		}
		if !validHTTPMethodToken(c.Permissions.Method) {
			return fmt.Errorf("permissions.method must be a non-empty HTTP token without whitespace or control characters, got %q", c.Permissions.Method)
		}
	}

	for _, wh := range c.Webhooks {
		if wh.Name == "" {
			return fmt.Errorf("webhook name is required")
		}
		if wh.Transport == "" {
			return fmt.Errorf("webhook %s: transport is required", wh.Name)
		}
		if wh.Transport == TransportNATS && wh.NATSURL == "" {
			return fmt.Errorf("webhook %s: nats_url is required for nats transport", wh.Name)
		}
		if wh.Transport == TransportNATS && wh.Subject == "" {
			return fmt.Errorf("webhook %s: subject is required for nats transport", wh.Name)
		}
		if wh.Transport == TransportWebhook && wh.WebhookURL == "" {
			return fmt.Errorf("webhook %s: webhook_url is required for webhook transport", wh.Name)
		}
		if wh.Trigger == "" {
			return fmt.Errorf("webhook %s: trigger is required (on_request|on_response)", wh.Name)
		}
	}

	if c.Identity.Enabled {
		if len(c.Identity.Providers) == 0 {
			return fmt.Errorf("identity.enabled requires at least one provider")
		}
		names := make(map[string]bool, len(c.Identity.Providers))
		for i := range c.Identity.Providers {
			p := &c.Identity.Providers[i]
			if p.Name == "" {
				return fmt.Errorf("identity provider name is required")
			}
			if names[p.Name] {
				return fmt.Errorf("duplicate identity provider name: %s", p.Name)
			}
			names[p.Name] = true
			if len(p.Algorithm) < 2 {
				return fmt.Errorf("identity provider %s: invalid algorithm %q", p.Name, p.Algorithm)
			}
			// Инвариант разделения секретов: партнёрский HMAC-секрет не должен
			// совпадать с нашими, иначе партнёрский токен прошёл бы как наш.
			if strings.HasPrefix(strings.ToUpper(p.Algorithm), "HS") {
				if p.SecretKey != "" && p.SecretKey == c.JWT.SecretKey {
					return fmt.Errorf("identity provider %s: secret_key must differ from jwt.secret_key", p.Name)
				}
				if p.SecretKey != "" && p.SecretKey == c.Permissions.APIKey {
					return fmt.Errorf("identity provider %s: secret_key must differ from permissions.api_key", p.Name)
				}
			}
		}
		if c.Identity.UserLookup.ServiceURL == "" {
			return fmt.Errorf("identity.user_lookup.service_url is required when identity.enabled is true")
		}
	}

	return nil
}

// GetTargetByName возвращает таргет по имени
func (c *Config) GetTargetByName(name string) *TargetConfig {
	for i := range c.Targets {
		if c.Targets[i].Name == name {
			return &c.Targets[i]
		}
	}
	return nil
}

// FindTargetForPath находит таргет для пути на основе правил маршрутизации
func (c *Config) FindTargetForPath(path string, method string, host ...string) (*TargetConfig, *RoutingRule) {
	var bestMatch *RoutingRule
	var bestMatchLen int

	reqHost := ""
	if len(host) > 0 {
		reqHost = host[0]
	}

	for i := range c.Routing.Rules {
		rule := &c.Routing.Rules[i]
		// Проверяем Host, если указан
		if rule.Host != "" {
			if reqHost == "" {
				continue
			}
			// Wildcard host: *.example.com
			if strings.HasPrefix(rule.Host, "*.") {
				suffix := rule.Host[1:] // .example.com
				if !strings.HasSuffix(reqHost, suffix) {
					continue
				}
			} else if !strings.EqualFold(reqHost, rule.Host) {
				continue
			}
		}

		// Проверяем метод, если указан
		if len(rule.Methods) > 0 {
			methodAllowed := false
			for _, m := range rule.Methods {
				if strings.EqualFold(m, method) {
					methodAllowed = true
					break
				}
			}
			if !methodAllowed {
				continue
			}
		}

		// Ищем самый длинный совпадающий префикс
		if strings.HasPrefix(path, rule.PathPrefix) {
			// При равной длине префикса правило с host конкретнее правила без host.
			l := len(rule.PathPrefix)
			if bestMatch == nil || l > bestMatchLen || (l == bestMatchLen && bestMatch.Host == "" && rule.Host != "") {
				bestMatch = rule
				bestMatchLen = l
			}
		}
	}

	if bestMatch != nil {
		return c.GetTargetByName(bestMatch.TargetName), bestMatch
	}

	return nil, nil
}
