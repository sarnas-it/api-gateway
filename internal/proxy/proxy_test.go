package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
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
			SecretKey:     "test-secret",
			Algorithm:     "HS256",
			ValidateExp:   true,
			Required:      authRequired,
			ClaimMappings: []string{"sub"},
		},
		Headers: config.HeadersConfig{
			ClaimToHeader: map[string]string{"sub": "X-User-ID"},
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

func newBodyLimitProxy(t *testing.T, limit *int64) (*MultiProxy, *TargetProxy, chan int) {
	t.Helper()
	received := make(chan int, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- len(b)
	}))
	t.Cleanup(srv.Close)

	mp := &MultiProxy{logger: zap.NewNop()}
	mp.config.Store(&config.Config{
		Server: config.ServerConfig{MaxRequestBodySize: limit},
	})
	tp, err := mp.createTargetProxy(&config.TargetConfig{Name: "t", URL: srv.URL}, false)
	if err != nil {
		t.Fatal(err)
	}
	return mp, tp, received
}

func TestProxyRequest_AppliesMaxRequestBodySize(t *testing.T) {
	limit := int64(5)
	mp, tp, received := newBodyLimitProxy(t, &limit)

	req := httptest.NewRequest("POST", "/", io.NopCloser(strings.NewReader("0123456789")))
	mp.proxyRequest(httptest.NewRecorder(), req, tp, "/")

	if n := <-received; n != 5 {
		t.Fatalf("upstream read %d bytes, want 5", n)
	}
}

func TestProxyRequest_ZeroMaxRequestBodySizeIsUnlimited(t *testing.T) {
	limit := int64(0)
	mp, tp, received := newBodyLimitProxy(t, &limit)

	req := httptest.NewRequest("POST", "/", io.NopCloser(strings.NewReader("0123456789")))
	mp.proxyRequest(httptest.NewRecorder(), req, tp, "/")

	if n := <-received; n != 10 {
		t.Fatalf("upstream read %d bytes, want 10 (0 = unlimited)", n)
	}
}

func TestReadBodyLimited(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int64
		want  string
	}{
		{"zero is unlimited", 0, "0123456789"},
		{"negative is unlimited", -1, "0123456789"},
		{"positive caps read", 4, "0123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := readBodyLimited(strings.NewReader("0123456789"), tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tc.want {
				t.Errorf("readBodyLimited(limit=%d) = %q, want %q", tc.limit, b, tc.want)
			}
		})
	}
}

// ──────── Weighted balancing ────────

func intPtr(v int) *int { return &v }

func poolTarget(name string, weight int) *TargetProxy {
	return &TargetProxy{
		config:           &config.TargetConfig{Name: name, Weight: intPtr(weight)},
		healthy:          true,
		cbState:          stateClosed,
		failureThreshold: defaultFailureThreshold,
		cbTimeout:        defaultCBTimeout,
	}
}

// poolTargetDefaultWeight строит таргет без явного weight — nil, т.е. дефолт.
func poolTargetDefaultWeight(name string) *TargetProxy {
	return &TargetProxy{
		config:           &config.TargetConfig{Name: name},
		healthy:          true,
		cbState:          stateClosed,
		failureThreshold: defaultFailureThreshold,
		cbTimeout:        defaultCBTimeout,
	}
}

func poolOf(targets ...*TargetProxy) *RouteConfig {
	rc := &RouteConfig{}
	for _, tp := range targets {
		weight := tp.config.EffectiveWeight()
		if weight < 0 {
			weight = 0
		}
		rc.Targets = append(rc.Targets, tp)
		rc.weights = append(rc.weights, weight)
		rc.totalWeight += weight
	}
	return rc
}

