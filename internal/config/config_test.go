package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

func TestLoad_ValidConfig(t *testing.T) {
	yaml := `
server:
  port: 9090
application:
  env: "dev"
targets:
  - name: "test-api"
    url: "http://localhost:9001"
    timeout: 10s
    path_prefix: "/api/v1/test"
jwt:
  secret_key: "my-secret"
  algorithm: "HS256"
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Server.Port)
	}
	if cfg.Targets[0].Timeout != 10*time.Second {
		t.Errorf("expected timeout 10s, got %v", cfg.Targets[0].Timeout)
	}
}

func TestLoad_DefaultsTargetWeightToOne(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Targets[0].EffectiveWeight() != 1 {
		t.Errorf("target weight = %d, want default 1", cfg.Targets[0].EffectiveWeight())
	}
	if cfg.Targets[0].Weight == nil || *cfg.Targets[0].Weight != 1 {
		t.Errorf("omitted weight must default to 1, got %v", cfg.Targets[0].Weight)
	}
}

func TestLoad_ExplicitZeroWeightIsExcluded(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
    weight: 0
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Targets[0].Weight == nil || *cfg.Targets[0].Weight != 0 {
		t.Fatalf("explicit weight 0 must be preserved, got %v", cfg.Targets[0].Weight)
	}
	if cfg.Targets[0].EffectiveWeight() != 0 {
		t.Errorf("explicit weight 0 effective = %d, want 0 (excluded)", cfg.Targets[0].EffectiveWeight())
	}
}

func TestLoad_MissingTarget(t *testing.T) {
	yaml := `
server:
  port: 8080
targets: []
`
	path := writeTempConfig(t, yaml)
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected error for empty targets")
	}
}

func TestLoad_DuplicateTargetName(t *testing.T) {
	yaml := `
targets:
  - name: "dup"
    url: "http://localhost:9001"
  - name: "dup"
    url: "http://localhost:9002"
`
	path := writeTempConfig(t, yaml)
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate target name")
	}
}

func TestLoad_IgnoresRemovedForwardHeaders(t *testing.T) {
	// headers.forward_headers удалён: заголовки проксируются всегда. Старый
	// конфиг с этим ключом должен по-прежнему загружаться (ключ игнорируется
	// как неизвестный), а не падать.
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
headers:
  forward_headers: true
  strip_authorization: true
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("config with removed headers.forward_headers must still load: %v", err)
	}
	if !cfg.Headers.StripAuthorization {
		t.Error("sibling headers settings must still be parsed")
	}
}

func TestLoad_RejectsMalformedTargetURL(t *testing.T) {
	yaml := `
targets:
  - name: "bad"
    url: "http://[::1"
`
	path := writeTempConfig(t, yaml)
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected error for malformed target URL")
	}
}

func TestFindTargetForPath_LongestPrefix(t *testing.T) {
	yaml := `
targets:
  - name: "auth"
    url: "http://auth:9001"
    path_prefix: "/api/v1/auth"
  - name: "auth-login"
    url: "http://auth:9001"
    path_prefix: "/api/v1/auth/login"
routing:
  rules:
    - path_prefix: "/api/v1/auth/login"
      target_name: "auth-login"
    - path_prefix: "/api/v1/auth"
      target_name: "auth"
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	target, _ := cfg.FindTargetForPath("/api/v1/auth/login", "GET")
	if target == nil || target.Name != "auth-login" {
		t.Errorf("expected auth-login, got %v", target)
	}

	target, _ = cfg.FindTargetForPath("/api/v1/auth/register", "GET")
	if target == nil || target.Name != "auth" {
		t.Errorf("expected auth, got %v", target)
	}
}

func TestFindTargetForPath_HonorsMethods(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
    path_prefix: "/api"
routing:
  rules:
    - path_prefix: "/api"
      target_name: "api"
      methods: ["GET"]
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	_, rule := cfg.FindTargetForPath("/api/test", "POST")
	if rule != nil {
		t.Errorf("expected no match for POST, but got rule: %+v", rule)
	}

	target, rule := cfg.FindTargetForPath("/api/test", "GET")
	if target == nil || rule == nil {
		t.Errorf("expected match for GET")
	}
}

func TestConfig_Defaults(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.Port != 8080 {
		t.Errorf("default port should be 8080, got %d", cfg.Server.Port)
	}
	if cfg.JWT.Algorithm != "HS256" {
		t.Errorf("default algorithm should be HS256, got %s", cfg.JWT.Algorithm)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("default log level should be info, got %s", cfg.Logging.Level)
	}
}

