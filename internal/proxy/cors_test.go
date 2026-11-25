package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"code.sarnas.ru/api-gateway/internal/config"
)

// corsTestProxy собирает MultiProxy только с конфигом: setCORSHeaders
// зависит исключительно от него.
func corsTestProxy(cfg *config.Config) *MultiProxy {
	mp := &MultiProxy{}
	mp.config.Store(cfg)
	return mp
}

// env: dev больше не включает мягкий CORS: без headers.cors заголовков нет.
func TestSetCORSHeaders_DevWithoutCORSConfigIsDisabled(t *testing.T) {
	mp := corsTestProxy(&config.Config{App: config.App{Env: "dev"}})
	header := make(http.Header)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://any.example")

	mp.setCORSHeaders(header, req)

	if got := header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty", got)
	}
	if got := header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want empty", got)
	}
}

// Явно включённый headers.cors с точным origin работает и в dev.
func TestSetCORSHeaders_DevWithExplicitCORSReflectsAllowedOrigin(t *testing.T) {
	cfg := &config.Config{App: config.App{Env: "dev"}}
	cfg.Headers.CORS = &config.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"https://app.example"},
	}
	mp := corsTestProxy(cfg)
	header := make(http.Header)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://app.example")

	mp.setCORSHeaders(header, req)

	if got := header.Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Errorf("Access-Control-Allow-Origin = %q, want https://app.example", got)
	}
	if got := header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want true", got)
	}
}

// Origin вне allowlist не получает CORS-заголовков даже в dev.
func TestSetCORSHeaders_DevWithExplicitCORSRejectsUnknownOrigin(t *testing.T) {
	cfg := &config.Config{App: config.App{Env: "dev"}}
	cfg.Headers.CORS = &config.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"https://app.example"},
	}
	mp := corsTestProxy(cfg)
	header := make(http.Header)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example")

	mp.setCORSHeaders(header, req)

	if got := header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty", got)
	}
	if got := header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want empty", got)
	}
}

// Регресс: prod без headers.cors по-прежнему не выставляет CORS.
func TestSetCORSHeaders_ProdWithoutCORSConfigHasNoHeaders(t *testing.T) {
	mp := corsTestProxy(&config.Config{App: config.App{Env: "prod"}})
	header := make(http.Header)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://any.example")

	mp.setCORSHeaders(header, req)

	if got := header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty", got)
	}
	if got := header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want empty", got)
	}
}

// Старт с env: dev без headers.cors громко предупреждает, но не падает.
func TestNewMultiProxy_WarnsOnDevWithoutCORS(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	cfg := &config.Config{App: config.App{Env: "dev"}}
	cfg.JWT.SecretKey = "test-secret"
	cfg.JWT.Algorithm = "HS256"
	cfg.Targets = []config.TargetConfig{{Name: "backend", URL: "http://127.0.0.1:1", Timeout: time.Second}}

	if _, err := NewMultiProxy(cfg, zap.New(core)); err != nil {
		t.Fatalf("NewMultiProxy: %v", err)
	}
	entries := logs.FilterMessageSnippet("headers.cors").All()
	if len(entries) == 0 {
		t.Fatal("no warning about disabled CORS")
	}
	if entries[0].Level != zap.WarnLevel {
		t.Errorf("level = %v, want warn", entries[0].Level)
	}
}
