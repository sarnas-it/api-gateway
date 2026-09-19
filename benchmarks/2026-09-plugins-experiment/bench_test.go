package experiment

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/proxy"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

// BenchmarkGateway гоняет реальный MultiProxy с конфигом из GATEWAY_BENCH_CONFIG.
// Запуск из корня репозитория:
//
//	GATEWAY_BENCH_CONFIG=benchmarks/2026-09-plugins-experiment/configs/baseline-jwt.yaml go test -run '^$' -bench BenchmarkGateway -benchmem -count=5 -benchtime=2s ./benchmarks/2026-09-plugins-experiment/
//
// Для .so-сценариев (jwt.so) тестовый бинарник нужно собирать с -trimpath (иначе хеш пакета
// не совпадёт с собранным .so):
//
//	go test -trimpath -run '^$' -bench BenchmarkGateway ...
func BenchmarkGateway(b *testing.B) {
	// bench_test.go лежит двумя уровнями ниже корня репозитория; go test запускает
	// бинарник с cwd = каталог пакета, поэтому пути из конфигов (bin/plugins/...)
	// и repo-root-относительный GATEWAY_BENCH_CONFIG разрешаем от корня.
	repoRoot, _ := filepath.Abs("../..")

	path := os.Getenv("GATEWAY_BENCH_CONFIG")
	if path == "" {
		b.Skip("GATEWAY_BENCH_CONFIG not set")
	}
	if !filepath.IsAbs(path) {
		rootPath := filepath.Join(repoRoot, path)
		if _, err := os.Stat(rootPath); err == nil {
			path = rootPath
		}
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		b.Fatalf("load config: %v", err)
	}

	for _, spec := range []*config.PluginSpec{
		cfg.Plugins.JWT, cfg.Plugins.RateLimit, cfg.Plugins.Webhooks, cfg.Plugins.Discovery,
	} {
		if spec != nil && spec.Path != "" && !filepath.IsAbs(spec.Path) {
			spec.Path = filepath.Join(repoRoot, spec.Path)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mp, err := proxy.NewMultiProxyWithPlugins(ctx, cfg, zap.NewNop())
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
	var mu sync.Mutex
	var lat []time.Duration
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// Свежий запрос на итерацию: middleware мутирует заголовки
			// (X-Request-ID/X-Trace-ID), общий *http.Request даёт race.
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("Authorization", "Bearer "+signed)
			rec := httptest.NewRecorder()
			start := time.Now()
			mp.ServeHTTP(rec, r)
			mu.Lock()
			lat = append(lat, time.Since(start))
			mu.Unlock()
			if rec.Code != http.StatusOK {
				b.Fatalf("unexpected status %d", rec.Code)
			}
		}
	})
	b.StopTimer()

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	if n := len(lat); n > 0 {
		b.Logf("  p50=%s p99=%s", lat[n/2], lat[n*99/100])
	}
}