func TestTLS_ValidConfig(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
tls:
  enabled: true
  domains:
    - "api.example.com"
  email: "admin@example.com"
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TLS.Enabled {
		t.Fatal("expected TLS enabled")
	}
	if cfg.TLS.Port != 443 {
		t.Errorf("default TLS port should be 443, got %d", cfg.TLS.Port)
	}
	if cfg.TLS.CacheDir != "/var/lib/api-gateway/certs" {
		t.Errorf("default cache dir mismatch, got %s", cfg.TLS.CacheDir)
	}
}

func TestTLS_NoDomains(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
tls:
  enabled: true
  email: "admin@example.com"
`
	path := writeTempConfig(t, yaml)
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected error for TLS without domains")
	}
}

func TestTLS_NoEmail(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
tls:
  enabled: true
  domains:
    - "api.example.com"
`
	path := writeTempConfig(t, yaml)
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected error for TLS without email")
	}
}

func TestDiscovery_DefaultsApplied(t *testing.T) {
	yaml := `
discovery:
  enabled: true
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	d := cfg.Discovery
	if d == nil || !d.Enabled {
		t.Fatal("discovery should be enabled")
	}
	if d.Provider != "docker" {
		t.Errorf("provider default: got %q", d.Provider)
	}
	if d.Host != "unix:///var/run/docker.sock" {
		t.Errorf("host default: got %q", d.Host)
	}
	if d.APIVersion != "v1.41" {
		t.Errorf("api_version default: got %q", d.APIVersion)
	}
	if d.LabelPrefix != "gateway" {
		t.Errorf("label_prefix default: got %q", d.LabelPrefix)
	}
	if len(d.ServiceNameLabels) != 2 ||
		d.ServiceNameLabels[0] != "com.docker.compose.service" ||
		d.ServiceNameLabels[1] != "io.podman.compose.service" {
		t.Errorf("service_name_labels default: got %v", d.ServiceNameLabels)
	}
	if d.Debounce != 500*time.Millisecond {
		t.Errorf("debounce default: got %v", d.Debounce)
	}
	if d.ResyncInterval != 5*time.Minute {
		t.Errorf("resync default: got %v", d.ResyncInterval)
	}
	if d.DefaultTimeout != 30*time.Second {
		t.Errorf("default_timeout default: got %v", d.DefaultTimeout)
	}
}

func TestDiscovery_AllowsEmptyTargetsWhenEnabled(t *testing.T) {
	yaml := `
targets: []
discovery:
  enabled: true
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("empty targets must be allowed with discovery enabled: %v", err)
	}
	if len(cfg.Targets) != 0 {
		t.Errorf("expected 0 targets, got %d", len(cfg.Targets))
	}
}

func TestDiscovery_StillRequiresTargetsWhenDisabled(t *testing.T) {
	yaml := `
targets: []
`
	path := writeTempConfig(t, yaml)
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected error for empty targets without discovery")
	}
}

