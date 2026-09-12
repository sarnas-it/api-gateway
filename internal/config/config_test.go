package config

import (
	"os"
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
	cfg, err := Load(path)
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

func TestLoad_MissingTarget(t *testing.T) {
	yaml := `
server:
  port: 8080
targets: []
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
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
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate target name")
	}
}

func TestLoad_RejectsMalformedTargetURL(t *testing.T) {
	yaml := `
targets:
  - name: "bad"
    url: "http://[::1"
`
	path := writeTempConfig(t, yaml)
	if _, err := Load(path); err == nil {
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
	cfg, err := Load(path)
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
	cfg, err := Load(path)
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
	cfg, err := Load(path)
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
	cfg, err := Load(path)
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
	_, err := Load(path)
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
	_, err := Load(path)
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
	cfg, err := Load(path)
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
	cfg, err := Load(path)
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
	if _, err := Load(path); err == nil {
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
	if _, err := Load(path); err == nil {
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
	if _, err := Load(path); err != nil {
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
	cfg, err := Load(path)
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
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxIdleConnsPerHost != 250 {
		t.Errorf("max_idle_conns_per_host = %d, want 250", cfg.MaxIdleConnsPerHost)
	}
}
