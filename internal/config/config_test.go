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
