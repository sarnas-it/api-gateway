package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/jwtutil"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

func signTestToken(claims jwt.MapClaims, secret string) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, _ := token.SignedString([]byte(secret))
	return s
}

// newTestMultiProxy builds a bare MultiProxy with just enough wired up to
// exercise modifyRequest directly (skips NewMultiProxy, whose NewMetrics()
// call registers process-global expvar names and panics if constructed more
// than once per test binary).
func newTestMultiProxy(t *testing.T, authRequired bool) *MultiProxy {
	t.Helper()
	cfg := &config.Config{
		JWT: config.JWTConfig{
			SecretKey:   "test-secret",
			Algorithm:   "HS256",
			ValidateExp: true,
			Required:    authRequired,
		},
	}
	jwtValidator, err := jwtutil.NewJWTValidator(
		cfg.JWT.SecretKey, cfg.JWT.Algorithm,
		cfg.JWT.ValidateExp, cfg.JWT.ValidateIss, cfg.JWT.ExpectedIss,
		cfg.JWT.ValidateAud, cfg.JWT.ExpectedAud, cfg.JWT.PublicKeyFile,
	)
	if err != nil {
		t.Fatal(err)
	}
	mp := &MultiProxy{jwtValidator: jwtValidator, logger: zap.NewNop()}
	mp.config.Store(cfg)
	return mp
}

// A routing rule with no per-route `auth:` block — only the global
// jwt.required flag governs whether a token is required on it.
var noAuthBlockRule = &config.RoutingRule{PathPrefix: "/api", TargetName: "x"}

func TestModifyRequest_RejectsInvalidTokenOnRouteWithoutAuthBlock(t *testing.T) {
	mp := newTestMultiProxy(t, true)
	r := httptest.NewRequest("GET", "/api/anything", nil)
	r.Header.Set("Authorization", "Bearer not-a-real-jwt")

	if err := mp.modifyRequest(r, nil, noAuthBlockRule); err == nil {
		t.Fatal("expected garbage token to be rejected on a route relying on global jwt.required, got nil error")
	}
}