func TestDiscovery_InvalidProvider(t *testing.T) {
	yaml := `
targets: []
discovery:
  enabled: true
  provider: nomad
`
	path := writeTempConfig(t, yaml)
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestDiscovery_PodmanProviderAccepted(t *testing.T) {
	yaml := `
targets: []
discovery:
  enabled: true
  provider: podman
`
	path := writeTempConfig(t, yaml)
	if _, _, err := Load(path); err != nil {
		t.Fatalf("podman provider must be accepted: %v", err)
	}
}

func TestConfig_MaxIdleConnsPerHostDefault(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxIdleConnsPerHost != 1000 {
		t.Errorf("default max_idle_conns_per_host = %d, want 1000", cfg.MaxIdleConnsPerHost)
	}
}

func TestConfig_MaxIdleConnsPerHostOverride(t *testing.T) {
	yaml := `
application:
  max_idle_conns_per_host: 250
targets:
  - name: "api"
    url: "http://api:9001"
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxIdleConnsPerHost != 250 {
		t.Errorf("max_idle_conns_per_host = %d, want 250", cfg.MaxIdleConnsPerHost)
	}
}

func TestLoad_LoggingFormatAccepted(t *testing.T) {
	for _, format := range []string{"console", "text", "json"} {
		t.Run(format, func(t *testing.T) {
			yaml := "targets:\n  - name: \"api\"\n    url: \"http://api:9001\"\nlogging:\n  format: \"" + format + "\"\n"
			path := writeTempConfig(t, yaml)
			if _, _, err := Load(path); err != nil {
				t.Fatalf("logging.format %q must be accepted: %v", format, err)
			}
		})
	}
}

func TestLoad_LoggingFormatCaseInsensitive(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"JSON", "json"},
		{"Console", "console"},
		{"TEXT", "text"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			yaml := "targets:\n  - name: \"api\"\n    url: \"http://api:9001\"\nlogging:\n  format: \"" + tc.in + "\"\n"
			path := writeTempConfig(t, yaml)
			cfg, _, err := Load(path)
			if err != nil {
				t.Fatalf("logging.format %q must be accepted case-insensitively: %v", tc.in, err)
			}
			if cfg.Logging.Format != tc.want {
				t.Errorf("logging.format %q normalized = %q, want %q", tc.in, cfg.Logging.Format, tc.want)
			}
		})
	}
}

func TestLoad_RejectsUnknownLoggingFormat(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
logging:
  format: "xml"
`
	path := writeTempConfig(t, yaml)
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown logging.format")
	}
	if !strings.Contains(err.Error(), "logging.format") || !strings.Contains(err.Error(), "xml") {
		t.Errorf("error must name the field and value, got: %v", err)
	}
}

func TestLoad_RejectsPooledRulesWithDifferentPolicy(t *testing.T) {
	const header = `
targets:
  - name: a
    url: http://a:9000
  - name: b
    url: http://b:9000
routing:
  rules:
`
	cases := []struct {
		name  string
		ruleA string
		ruleB string
	}{
		{
			name:  "auth differs",
			ruleA: "    - {host: h, path_prefix: /api, target_name: a, auth: {required: true}}\n",
			ruleB: "    - {host: h, path_prefix: /api, target_name: b, auth: {required: false}}\n",
		},
		{
			name:  "strip_path differs",
			ruleA: "    - {host: h, path_prefix: /api, target_name: a}\n",
			ruleB: "    - {host: h, path_prefix: /api, target_name: b, strip_path: true}\n",
		},
		{
			name:  "rate_limit differs",
			ruleA: "    - {host: h, path_prefix: /api, target_name: a}\n",
			ruleB: "    - {host: h, path_prefix: /api, target_name: b, rate_limit: {requests_per_second: 10, burst: 20}}\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempConfig(t, header+tc.ruleA+tc.ruleB)
			_, _, err := Load(path)
			if err == nil {
				t.Fatal("expected error for pooled rules with differing policy")
			}
			if !strings.Contains(err.Error(), "pool") {
				t.Errorf("error should explain the shared pool conflict, got: %v", err)
			}
		})
	}
}

func TestLoad_AcceptsPooledRulesWithIdenticalPolicy(t *testing.T) {
	yaml := `
targets:
  - name: a
    url: http://a:9000
  - name: b
    url: http://b:9000
routing:
  rules:
    - {host: h, path_prefix: /api, target_name: a, strip_path: true, auth: {required: true}, rate_limit: {requests_per_second: 10, burst: 20}}
    - {host: h, path_prefix: /api, target_name: b, strip_path: true, auth: {required: true}, rate_limit: {requests_per_second: 10, burst: 20}}
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("pooled rules with identical policy must load: %v", err)
	}
	if len(cfg.Routing.Rules) != 2 {
		t.Fatalf("both pooled rules must survive, got %d", len(cfg.Routing.Rules))
	}
}

func TestLoad_AllowsSameRouteDifferentMethods(t *testing.T) {
	yaml := `
targets:
  - name: a
    url: http://a:9000
  - name: b
    url: http://b:9000
routing:
  rules:
    - {host: h, path_prefix: /api, target_name: a, methods: [GET], auth: {required: true}}
    - {host: h, path_prefix: /api, target_name: b, methods: [POST]}
`
	path := writeTempConfig(t, yaml)
	if _, _, err := Load(path); err != nil {
		t.Fatalf("rules with different methods are different pools and must load: %v", err)
	}
}

func TestConfig_MaxRequestBodySizeDefault(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Server.EffectiveMaxRequestBodySize(); got != 10<<20 {
		t.Errorf("omitted max_request_body_size effective = %d, want %d", got, 10<<20)
	}
	if cfg.Server.MaxRequestBodySize == nil {
		t.Fatal("omitted max_request_body_size must materialize the 10 MiB default")
	}
}

func TestConfig_MaxRequestBodySizeZeroIsUnlimited(t *testing.T) {
	yaml := `
server:
  max_request_body_size: 0
targets:
  - name: "api"
    url: "http://api:9001"
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Server.EffectiveMaxRequestBodySize(); got != 0 {
		t.Errorf("explicit max_request_body_size: 0 must mean unlimited, got effective %d", got)
	}
}

