package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/basili4-1982/api-gateway/internal/config"
)

func newTestProxy(targets ...*config.TargetConfig) *MultiProxy {
	mp := &MultiProxy{
		targets: make(map[string]*TargetProxy),
		logger:  zap.NewNop(),
	}
	var cfg config.Config
	mp.config.Store(&cfg)
	for _, tc := range targets {
		mp.targets[tc.Name] = &TargetProxy{config: tc, healthy: true}
	}
	return mp
}

func decodeHealth(t *testing.T, body []byte) (string, []healthService) {
	t.Helper()
	var out struct {
		Status   string          `json:"status"`
		Services []healthService `json:"services"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return out.Status, out.Services
}

func TestServeHealthAllHealthy(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	mp := newTestProxy(
		&config.TargetConfig{Name: "backend", URL: "http://backend:8080", HealthCheck: up.URL},
		&config.TargetConfig{Name: "frontend", URL: "http://frontend:80", HealthCheck: up.URL},
	)

	rec := httptest.NewRecorder()
	mp.serveHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	status, services := decodeHealth(t, rec.Body.Bytes())
	if status != "ok" {
		t.Errorf("status = %q, want ok", status)
	}
	if len(services) != 2 {
		t.Fatalf("services = %d, want 2", len(services))
	}
	if services[0].Name != "backend" || services[1].Name != "frontend" {
		t.Errorf("services not sorted: %q, %q", services[0].Name, services[1].Name)
	}
	for _, s := range services {
		if !s.Healthy {
			t.Errorf("service %q healthy = false, want true", s.Name)
		}
	}
}

func TestServeHealthDegraded(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	downURL := down.URL
	down.Close() // теперь недоступен

	mp := newTestProxy(
		&config.TargetConfig{Name: "ok-svc", URL: "http://ok", HealthCheck: up.URL},
		&config.TargetConfig{Name: "down-svc", URL: "http://down", HealthCheck: downURL},
	)

	rec := httptest.NewRecorder()
	mp.serveHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	status, services := decodeHealth(t, rec.Body.Bytes())
	if status != "degraded" {
		t.Errorf("status = %q, want degraded", status)
	}
	for _, s := range services {
		switch s.Name {
		case "ok-svc":
			if !s.Healthy {
				t.Errorf("ok-svc healthy = false, want true")
			}
		case "down-svc":
			if s.Healthy {
				t.Errorf("down-svc healthy = true, want false")
			}
			if s.Error == "" {
				t.Errorf("down-svc error empty, want non-empty")
			}
		}
	}
}

func TestServeHealthNoCheckDefault(t *testing.T) {
	mp := newTestProxy(
		&config.TargetConfig{Name: "no-check", URL: "http://no-check:9000"},
	)

	rec := httptest.NewRecorder()
	mp.serveHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	status, services := decodeHealth(t, rec.Body.Bytes())
	if status != "ok" {
		t.Errorf("status = %q, want ok", status)
	}
	if len(services) != 1 || !services[0].Healthy {
		t.Errorf("service without health_check should be healthy: %+v", services)
	}
}

func TestHealthEndpointMiddlewarePassthrough(t *testing.T) {
	mp := newTestProxy()
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true })

	handler := healthEndpointMiddleware(mp)(next)

	// Не целевой путь — уходит дальше по цепочке.
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/other", nil))
	if !called {
		t.Fatal("middleware should pass through non-health path")
	}

	// Целевой путь — обрабатывается здесь, next не вызывается.
	called = false
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if called {
		t.Fatal("middleware must not pass through /api/health")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", rec.Code)
	}
}