func TestModifyRequest_AcceptsValidTokenOnRouteWithoutAuthBlock(t *testing.T) {
	mp := newTestMultiProxy(t, true)
	token := signTestToken(jwt.MapClaims{
		"sub": "123",
		"exp": float64(1893456000), // 2030-01-01
	}, "test-secret")
	r := httptest.NewRequest("GET", "/api/anything", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	if err := mp.modifyRequest(r, nil, noAuthBlockRule); err != nil {
		t.Fatalf("expected valid token to be accepted, got %v", err)
	}
}

func TestSetHealthy_RecoveredHealthCheckHalfOpensCircuit(t *testing.T) {
	tp := &TargetProxy{
		healthy:          true,
		cbState:          stateClosed,
		failureThreshold: defaultFailureThreshold,
		cbTimeout:        defaultCBTimeout,
	}

	// Три подряд ошибки транспорта открывают цепь.
	tp.recordCall(errTest)
	tp.recordCall(errTest)
	tp.recordCall(errTest)
	if tp.cbState != stateOpen {
		t.Fatalf("expected circuit to be open after failures, got %v", tp.cbState)
	}

	// Health check подтвердил восстановление — не ждём cbTimeout.
	tp.setHealthy(true)
	if tp.cbState != stateHalfOpen {
		t.Fatalf("expected circuit to half-open on health check recovery, got %v", tp.cbState)
	}
	if !tp.halfOpenProbe.Load() {
		t.Fatal("expected a probe to be armed after health check recovery")
	}
}

var errTest = fmt.Errorf("boom")

func TestModifyRequest_OptionalAuthIgnoresInvalidToken(t *testing.T) {
	mp := newTestMultiProxy(t, false)
	r := httptest.NewRequest("GET", "/api/anything", nil)
	r.Header.Set("Authorization", "Bearer not-a-real-jwt")

	if err := mp.modifyRequest(r, nil, noAuthBlockRule); err != nil {
		t.Fatalf("expected optional auth to let the request through, got %v", err)
	}
}

func TestIPRateLimiter_Allow(t *testing.T) {
	rl := NewIPRateLimiter(10, 5)

	ip := "192.168.1.1"
	for i := 0; i < 5; i++ {
		if !rl.Allow(ip) {
			t.Fatalf("expected allow at iteration %d", i)
		}
	}
}

func TestIPRateLimiter_Deny(t *testing.T) {
	rl := NewIPRateLimiter(1, 1)

	ip := "10.0.0.1"
	rl.Allow(ip)

	if rl.Allow(ip) {
		t.Fatal("expected deny after burst consumed")
	}
}

func TestIPRateLimiter_DifferentIPs(t *testing.T) {
	rl := NewIPRateLimiter(0, 0)

	if rl.Allow("10.0.0.1") {
		t.Fatal("expected deny for 10.0.0.1")
	}
	if rl.Allow("10.0.0.2") {
		t.Fatal("expected deny for 10.0.0.2")
	}
}

func TestGetClientIP_XForwardedFor(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	r.RemoteAddr = "192.168.1.1:12345"

	ip := getClientIP(r)
	if ip != "203.0.113.1" {
		t.Errorf("expected 203.0.113.1, got %s", ip)
	}
}

func TestGetClientIP_RemoteAddr(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:8080"

	ip := getClientIP(r)
	if ip != "10.0.0.5" {
		t.Errorf("expected 10.0.0.5, got %s", ip)
	}
}

func TestRouteLookup(t *testing.T) {
	// start test server as target
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	targetSrv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv2.Close()

	_ = targetSrv
	_ = targetSrv2
}

func TestReload_RecreatesTargetWhenURLChanges(t *testing.T) {
	cfg := &config.Config{
		Targets: []config.TargetConfig{
			{Name: "svc", URL: "http://svc:9000", Timeout: 5 * time.Second},
		},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{
			{PathPrefix: "/api", TargetName: "svc"},
		}},
	}
	mp := &MultiProxy{
		targets:     map[string]*TargetProxy{},
		routeByRule: map[*config.RoutingRule]*RouteConfig{},
		logger:      zap.NewNop(),
	}
	mp.config.Store(cfg)

	old, err := mp.createTargetProxy(&cfg.Targets[0], false)
	if err != nil {
		t.Fatal(err)
	}
	mp.targets["svc"] = old

	newCfg := &config.Config{
		Targets: []config.TargetConfig{
			{Name: "svc", URL: "http://svc:9999", Timeout: 5 * time.Second},
		},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{
			{PathPrefix: "/api", TargetName: "svc"},
		}},
	}
	if err := mp.Reload(newCfg); err != nil {
		t.Fatal(err)
	}

	got := mp.targets["svc"]
	if got == old {
		t.Fatal("target proxy must be recreated when URL changes")
	}
	if got.targetURL.Host != "svc:9999" {
		t.Errorf("target URL not updated: %s", got.targetURL.Host)
	}
}

func TestReload_KeepsOldTargetWhenReplacementFails(t *testing.T) {
	cfg := &config.Config{
		Targets: []config.TargetConfig{
			{Name: "svc", URL: "http://svc:9000", Timeout: 5 * time.Second},
		},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{
			{PathPrefix: "/api", TargetName: "svc"},
		}},
	}
	mp := &MultiProxy{
		targets:     map[string]*TargetProxy{},
		routeByRule: map[*config.RoutingRule]*RouteConfig{},
		logger:      zap.NewNop(),
	}
	mp.config.Store(cfg)

	oldURL, err := url.Parse("http://svc:9000")
	if err != nil {
		t.Fatal(err)
	}
	old := &TargetProxy{
		config:      &cfg.Targets[0],
		targetURL:   oldURL,
		healthCheck: &HealthChecker{stopCh: make(chan struct{})},
	}
	mp.targets["svc"] = old

	// Malformed URL makes createTargetProxy fail after targetChanged == true.
	badCfg := &config.Config{
		Targets: []config.TargetConfig{
			{Name: "svc", URL: "http://[::1", Timeout: 5 * time.Second},
		},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{
			{PathPrefix: "/api", TargetName: "svc"},
		}},
	}

	if err := mp.Reload(badCfg); err == nil {
		t.Fatal("expected reload to fail on malformed target URL")
	}
	if got := mp.targets["svc"]; got != old {
		t.Fatal("old target must be kept when replacement creation fails")
	}
	select {
	case <-old.healthCheck.stopCh:
		t.Fatal("old healthcheck must not be stopped when replacement creation fails")
	default:
	}

	// Повторный reload с той же ошибкой не должен паниковать (двойное закрытие).
	if err := mp.Reload(badCfg); err == nil {
		t.Fatal("expected second reload to fail on malformed target URL")
	}
}

