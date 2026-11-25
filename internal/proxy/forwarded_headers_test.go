package proxy

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"code.sarnas.ru/api-gateway/internal/config"
	"code.sarnas.ru/api-gateway/internal/jwtutil"
)

// forwardedRequest — то, что увидел бэкенд.
type forwardedRequest struct {
	path string
	raw  string // RequestURI: закодированная форма, как её получит бэкенд
	host string
	xff  string
	xfh  string
	xfp  string
}

// forwardedFixture — MultiProxy с одним маршрутом на живой upstream,
// запоминающий полученные заголовки и путь.
type forwardedFixture struct {
	mp       *MultiProxy
	upstream *httptest.Server
	seen     chan forwardedRequest
	hits     atomic.Int64
}

func newForwardedFixture(t *testing.T, rule config.RoutingRule, mutate func(*config.Config)) *forwardedFixture {
	t.Helper()
	f := &forwardedFixture{seen: make(chan forwardedRequest, 8)}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.seen <- forwardedRequest{
			path: r.URL.Path,
			raw:  r.URL.RequestURI(),
			host: r.Host,
			xff:  r.Header.Get("X-Forwarded-For"),
			xfh:  r.Header.Get("X-Forwarded-Host"),
			xfp:  r.Header.Get("X-Forwarded-Proto"),
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.upstream.Close)

	if rule.PathPrefix == "" && rule.PathRegex == "" {
		rule.PathPrefix = "/"
	}
	cfg := &config.Config{
		Targets: []config.TargetConfig{{Name: "backend", URL: f.upstream.URL}},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{rule}},
		Headers: config.HeadersConfig{
			CORS: &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"*"}},
		},
		JWT: config.JWTConfig{SecretKey: "test-secret", Algorithm: "HS256", ValidateExp: true},
	}
	if mutate != nil {
		mutate(cfg)
	}
	validator, err := jwtutil.NewJWTValidator(
		cfg.JWT.SecretKey, cfg.JWT.Algorithm,
		cfg.JWT.ValidateExp, cfg.JWT.ValidateIss, cfg.JWT.ExpectedIss,
		cfg.JWT.ValidateAud, cfg.JWT.ExpectedAud, cfg.JWT.PublicKeyFile,
	)
	if err != nil {
		t.Fatal(err)
	}
	f.mp = &MultiProxy{
		targets:      map[string]*TargetProxy{},
		routeByRule:  map[*config.RoutingRule]*RouteConfig{},
		jwtValidator: validator,
		logger:       zap.NewNop(),
		metrics:      NewMetrics(false),
	}
	f.mp.config.Store(cfg)
	tp, err := f.mp.createTargetProxy(&cfg.Targets[0], false)
	if err != nil {
		t.Fatal(err)
	}
	f.mp.targets["backend"] = tp
	f.mp.rebuildRouteConfigs(cfg)
	f.mp.handler = f.mp.proxyHandler()
	return f
}

func (f *forwardedFixture) do(t *testing.T, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.mp.handler.ServeHTTP(rec, r)
	return rec
}

func newForwardedRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.Host = "public.example"
	r.RemoteAddr = "198.51.100.5:12345"
	return r
}

// TestForwardedFor_UntrustedPeerDropsClientChain: клиент прислал свою цепочку,
// пир не входит в trusted_proxies — бэкенд должен увидеть только реальный IP
// пира, без порта.
func TestForwardedFor_UntrustedPeerDropsClientChain(t *testing.T) {
	f := newForwardedFixture(t, config.RoutingRule{TargetName: "backend"}, nil)

	r := newForwardedRequest(t, "/api/items")
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if rec := f.do(t, r); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	got := <-f.seen
	if got.xff != "198.51.100.5" {
		t.Fatalf("backend X-Forwarded-For = %q, want only the peer address without port", got.xff)
	}
}

// TestForwardedFor_PeerWithoutPort: даже без клиентского XFF бэкенд не должен
// получать RemoteAddr с портом (регрессия старой ручной подстановки).
func TestForwardedFor_PeerWithoutPort(t *testing.T) {
	f := newForwardedFixture(t, config.RoutingRule{TargetName: "backend"}, nil)

	if rec := f.do(t, newForwardedRequest(t, "/api/items")); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := <-f.seen; got.xff != "198.51.100.5" {
		t.Fatalf("backend X-Forwarded-For = %q, want %q", got.xff, "198.51.100.5")
	}
}

// TestForwardedFor_TrustedPeerKeepsChain: доверенный пир — цепочка клиента
// сохраняется, IP пира дописывается в конец ReverseProxy.
func TestForwardedFor_TrustedPeerKeepsChain(t *testing.T) {
	f := newForwardedFixture(t, config.RoutingRule{TargetName: "backend"}, nil)
	f.mp.trustedProxies = mustCIDRs(t, "10.0.0.0/8")

	r := newForwardedRequest(t, "/api/items")
	r.RemoteAddr = "10.0.0.5:12345"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 10.1.1.1")

	if rec := f.do(t, r); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := <-f.seen; got.xff != "1.2.3.4, 10.1.1.1, 10.0.0.5" {
		t.Fatalf("backend X-Forwarded-For = %q, want client chain plus trusted peer", got.xff)
	}
}