func TestConfig_MaxRequestBodySizeExplicitLimit(t *testing.T) {
	yaml := `
server:
  max_request_body_size: 1048576
targets:
  - name: "api"
    url: "http://api:9001"
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Server.EffectiveMaxRequestBodySize(); got != 1048576 {
		t.Errorf("explicit max_request_body_size effective = %d, want 1048576", got)
	}
}

func minimalConfig() Config {
	return Config{
		Targets: []TargetConfig{{Name: "api", URL: "http://api:9001"}},
	}
}

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

func hasWarning(warnings []string, want string) bool {
	for _, w := range warnings {
		if w == want {
			return true
		}
	}
	return false
}

func TestLoad_WarnsOnUnknownKeys(t *testing.T) {
	yaml := `
bogus_top: true
server:
  port: 8080
  bogus_nested: 1
headers:
  forward_headers: true
targets:
  - name: "api"
    url: "http://api:9001"
    bogus_target: x
`
	path := writeTempConfig(t, yaml)
	_, warnings, err := Load(path)
	if err != nil {
		t.Fatalf("unknown keys must not fail loading: %v", err)
	}
	for _, want := range []string{"bogus_top", "server.bogus_nested", "headers.forward_headers", "targets.bogus_target"} {
		if !hasWarning(warnings, want) {
			t.Errorf("expected warning %q, got %v", want, warnings)
		}
	}
}

func TestLoad_NoWarningsOnFullyTaggedConfig(t *testing.T) {
	yaml := `
application:
  env: "dev"
  health_check: true
  circuit_breaker: true
  metrics_enabled: false
  metrics_allowed_ips: ["10.0.0.1"]
  max_idle_conns_per_host: 100
server:
  port: 8080
  read_timeout: 5s
  write_timeout: 10s
  idle_timeout: 120s
  max_request_body_size: 1048576
tls:
  enabled: false
  port: 443
  http_port: 80
  domains: ["api.example.com"]
  email: "admin@example.com"
  cache_dir: "/var/lib/api-gateway/certs"
  staging: false
  redirect_http: true
  directory_url: ""
static:
  apps:
    - path_prefix: "/"
      root_dir: "/srv"
      index_file: "index.html"
      max_age: 60
  skip_prefixes: ["/api"]
targets:
  - name: "api"
    url: "http://api:9001"
    timeout: 5s
    path_prefix: "/api"
    strip_prefix: true
    weight: 1
    health_check: "/health"
jwt:
  secret_key: "secret"
  public_key_file: ""
  algorithm: "HS256"
  validate_exp: true
  validate_iss: false
  expected_iss: ""
  validate_aud: false
  expected_aud: ""
  claim_mappings: ["sub"]
  required: false
basic_auth:
  enabled: false
  username: "u"
  password: "p"
  skip_paths: ["/health"]
logging:
  level: "info"
  format: "json"
  access_log: false
headers:
  strip_authorization: true
  claim_to_header:
    sub: "X-User-ID"
  add_headers:
    X-Gateway: "v1"
  sign_header: "X-Sign"
  cors:
    enabled: true
    allowed_origins: ["*"]
    allowed_methods: ["GET"]
    allowed_headers: ["Authorization"]
    expose_headers: ["X-User-ID"]
    max_age: 60
routing:
  rules:
    - host: "api.example.com"
      path_prefix: "/api"
      target_name: "api"
      methods: ["GET"]
      strip_path: true
      auth:
        required: true
        roles: ["admin"]
        roles_all: ["mfa"]
        strip_token: false
      rate_limit:
        requests_per_second: 10
        burst: 20
  global_limit:
    requests_per_second: 100
    burst: 200
permissions:
  enabled: false
  service_url: "http://perm"
  cache_ttl: 60s
  header_name: "X-Perm"
  invalidate_token: "t"
  api_key: "k"
webhooks:
  - name: "wh"
    transport: "webhook"
    nats_url: ""
    subject: ""
    webhook_url: "http://hook"
    trigger: "on_response"
    methods: ["POST"]
    on_status_codes: [200]
    exclude_paths: ["/health"]
    async: false
    include_request_body: true
    include_response_body: false
    batch_size: 10
    flush_interval: 100ms
discovery:
  enabled: false
  provider: "docker"
  host: "unix:///var/run/docker.sock"
  api_version: "v1.41"
  label_prefix: "gateway"
  service_name_labels: ["a"]
  network: "net"
  debounce: 1s
  resync_interval: 1m
  default_timeout: 1s
  state_file: "/tmp/state.json"
`
	path := writeTempConfig(t, yaml)
	_, warnings, err := Load(path)
	if err != nil {
		t.Fatalf("fully tagged valid config must load: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no unknown-key warnings, got %v", warnings)
	}
}

func TestLoad_AuthRolesAll(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
routing:
  rules:
    - path_prefix: "/api"
      target_name: "api"
      auth:
        required: true
        roles: ["admin", "support"]
        roles_all: ["mfa", "verified"]
`
	path := writeTempConfig(t, yaml)
	cfg, warnings, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no unknown-key warnings, got %v", warnings)
	}
	auth := cfg.Routing.Rules[0].Auth
	if auth == nil {
		t.Fatal("expected auth block")
	}
	if len(auth.Roles) != 2 || auth.Roles[0] != "admin" {
		t.Errorf("roles: got %v", auth.Roles)
	}
	if len(auth.RolesAll) != 2 || auth.RolesAll[0] != "mfa" {
		t.Errorf("roles_all: got %v", auth.RolesAll)
	}
}