// TestReload_MultiTargetFailureLeavesStateIntact проверяет атомарность Reload:
// если создание одного из новых таргетов падает, старые config/targets/route
// configs должны остаться нетронутыми, а healthcheck'и — не остановленными.
func TestReload_MultiTargetFailureLeavesStateIntact(t *testing.T) {
	oldCfg := &config.Config{
		Targets: []config.TargetConfig{
			{Name: "a", URL: "http://a:9000", Timeout: 5 * time.Second},
			{Name: "b", URL: "http://b:9000", Timeout: 5 * time.Second},
		},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{
			{PathPrefix: "/a", TargetName: "a", RateLimit: &config.RateLimitRule{RequestsPerSecond: 1, Burst: 1}},
			{PathPrefix: "/b", TargetName: "b"},
		}},
	}
	mp := &MultiProxy{
		targets:     map[string]*TargetProxy{},
		routeByRule: map[*config.RoutingRule]*RouteConfig{},
		logger:      zap.NewNop(),
	}
	mp.config.Store(oldCfg)

	oldA := &TargetProxy{config: &oldCfg.Targets[0], healthCheck: &HealthChecker{stopCh: make(chan struct{})}}
	oldB := &TargetProxy{config: &oldCfg.Targets[1], healthCheck: &HealthChecker{stopCh: make(chan struct{})}}
	mp.targets["a"] = oldA
	mp.targets["b"] = oldB
	mp.rebuildRouteConfigs(oldCfg)

	oldRuleA := &oldCfg.Routing.Rules[0]
	oldRC := mp.routeByRule[oldRuleA]
	if oldRC == nil || oldRC.RateLimit == nil {
		t.Fatal("test setup: expected route config with rate limit")
	}

	// Первый таргет меняет URL (будет пересоздан), второй — с битым URL (падение).
	badCfg := &config.Config{
		Targets: []config.TargetConfig{
			{Name: "a", URL: "http://a:9999", Timeout: 5 * time.Second},
			{Name: "b", URL: "http://[::1", Timeout: 5 * time.Second},
		},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{
			{PathPrefix: "/a", TargetName: "a", RateLimit: &config.RateLimitRule{RequestsPerSecond: 99, Burst: 99}},
			{PathPrefix: "/b", TargetName: "b"},
		}},
	}

	if err := mp.Reload(badCfg); err == nil {
		t.Fatal("expected reload to fail on malformed second target URL")
	}

	if mp.config.Load() != oldCfg {
		t.Fatal("config must not be swapped on failed reload")
	}
	if mp.targets["a"] != oldA || mp.targets["b"] != oldB {
		t.Fatal("targets must not be swapped on failed reload")
	}
	if mp.routeByRule[oldRuleA] != oldRC || mp.routeByRule[oldRuleA].RateLimit != oldRC.RateLimit {
		t.Fatal("route configs must not be rebuilt on failed reload")
	}
	for name, old := range map[string]*TargetProxy{"a": oldA, "b": oldB} {
		select {
		case <-old.healthCheck.stopCh:
			t.Fatalf("old healthcheck for target %s must not be stopped on failed reload", name)
		default:
		}
	}
}

func TestHealthCheckerStop_Idempotent(t *testing.T) {
	hc := &HealthChecker{stopCh: make(chan struct{})}
	hc.Stop()
	hc.Stop()

	var nilHC *HealthChecker
	nilHC.Stop()
}

func TestCreateTargetProxy_UsesConfiguredConnPool(t *testing.T) {
	cfg := &config.Config{
		App: config.App{MaxIdleConnsPerHost: 250},
		Targets: []config.TargetConfig{
			{Name: "a", URL: "http://a:1"},
			{Name: "b", URL: "http://b:1"},
		},
	}
	mp := &MultiProxy{logger: zap.NewNop()}
	mp.config.Store(cfg)

	tp, err := mp.createTargetProxy(&cfg.Targets[0], false)
	if err != nil {
		t.Fatal(err)
	}
	if tp.transport.MaxIdleConnsPerHost != 250 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 250", tp.transport.MaxIdleConnsPerHost)
	}
	if tp.transport.MaxIdleConns != 500 {
		t.Errorf("MaxIdleConns = %d, want 500 (250 * 2 targets)", tp.transport.MaxIdleConns)
	}
}
