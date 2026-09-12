package config

import (
	"fmt"
	"net/url"
	"os"
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
	Staging      bool     `yaml:"staging"`       // true = тестовый CA, false = production Let's Encrypt
	RedirectHTTP bool     `yaml:"redirect_http"` // автоматический redirect HTTP → HTTPS
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
	Port               int           `yaml:"port"`
	ReadTimeout        time.Duration `yaml:"read_timeout"`
	WriteTimeout       time.Duration `yaml:"write_timeout"`
	IdleTimeout        time.Duration `yaml:"idle_timeout"`
	MaxRequestBodySize int64         `yaml:"max_request_body_size"` // макс. размер тела запроса (байт), 0 = без лимита
}

// TargetConfig конфигурация целевого сервера
type TargetConfig struct {
	Name        string        `yaml:"name"`         // уникальное имя таргета
	URL         string        `yaml:"url"`          // например: http://localhost:9001
	Timeout     time.Duration `yaml:"timeout"`      // таймаут для запросов к цели
	PathPrefix  string        `yaml:"path_prefix"`  // какой путь проксировать (опционально)
	StripPrefix bool          `yaml:"strip_prefix"` // удалять префикс при проксировании
	Weight      int           `yaml:"weight"`       // для балансировки (опционально)
	HealthCheck string        `yaml:"health_check"` // URL для проверки здоровья
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
	Roles      []string `yaml:"roles,omitempty"`       // требуемые роли (опционально)
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
	CacheTTL        time.Duration `yaml:"cache_ttl"`
	HeaderName      string        `yaml:"header_name"`
	InvalidateToken string        `yaml:"invalidate_token"`
	APIKey          string        `yaml:"api_key"`
}

// HeadersConfig конфигурация заголовков
type HeadersConfig struct {
	StripAuthorization bool              `yaml:"strip_authorization"`
	ForwardHeaders     bool              `yaml:"forward_headers"`
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
	Format    string `yaml:"format"`     // json или text
	AccessLog bool   `yaml:"access_log"` // построчный лог каждого запроса; выкл. по умолчанию (аллокации на каждый запрос)
}

// Load загружает конфигурацию из файла
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Подстановка переменных окружения ${VAR_NAME}
	resolved := os.Expand(string(data), func(key string) string {
		val := os.Getenv(key)
		if val == "" {
			return fmt.Sprintf("${%s}", key)
		}
		return val
	})

	var cfg Config
	if err := yaml.Unmarshal([]byte(resolved), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Устанавливаем значения по умолчанию
	cfg.setDefaults()

	// Валидация конфигурации
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
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
	if c.Server.MaxRequestBodySize == 0 {
		c.Server.MaxRequestBodySize = 10 << 20 // 10 MB
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
	}

	if c.JWT.Algorithm == "" {
		c.JWT.Algorithm = "HS256"
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
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
	for _, rule := range c.Routing.Rules {
		if rule.PathPrefix == "" {
			return fmt.Errorf("routing rule path_prefix is required")
		}
		if !targetNames[rule.TargetName] {
			return fmt.Errorf("routing rule references unknown target: %s", rule.TargetName)
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

	if c.Permissions.Enabled && c.Permissions.ServiceURL == "" {
		return fmt.Errorf("permissions.service_url is required when permissions.enabled is true")
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

	for _, rule := range c.Routing.Rules {
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
			if len(rule.PathPrefix) > bestMatchLen {
				bestMatch = &rule
				bestMatchLen = len(rule.PathPrefix)
			}
		}
	}

	if bestMatch != nil {
		return c.GetTargetByName(bestMatch.TargetName), bestMatch
	}

	return nil, nil
}