func TestPickTarget_WeightedDistribution(t *testing.T) {
	rc := poolOf(poolTarget("a", 3), poolTarget("b", 1))

	counts := map[string]int{}
	const n = 400
	for i := 0; i < n; i++ {
		tp := rc.pickTarget(false)
		if tp == nil {
			t.Fatal("pickTarget returned nil with healthy targets")
		}
		counts[tp.config.Name]++
	}
	if counts["a"] < 280 || counts["a"] > 320 {
		t.Fatalf("weight-3 target selected %d/%d times, want ~300", counts["a"], n)
	}
	if counts["b"] < 80 || counts["b"] > 120 {
		t.Fatalf("weight-1 target selected %d/%d times, want ~100", counts["b"], n)
	}
}

func TestPickTarget_SkipsUnhealthy(t *testing.T) {
	a := poolTarget("a", 3)
	b := poolTarget("b", 1)
	a.healthy = false
	rc := poolOf(a, b)

	for i := 0; i < 50; i++ {
		if tp := rc.pickTarget(false); tp != b {
			t.Fatalf("unhealthy target selected: %v", tp)
		}
	}
}

func TestPickTarget_AllUnhealthyReturnsNil(t *testing.T) {
	a := poolTarget("a", 3)
	b := poolTarget("b", 1)
	a.healthy = false
	b.healthy = false
	rc := poolOf(a, b)

	if tp := rc.pickTarget(false); tp != nil {
		t.Fatalf("expected nil when all targets unhealthy, got %v", tp)
	}
}

func TestPickTarget_ZeroWeightNeverSelected(t *testing.T) {
	rc := poolOf(poolTarget("a", 1), poolTarget("b", 0))
	for i := 0; i < 50; i++ {
		tp := rc.pickTarget(false)
		if tp == nil || tp.config.Name != "a" {
			t.Fatalf("zero-weight target selected: %v", tp)
		}
	}

	zeroOnly := poolOf(poolTarget("z", 0))
	if tp := zeroOnly.pickTarget(false); tp != nil {
		t.Fatalf("pool with only zero weights must return nil, got %v", tp)
	}
}

func TestPickTarget_NilWeightDefaultsToOne(t *testing.T) {
	rc := poolOf(poolTargetDefaultWeight("a"))
	if rc.totalWeight != 1 || rc.weights[0] != 1 {
		t.Fatalf("nil weight must default to 1, got weights=%v total=%d", rc.weights, rc.totalWeight)
	}
	if tp := rc.pickTarget(false); tp == nil || tp.config.Name != "a" {
		t.Fatalf("nil-weight target must be selectable, got %v", tp)
	}
}

func TestPickTarget_HalfOpenProbeSurvivesAnotherWinner(t *testing.T) {
	// b сканируется первым и выигрывает первый раунд; a — half-open с
	// взведённым пробником. До исправления сканирование a потребляло пробник,
	// поэтому после победы b таргет a навсегда исключался из выбора.
	b := poolTarget("b", 1)
	a := poolTarget("a", 1)
	a.mu.Lock()
	a.cbState = stateHalfOpen
	a.halfOpenProbe.Store(true)
	a.mu.Unlock()
	rc := poolOf(b, a)

	sawA := false
	for i := 0; i < 4; i++ {
		tp := rc.pickTarget(true)
		if tp == nil {
			t.Fatalf("pick %d returned nil", i)
		}
		if tp == a {
			sawA = true
			break
		}
	}
	if !sawA {
		t.Fatal("half-open recovered target was stranded after another candidate won selection")
	}
}

