package config

import (
	"net/http/httptest"
	"os"
	"testing"
)

// Бенчмарки маршрутизации: матчеры выполняются на каждом запросе, поэтому
// стоимость regex/headers/query должна быть измерима, а не «на глаз».
const benchYAML = `
targets:
  - name: t
    url: http://t:1
routing:
  rules:
    - path_prefix: "/static"
      target_name: t
    - path_prefix: "/api/v1/auth"
      target_name: t
    - path_prefix: "/api/v1/cart"
      target_name: t
    - path_prefix: "/api/v1/catalog"
      target_name: t
    - path_prefix: "/api/v1/payment"
      target_name: t
    - path_prefix: "/api/v1/orders"
      target_name: t
    - path_prefix: "/api/v1/users"
      target_name: t
    - path_prefix: "/api/v1/files"
      target_name: t
    - path_prefix: "/api/v1/chat"
      target_name: t
    - path_prefix: "/"
      target_name: t
    - path_prefix: "/"
      target_name: t
      headers:
        X-Tenant: "partner"
    - path_regex: "^/api/v[0-9]+/experimental/"
      target_name: t
    - path_prefix: "/"
      target_name: t
      query:
        debug: "1"
`

func benchConfig(b *testing.B) *Config {
	b.Helper()
	f, err := os.CreateTemp("", "bench-config-*.yaml")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { os.Remove(f.Name()) })
	if _, err := f.WriteString(benchYAML); err != nil {
		b.Fatal(err)
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	cfg, _, err := Load(f.Name())
	if err != nil {
		b.Fatal(err)
	}
	return cfg
}

func BenchmarkFindTarget_Prefix(b *testing.B) {
	cfg := benchConfig(b)
	req := httptest.NewRequest("GET", "/api/v1/cart/items", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if tg, _ := cfg.FindTargetForRequest(req); tg == nil {
			b.Fatal("no target")
		}
	}
}

func BenchmarkFindTarget_Header(b *testing.B) {
	cfg := benchConfig(b)
	req := httptest.NewRequest("GET", "/api/v1/cart/items", nil)
	req.Header.Set("X-Tenant", "partner")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if tg, _ := cfg.FindTargetForRequest(req); tg == nil {
			b.Fatal("no target")
		}
	}
}

func BenchmarkFindTarget_PathRegex(b *testing.B) {
	cfg := benchConfig(b)
	req := httptest.NewRequest("GET", "/api/v2/experimental/feature", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if tg, _ := cfg.FindTargetForRequest(req); tg == nil {
			b.Fatal("no target")
		}
	}
}

func BenchmarkFindTarget_Query(b *testing.B) {
	cfg := benchConfig(b)
	req := httptest.NewRequest("GET", "/api/v1/cart/items?debug=1", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if tg, _ := cfg.FindTargetForRequest(req); tg == nil {
			b.Fatal("no target")
		}
	}
}

func BenchmarkFindTarget_PathOnly(b *testing.B) {
	cfg := benchConfig(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if tg, _ := cfg.FindTargetForPath("/api/v1/cart/items", "GET"); tg == nil {
			b.Fatal("no target")
		}
	}
}