// TestForwardedFor_TrustedPeerWithoutChain: доверенный пир без клиентского
// XFF — бэкенд видит IP пира без порта.
func TestForwardedFor_TrustedPeerWithoutChain(t *testing.T) {
	f := newForwardedFixture(t, config.RoutingRule{TargetName: "backend"}, nil)
	f.mp.trustedProxies = mustCIDRs(t, "10.0.0.0/8")

	r := newForwardedRequest(t, "/api/items")
	r.RemoteAddr = "10.0.0.5:12345"

	if rec := f.do(t, r); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := <-f.seen; got.xff != "10.0.0.5" {
		t.Fatalf("backend X-Forwarded-For = %q, want %q", got.xff, "10.0.0.5")
	}
}

// TestForwardedHostProto_ClientValuesOverwritten: клиентские
// X-Forwarded-Host/Proto не проходят, бэкенд видит публичный Host и
// фактическую схему соединения с шлюзом.
func TestForwardedHostProto_ClientValuesOverwritten(t *testing.T) {
	f := newForwardedFixture(t, config.RoutingRule{TargetName: "backend"}, nil)

	r := newForwardedRequest(t, "/api/items")
	r.Header.Set("X-Forwarded-Host", "evil.example")
	r.Header.Set("X-Forwarded-Proto", "https")

	if rec := f.do(t, r); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	got := <-f.seen
	if got.xfh != "public.example" {
		t.Fatalf("backend X-Forwarded-Host = %q, want public.example", got.xfh)
	}
	if got.xfp != "http" {
		t.Fatalf("backend X-Forwarded-Proto = %q, want http for a plain connection", got.xfp)
	}
}

// TestForwardedProto_TLSConnection: TLS на шлюзе — бэкенд видит https,
// независимо от того, что прислал клиент.
func TestForwardedProto_TLSConnection(t *testing.T) {
	f := newForwardedFixture(t, config.RoutingRule{TargetName: "backend"}, nil)

	r := newForwardedRequest(t, "/api/items")
	r.TLS = &tls.ConnectionState{}
	r.Header.Set("X-Forwarded-Proto", "http")

	if rec := f.do(t, r); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := <-f.seen; got.xfp != "https" {
		t.Fatalf("backend X-Forwarded-Proto = %q, want https", got.xfp)
	}
}

// TestForwardedHeaders_OptionalAuthEarlyReturn: маршрут с необязательной
// аутентификацией возвращается из modifyRequest раньше хвоста — forwarding-
// заголовки всё равно должны быть перезаписаны.
func TestForwardedHeaders_OptionalAuthEarlyReturn(t *testing.T) {
	f := newForwardedFixture(t, config.RoutingRule{TargetName: "backend"}, nil)

	r := newForwardedRequest(t, "/api/items")
	r.Header.Set("Authorization", "Bearer not-a-real-jwt")
	r.Header.Set("X-Forwarded-Host", "evil.example")
	r.Header.Set("X-Forwarded-Proto", "https")

	if rec := f.do(t, r); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got := <-f.seen
	if got.xfh != "public.example" || got.xfp != "http" {
		t.Fatalf("backend saw X-Forwarded-Host=%q Proto=%q, want public.example/http", got.xfh, got.xfp)
	}
}

// TestForwardedHeaders_PreserveHostWebDAV: preserve_host по-прежнему отдаёт
// бэкенду публичный Host, а X-Forwarded-Host не ломается.
func TestForwardedHeaders_PreserveHostWebDAV(t *testing.T) {
	f := newForwardedFixture(t, config.RoutingRule{TargetName: "backend", PreserveHost: true}, nil)

	r := newForwardedRequest(t, "/dav/ws/")
	r.Host = "dav.example"
	r.Header.Set("X-Forwarded-Host", "evil.example")

	if rec := f.do(t, r); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	got := <-f.seen
	if got.host != "dav.example" {
		t.Fatalf("backend Host = %q, want preserved dav.example", got.host)
	}
	if got.xfh != "dav.example" {
		t.Fatalf("backend X-Forwarded-Host = %q, want dav.example", got.xfh)
	}
}

// TestFinishRequestHeaders_Forwarded: путь auth-коннектора использует тот же
// хелпер — клиентские forwarding-заголовки не проходят.
func TestFinishRequestHeaders_Forwarded(t *testing.T) {
	mp := newTestMultiProxy(t, false)
	r := httptest.NewRequest(http.MethodGet, "/api/items", nil)
	r.Host = "public.example"
	r.RemoteAddr = "198.51.100.5:12345"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	r.Header.Set("X-Forwarded-Host", "evil.example")
	r.Header.Set("X-Forwarded-Proto", "https")

	if err := mp.finishRequestHeaders(r, false); err != nil {
		t.Fatal(err)
	}
	if got := r.Header.Get("X-Forwarded-For"); got != "" {
		t.Fatalf("X-Forwarded-For = %q, want it removed for an untrusted peer", got)
	}
	if got := r.Header.Get("X-Forwarded-Host"); got != "public.example" {
		t.Fatalf("X-Forwarded-Host = %q, want public.example", got)
	}
	if got := r.Header.Get("X-Forwarded-Proto"); got != "http" {
		t.Fatalf("X-Forwarded-Proto = %q, want http", got)
	}
}