func TestLoad_PermissionsEndpointDefaults(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
permissions:
  enabled: true
  service_url: "http://perm"
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Permissions.Method != "GET" {
		t.Errorf("default permissions.method = %q, want GET", cfg.Permissions.Method)
	}
	if cfg.Permissions.Path != "/api/v1/users/{user_id}/effective-permissions" {
		t.Errorf("default permissions.path = %q", cfg.Permissions.Path)
	}
	if cfg.Permissions.APIKeyHeader != "X-API-Key" {
		t.Errorf("default permissions.api_key_header = %q, want X-API-Key", cfg.Permissions.APIKeyHeader)
	}
}

func TestLoad_PermissionsEndpointCustom(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
permissions:
  enabled: true
  service_url: "http://perm"
  method: "POST"
  path: "/v2/users/{user_id}/perms"
  api_key_header: "Authorization"
`
	path := writeTempConfig(t, yaml)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Permissions.Method != "POST" {
		t.Errorf("permissions.method = %q, want POST", cfg.Permissions.Method)
	}
	if cfg.Permissions.Path != "/v2/users/{user_id}/perms" {
		t.Errorf("permissions.path = %q", cfg.Permissions.Path)
	}
	if cfg.Permissions.APIKeyHeader != "Authorization" {
		t.Errorf("permissions.api_key_header = %q, want Authorization", cfg.Permissions.APIKeyHeader)
	}
}

func TestLoad_PermissionsEnabledRequiresServiceURL(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
permissions:
  enabled: true
`
	path := writeTempConfig(t, yaml)
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected error when permissions enabled without service_url")
	}
}

