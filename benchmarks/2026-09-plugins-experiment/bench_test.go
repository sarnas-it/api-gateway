package experiment

import (
	"context"
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
			lat = append(lat, time.Since(start))
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
