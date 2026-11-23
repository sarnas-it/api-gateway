package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"code.sarnas.ru/api-gateway/internal/config"
)

// TestMetricsHandlerAndBoundedRouteLabel проверяет /metrics и /metrics/prometheus
// на живом MultiProxy (в одном тесте: NewMetrics(true) регистрирует expvar-
// переменные глобально и второй экземпляр паникует).
//
// Про Handler: раньше он регистрировал expvar-переменную на каждый вызов и
// паниковал на втором запросе к /metrics.
//
// Про route: метка ограничена маршрутами конфига, а не URL-путями из запросов.
// Боты перебирают случайные пути, и раньше каждый новый первый сегмент
// (/wp-login.php, /.env, /<случайная строка>) создавал новые серии gateway_*
// (по 13 бакетов гистограммы на путь). Кардинальность росла без предела и
// выедала -remoteWrite.maxHourlySeries у vmagent, из-за чего переставали
// скрапиться остальные таргеты.
func TestMetricsHandlerAndBoundedRouteLabel(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := &config.Config{}
	cfg.MetricsEnabled = true
	cfg.Server.Port = 8080
	cfg.JWT.SecretKey = "test-secret"
	cfg.JWT.Algorithm = "HS256"
	cfg.Targets = []config.TargetConfig{{Name: "frontend", URL: backend.URL, Timeout: time.Second}}
	cfg.Routing.Rules = []config.RoutingRule{{PathPrefix: "/", TargetName: "frontend"}}

	mp, err := NewMultiProxy(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("NewMultiProxy: %v", err)
	}

	// /metrics можно запрашивать многократно, без паники.
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		mp.metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status %d, want 200", i, rec.Code)
		}
	}

	// Пятьдесят «сканов» по несуществующим путям, все попадают в один маршрут.
	for i := 0; i < 50; i++ {
		mp.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, fmt.Sprintf("/scan-%d.php", i), nil))
	}

	rec := httptest.NewRecorder()
	mp.metrics.PromHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics/prometheus", nil))
	body := rec.Body.String()

	routes := map[string]struct{}{}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, `gateway_requests_total{`) {
			continue
		}
		const prefix = `route="`
		start := strings.Index(line, prefix)
		if start < 0 {
			t.Fatalf("no route label in %q", line)
		}
		rest := line[start+len(prefix):]
		routes[rest[:strings.Index(rest, `"`)]] = struct{}{}
	}
	if len(routes) != 1 {
		t.Fatalf("route label must be bounded by routing rules, got %d values: %v", len(routes), routes)
	}
	if _, ok := routes["frontend"]; !ok {
		t.Fatalf("want route=frontend, got %v", routes)
	}

	for _, want := range []string{
		`gateway_requests_total{method="GET",route="frontend",status="200"} 50`,
		`gateway_request_duration_seconds_bucket{method="GET",route="frontend",status="200",le="0.025"} 50`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
}
