package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"code.sarnas.ru/api-gateway/internal/config"
)

// newRouteProxy собирает MultiProxy с одним маршрутом на реальный upstream.
type routeFixture struct {
	mp       *MultiProxy
	upstream *httptest.Server
	host     string
	bodyLen  chan int
	method   chan string
}

func newRouteProxy(t *testing.T, rule config.RoutingRule) *routeFixture {
	t.Helper()
	bodyLen := make(chan int, 4)
	method := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodyLen <- len(b)
		method <- r.Host + "|" + r.Method
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	if rule.PathPrefix == "" {
		rule.PathPrefix = "/"
	}
	cfg := &config.Config{
		Targets: []config.TargetConfig{{Name: "dav", URL: upstream.URL}},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{rule}},
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
	mp.targets["dav"] = tp
	mp.rebuildRouteConfigs(cfg)
	mp.handler = mp.proxyHandler()
	return &routeFixture{mp: mp, upstream: upstream, host: "dav.example", bodyLen: bodyLen, method: method}
}

func (f *routeFixture) do(t *testing.T, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Host = f.host
	rec := httptest.NewRecorder()
	f.mp.handler.ServeHTTP(rec, req)
	return rec
}

// TestRoutePreserveHostAndStreamBody: WebDAV-маршрут должен видеть публичный
// Host (иначе MOVE/COPY с абсолютным Destination ломаются) и получать тело
// целиком, без буферизации и обрезания.
func TestRoutePreserveHostAndStreamBody(t *testing.T) {
	limit := int64(64 << 20)
	noBuffer := false
	f := newRouteProxy(t, config.RoutingRule{
		Host:               "dav.example",
		TargetName:         "dav",
		PreserveHost:       true,
		BufferRequestBody:  &noBuffer,
		MaxRequestBodySize: &limit,
	})

	payload := bytes.Repeat([]byte("x"), 1<<20) // 1 МиБ
	rec := f.do(t, "PUT", "/dav/ws/file.bin", payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", rec.Code, rec.Body.String())
	}
	if n := <-f.bodyLen; n != len(payload) {
		t.Fatalf("upstream body = %d bytes, want %d", n, len(payload))
	}
	if got := <-f.method; got != "dav.example|PUT" {
		t.Fatalf("upstream saw %q, want preserved host and PUT", got)
	}

	// WebDAV-методы не перечислены в methods — маршрут обязан их принимать.
	if rec := f.do(t, "PROPFIND", "/dav/ws/", nil); rec.Code != http.StatusOK {
		t.Fatalf("PROPFIND status = %d", rec.Code)
	}
	if got := <-f.method; got != "dav.example|PROPFIND" {
		t.Fatalf("upstream saw %q", got)
	}
	<-f.bodyLen
}

// TestRouteRejectsOversizedBody: тело больше лимита маршрута не обрезается
// молча, а получает 413.
func TestRouteRejectsOversizedBody(t *testing.T) {
	limit := int64(1024)
	f := newRouteProxy(t, config.RoutingRule{
		Host:               "dav.example",
		TargetName:         "dav",
		MaxRequestBodySize: &limit,
	})
	rec := f.do(t, "PUT", "/dav/ws/big.bin", bytes.Repeat([]byte("x"), 4096))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}