func TestReload_PicksUpWeightChanges(t *testing.T) {
	newCfg := func(weightA, weightB int) *config.Config {
		return &config.Config{
			Targets: []config.TargetConfig{
				{Name: "a", URL: "http://a:9000", Timeout: time.Second, Weight: intPtr(weightA)},
				{Name: "b", URL: "http://b:9000", Timeout: time.Second, Weight: intPtr(weightB)},
			},
			Routing: config.RoutingConfig{Rules: []config.RoutingRule{
				{Host: "h", PathPrefix: "/api", TargetName: "a"},
				{Host: "h", PathPrefix: "/api", TargetName: "b"},
			}},
		}
	}

	initial := newCfg(1, 1)
	mp := &MultiProxy{
		targets:     map[string]*TargetProxy{},
		routeByRule: map[*config.RoutingRule]*RouteConfig{},
		logger:      zap.NewNop(),
	}
	mp.config.Store(initial)
	for i := range initial.Targets {
		tp, err := mp.createTargetProxy(&initial.Targets[i], false)
		if err != nil {
			t.Fatal(err)
		}
		mp.targets[initial.Targets[i].Name] = tp
	}
	mp.rebuildRouteConfigs(initial)

	if rc := mp.routeByRule[&initial.Routing.Rules[0]]; rc == nil || rc.totalWeight != 2 {
		t.Fatalf("initial pool totalWeight = %v, want 2", rc)
	}

	updated := newCfg(3, 1)
	if err := mp.Reload(updated); err != nil {
		t.Fatal(err)
	}

	rc := mp.routeByRule[&updated.Routing.Rules[0]]
	if rc == nil {
		t.Fatal("route config missing after reload")
	}
	if len(rc.weights) != 2 || rc.weights[0] != 3 || rc.weights[1] != 1 {
		t.Fatalf("weights after reload = %v, want [3 1]", rc.weights)
	}
	if rc.totalWeight != 4 {
		t.Fatalf("totalWeight after reload = %d, want 4", rc.totalWeight)
	}

	counts := map[string]int{}
	for i := 0; i < 400; i++ {
		tp := rc.pickTarget(false)
		if tp == nil {
			t.Fatal("pickTarget returned nil after reload")
		}
		counts[tp.config.Name]++
	}
	if counts["a"] < 280 || counts["a"] > 320 {
		t.Fatalf("weight-3 target selected %d/400 after reload, want ~300", counts["a"])
	}
}

func TestProxyHandler_BalancesAcrossPool(t *testing.T) {
	var aCount, bCount atomic.Int64
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aCount.Add(1)
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bCount.Add(1)
	}))
	defer srvB.Close()

	cfg := &config.Config{
		Targets: []config.TargetConfig{
			{Name: "a", URL: srvA.URL, Weight: intPtr(3)},
			{Name: "b", URL: srvB.URL, Weight: intPtr(1)},
		},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{
			{Host: "h", PathPrefix: "/api", TargetName: "a"},
			{Host: "h", PathPrefix: "/api", TargetName: "b"},
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

	for i := 0; i < 40; i++ {
		req := httptest.NewRequest("GET", "/api/x", nil)
		req.Host = "h"
		rec := httptest.NewRecorder()
		mp.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, body %s", i, rec.Code, rec.Body.String())
		}
	}
	if got := aCount.Load(); got != 30 {
		t.Fatalf("target a served %d/40, want 30", got)
	}
	if got := bCount.Load(); got != 10 {
		t.Fatalf("target b served %d/40, want 10", got)
	}
}