func TestLoad_PermissionsPathRequiresUserIDPlaceholder(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
permissions:
  enabled: true
  service_url: "http://perm"
  path: "/api/v1/users/effective-permissions"
`
	path := writeTempConfig(t, yaml)
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected error when permissions.path lacks {user_id}")
	}
}

func TestLoad_PermissionsRejectsInvalidMethod(t *testing.T) {
	yaml := `
targets:
  - name: "api"
    url: "http://api:9001"
permissions:
  enabled: true
  service_url: "http://perm"
  method: "GE T"
`
	path := writeTempConfig(t, yaml)
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected error for permissions.method with whitespace")
	}
}

func TestValidate_PermissionsRejectsEmptyMethod(t *testing.T) {
	cfg := &Config{
		Targets: []TargetConfig{{Name: "api", URL: "http://api:9001"}},
		Permissions: PermissionsConfig{
			Enabled:    true,
			ServiceURL: "http://perm",
			Path:       "/api/v1/users/{user_id}/effective-permissions",
			Method:     "",
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty permissions.method")
	}
}

func TestLoad_EnvSubstitution(t *testing.T) {
	t.Setenv("TEST_ENV_VALUE", "prod-x")
	path := writeTempConfig(t, `
application:
  env: "${TEST_ENV_VALUE}"
targets:
  - name: backend
    url: "http://backend:80"
`)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != "prod-x" {
		t.Fatalf("env = %q, want %q", cfg.Env, "prod-x")
	}
}

func TestLoad_MissingEnvFails(t *testing.T) {
	os.Unsetenv("TEST_MISSING_ENV")
	path := writeTempConfig(t, `
application:
  env: "${TEST_MISSING_ENV}"
`)
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected error for undefined env var")
	}
	if !strings.Contains(err.Error(), "TEST_MISSING_ENV") {
		t.Fatalf("error %q does not name the missing variable", err)
	}
}

func TestLoad_EmptyEnvFails(t *testing.T) {
	t.Setenv("TEST_EMPTY_ENV", "")
	path := writeTempConfig(t, `
application:
  env: "${TEST_EMPTY_ENV}"
`)
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected error for empty env var")
	}
	if !strings.Contains(err.Error(), "TEST_EMPTY_ENV") {
		t.Fatalf("error %q does not name the empty variable", err)
	}
}

func TestLoad_MultipleMissingEnvsListed(t *testing.T) {
	os.Unsetenv("TEST_MISS_B")
	os.Unsetenv("TEST_MISS_A")
	path := writeTempConfig(t, `
application:
  env: "${TEST_MISS_B}"
jwt:
  secret_key: "${TEST_MISS_A}"
`)
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected error for undefined env vars")
	}
	if !strings.Contains(err.Error(), "TEST_MISS_A") || !strings.Contains(err.Error(), "TEST_MISS_B") {
		t.Fatalf("error %q must name both variables", err)
	}
}

func TestLoad_EscapedEnvStaysLiteral(t *testing.T) {
	path := writeTempConfig(t, `
application:
  env: "$${LITERAL}"
targets:
  - name: backend
    url: "http://backend:80"
`)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != "${LITERAL}" {
		t.Fatalf("env = %q, want %q", cfg.Env, "${LITERAL}")
	}
}

func TestLoadLenient_MissingEnvStaysLiteral(t *testing.T) {
	os.Unsetenv("TEST_LENIENT_MISSING")
	path := writeTempConfig(t, `
application:
  env: "${TEST_LENIENT_MISSING}"
targets:
  - name: backend
    url: "http://backend:80"
`)
	cfg, _, err := LoadLenient(path)
	if err != nil {
		t.Fatalf("lenient load must not fail on a missing env var: %v", err)
	}
	if cfg.Env != "${TEST_LENIENT_MISSING}" {
		t.Fatalf("env = %q, want literal %q", cfg.Env, "${TEST_LENIENT_MISSING}")
	}
}

func TestLoadLenient_ExpandsDefinedEnv(t *testing.T) {
	t.Setenv("TEST_LENIENT_VALUE", "prod-x")
	path := writeTempConfig(t, `
application:
  env: "${TEST_LENIENT_VALUE}"
targets:
  - name: backend
    url: "http://backend:80"
`)
	cfg, _, err := LoadLenient(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != "prod-x" {
		t.Fatalf("env = %q, want %q", cfg.Env, "prod-x")
	}
}

func TestLoadLenient_NoTargets(t *testing.T) {
	path := writeTempConfig(t, "targets: []\n")
	cfg, _, err := LoadLenient(path)
	if err != nil {
		t.Fatalf("lenient load must not require targets: %v", err)
	}
	if len(cfg.Targets) != 0 {
		t.Fatalf("targets = %d, want 0", len(cfg.Targets))
	}
}

func TestLoadLenient_WarnsOnUnknownKeys(t *testing.T) {
	path := writeTempConfig(t, `
bogus_top: true
server:
  port: 8080
  bogus_nested: 1
targets:
  - name: "api"
    url: "http://api:9001"
    bogus_target: x
`)
	_, warnings, err := LoadLenient(path)
	if err != nil {
		t.Fatalf("unknown keys must not fail lenient loading: %v", err)
	}
	for _, want := range []string{"bogus_top", "server.bogus_nested", "targets.bogus_target"} {
		if !hasWarning(warnings, want) {
			t.Errorf("expected warning %q, got %v", want, warnings)
		}
	}
}
