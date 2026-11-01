package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"code.sarnas.ru/api-gateway/internal/config"
)

func lbCandidate(name string, active int64) *TargetProxy {
	tp := &TargetProxy{
		config:           &config.TargetConfig{Name: name},
		healthy:          true,
		cbState:          stateClosed,
		failureThreshold: defaultFailureThreshold,
		cbTimeout:        defaultCBTimeout,
	}
	tp.active.Store(active)
	return tp
}

func lbRoute(policy string, targets ...*TargetProxy) *RouteConfig {
	rc := &RouteConfig{lbPolicy: policy}
	for _, tp := range targets {
		rc.Targets = append(rc.Targets, tp)
		rc.weights = append(rc.weights, 1)
		rc.totalWeight++
	}
	return rc
}

func TestPickTargetLeastConn(t *testing.T) {
	a, b, c := lbCandidate("a", 3), lbCandidate("b", 1), lbCandidate("c", 2)
	rc := lbRoute("least_conn", a, b, c)
	if got := rc.pickTarget(false); got != b {
		t.Fatalf("least_conn picked %s, want b", got.config.Name)
	}
}

func TestPickTargetIPHashStable(t *testing.T) {
	a, b, c := lbCandidate("a", 0), lbCandidate("b", 0), lbCandidate("c", 0)
	rc := lbRoute("ip_hash", a, b, c)
	first := rc.pickTarget(false, "203.0.113.7")
	for i := 0; i < 10; i++ {
		if got := rc.pickTarget(false, "203.0.113.7"); got != first {
			t.Fatalf("ip_hash changed target: %s vs %s", first.config.Name, got.config.Name)
		}
	}
}

func TestPickTargetRandomUsesMultipleTargets(t *testing.T) {
	a, b, c := lbCandidate("a", 0), lbCandidate("b", 0), lbCandidate("c", 0)
	rc := lbRoute("random", a, b, c)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		seen[rc.pickTarget(false).config.Name] = true
	}
	if len(seen) < 2 {
		t.Fatalf("random picked only %v", seen)
	}
}

func TestRetryOnTransportError(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer good.Close()

	cfg := &config.Config{
		Targets: []config.TargetConfig{
			{Name: "bad", URL: "http://127.0.0.1:1"},
			{Name: "good", URL: good.URL},
		},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{
			{PathPrefix: "/", TargetName: "bad", Retries: 1},
			{PathPrefix: "/", TargetName: "good", Retries: 1},
		}},
	}
	mp := &MultiProxy{
		targets:     map[string]*TargetProxy{},
		routeByRule: map[*config.RoutingRule]*RouteConfig{},
		logger:      zap.NewNop(),
		metrics:     NewMetrics(false),
	}
	mp.config.Store(cfg)
	for i := range cfg.Targets {
		tp, err := mp.createTargetProxy(&cfg.Targets[i], false)
		if err != nil {
			t.Fatal(err)
		}
		mp.targets[cfg.Targets[i].Name] = tp
	}
	mp.rebuildRouteConfigs(cfg)
	mp.handler = mp.proxyHandler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	mp.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}