func TestProxyHandler_AllUnhealthyReturns503(t *testing.T) {
	cfg := &config.Config{
		Targets: []config.TargetConfig{{Name: "a", URL: "http://a:9000", Weight: intPtr(1)}},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{
			{Host: "h", PathPrefix: "/api", TargetName: "a"},
		}},
	}
	mp := &MultiProxy{
		targets:     map[string]*TargetProxy{},
		routeByRule: map[*config.RoutingRule]*RouteConfig{},
		logger:      zap.NewNop(),
		metrics:     NewMetrics(false),
	}
	mp.config.Store(cfg)
	tp, err := mp.createTargetProxy(&cfg.Targets[0], false)
	if err != nil {
		t.Fatal(err)
	}
	tp.healthy = false
	mp.targets["a"] = tp
	mp.rebuildRouteConfigs(cfg)
	mp.handler = mp.proxyHandler()

	req := httptest.NewRequest("GET", "/api/x", nil)
	req.Host = "h"
	rec := httptest.NewRecorder()
	mp.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestCheckRoles(t *testing.T) {
	tests := []struct {
		name  string
		roles interface{}
		omit  bool
		anyOf []string
		allOf []string
		want  bool
	}{
		{name: "any-of single match", roles: []interface{}{"admin", "user"}, anyOf: []string{"admin"}, want: true},
		{name: "any-of second listed matches", roles: []interface{}{"user"}, anyOf: []string{"admin", "user"}, want: true},
		{name: "any-of no match", roles: []interface{}{"user"}, anyOf: []string{"admin"}, want: false},
		{name: "all-of all present", roles: []interface{}{"admin", "mfa"}, allOf: []string{"admin", "mfa"}, want: true},
		{name: "all-of one missing", roles: []interface{}{"admin"}, allOf: []string{"admin", "mfa"}, want: false},
		{name: "combined satisfied", roles: []interface{}{"admin", "mfa"}, anyOf: []string{"admin", "support"}, allOf: []string{"mfa"}, want: true},
		{name: "combined any-of unsatisfied", roles: []interface{}{"mfa"}, anyOf: []string{"admin"}, allOf: []string{"mfa"}, want: false},
		{name: "combined all-of unsatisfied", roles: []interface{}{"admin"}, anyOf: []string{"admin"}, allOf: []string{"mfa"}, want: false},
		{name: "missing claim fails", omit: true, anyOf: []string{"admin"}, want: false},
		{name: "malformed claim number fails", roles: float64(42), anyOf: []string{"admin"}, want: false},
		{name: "malformed claim object fails", roles: map[string]interface{}{"admin": true}, anyOf: []string{"admin"}, want: false},
		{name: "malformed claim array with non-string fails", roles: []interface{}{"admin", float64(7)}, anyOf: []string{"admin"}, want: false},
		{name: "single string claim matches", roles: "admin", anyOf: []string{"admin"}, want: true},
		{name: "single string claim does not match", roles: "user", anyOf: []string{"admin"}, want: false},
		{name: "no roles configured allows", omit: true, want: true},
		{name: "no roles configured ignores malformed claim", roles: float64(42), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := jwt.MapClaims{}
			if !tt.omit {
				claims["roles"] = tt.roles
			}
			mp := &MultiProxy{}
			err := mp.checkRoles(claims, tt.anyOf, tt.allOf)
			if tt.want && err != nil {
				t.Fatalf("expected allowed, got error: %v", err)
			}
			if !tt.want && err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestModifyRequest_RoleChecks(t *testing.T) {
	tests := []struct {
		name  string
		roles interface{}
		omit  bool
		auth  *config.AuthRule
		want  bool
	}{
		{
			name:  "any-of allows matching role",
			roles: []interface{}{"support"},
			auth:  &config.AuthRule{Required: true, Roles: []string{"admin", "support"}},
			want:  true,
		},
		{
			name:  "any-of rejects no matching role",
			roles: []interface{}{"user"},
			auth:  &config.AuthRule{Required: true, Roles: []string{"admin"}},
			want:  false,
		},
		{
			name:  "all-of rejects missing role",
			roles: []interface{}{"admin"},
			auth:  &config.AuthRule{Required: true, RolesAll: []string{"admin", "mfa"}},
			want:  false,
		},
		{
			name: "missing claim rejected on role-protected route",
			omit: true,
			auth: &config.AuthRule{Required: true, Roles: []string{"admin"}},
			want: false,
		},
		{
			name: "no roles configured allows",
			omit: true,
			auth: &config.AuthRule{Required: true},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mp := newTestMultiProxy(t, true)
			claims := jwt.MapClaims{
				"sub": "123",
				"exp": float64(1893456000),
			}
			if !tt.omit {
				claims["roles"] = tt.roles
			}
			token := signTestToken(claims, "test-secret")
			r := httptest.NewRequest("GET", "/api/anything", nil)
			r.Header.Set("Authorization", "Bearer "+token)
			rule := &config.RoutingRule{PathPrefix: "/api", TargetName: "x", Auth: tt.auth}

			err := mp.modifyRequest(r, nil, rule)
			if tt.want && err != nil {
				t.Fatalf("expected allowed, got error: %v", err)
			}
			if !tt.want && err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}
