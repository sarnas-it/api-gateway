package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"go.uber.org/zap"
)

func buildEventsPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "events")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/plugins/events")
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build events plugin: %v\n%s", err, out)
	}
	return bin
}

func TestMultiProxyEventsPlugin(t *testing.T) {
	var received atomic.Int64
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	bin := buildEventsPlugin(t)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{Enabled: true, Webhooks: &config.PluginSpec{Path: bin, Transport: "grpc"}},
		JWT:     config.JWTConfig{SecretKey: "benchsecret", Algorithm: "HS256"},
		Targets: []config.TargetConfig{{Name: "b", URL: backend.URL}},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{{PathPrefix: "/", TargetName: "b"}}},
		Webhooks: []config.WebhookConfig{{
			Name: "h", Transport: config.TransportWebhook, WebhookURL: sink.URL,
			Trigger: config.TriggerOnResponse, Async: true,
		}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mp, err := NewMultiProxyWithPlugins(ctx, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	defer mp.Stop(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mp.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if received.Load() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("webhook event not delivered to sink")
}
