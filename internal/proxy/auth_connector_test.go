package proxy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"code.sarnas.ru/api-gateway/internal/config"
)

type fakeConnector struct {
	claims map[string]any
	err    error
	seen   *http.Request
}

func (f *fakeConnector) Authenticate(r *http.Request) (map[string]any, error) {
	f.seen = r
	return f.claims, f.err
}

func connectorProxy(t *testing.T, connector *fakeConnector) (*MultiProxy, *config.Config) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Server.Port = 8080
	cfg.JWT.SecretKey = "our-secret"
	cfg.JWT.Algorithm = "HS256"
	cfg.Headers.ClaimToHeader = map[string]string{"id": "X-User-ID", "roles": "X-User-Roles"}
	cfg.JWT.ClaimMappings = []string{"id", "email", "roles"}
	cfg.Targets = []config.TargetConfig{{Name: "api", URL: "http://127.0.0.1:1"}}
	mp, err := NewMultiProxy(cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	mp.SetAuthConnectors(map[string]AuthConnector{"fake": connector})
	return mp, cfg
}

func connectorRule(roles ...string) *config.RoutingRule {
	return &config.RoutingRule{PathPrefix: "/api", Auth: &config.AuthRule{Connector: "fake", Roles: roles}}
}

func TestConnectorAuthenticatesWithoutAuthorization(t *testing.T) {
	// Глобально JWT обязателен: маршрут с коннектором не должен наследовать
	// это требование, иначе API-ключ никогда бы не сработал.
	conn := &fakeConnector{claims: map[string]any{"id": "svc-a", "roles": []string{"admin"}}}
	mp, cfg := connectorProxy(t, conn)
	cfg.JWT.Required = true

	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("X-User-ID", "forged")
	if err := mp.modifyRequest(req, &cfg.Targets[0], connectorRule()); err != nil {
		t.Fatalf("modifyRequest: %v", err)
	}
	if conn.seen == nil {
		t.Fatal("connector was not called")
	}
	if got := req.Header.Get("X-User-ID"); got != "svc-a" {
		t.Fatalf("X-User-ID = %q, want svc-a (client value must be scrubbed)", got)
	}
	if got := req.Header.Get("X-User-Roles"); got != "[admin]" {
		t.Fatalf("X-User-Roles = %q", got)
	}
}

func TestConnectorFailClosed(t *testing.T) {
	cases := map[string]*fakeConnector{
		"no identity": {claims: nil},
		"error":       {err: errors.New("backend down")},
	}
	for name, conn := range cases {
		t.Run(name, func(t *testing.T) {
			mp, cfg := connectorProxy(t, conn)
			err := mp.modifyRequest(httptest.NewRequest(http.MethodGet, "/api/x", nil),
				&cfg.Targets[0], connectorRule())
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestConnectorUnknownIsError(t *testing.T) {
	conn := &fakeConnector{claims: map[string]any{"id": "svc-a"}}
	mp, cfg := connectorProxy(t, conn)
	rule := &config.RoutingRule{Auth: &config.AuthRule{Connector: "missing"}}
	if err := mp.modifyRequest(httptest.NewRequest(http.MethodGet, "/api/x", nil),
		&cfg.Targets[0], rule); err == nil {
		t.Fatal("expected error for unknown connector")
	}
}

func TestConnectorRoles(t *testing.T) {
	conn := &fakeConnector{claims: map[string]any{"id": "svc-a", "roles": []string{"viewer"}}}
	mp, cfg := connectorProxy(t, conn)

	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	if err := mp.modifyRequest(req, &cfg.Targets[0], connectorRule("admin")); err == nil {
		t.Fatal("expected role error")
	}
	req = httptest.NewRequest(http.MethodGet, "/api/x", nil)
	if err := mp.modifyRequest(req, &cfg.Targets[0], connectorRule("viewer")); err != nil {
		t.Fatalf("modifyRequest: %v", err)
	}
}

func TestConnectorIgnoresPresentedJWT(t *testing.T) {
	// На маршруте с коннектором «свой» JWT не должен подменять identity:
	// иначе коннектор обходился бы валидным токеном шлюза.
	conn := &fakeConnector{claims: nil}
	mp, cfg := connectorProxy(t, conn)
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	if err := mp.modifyRequest(req, &cfg.Targets[0], connectorRule()); err == nil {
		t.Fatal("expected error")
	}
}
