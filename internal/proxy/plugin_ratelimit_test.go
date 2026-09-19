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
	cmd.Dir = "../.." // репозиторий — два уровня выше internal/proxy
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
		JWT:     config.JWTConfig{SecretKey: "benchsecret", Algorithm: "HS256"},
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
