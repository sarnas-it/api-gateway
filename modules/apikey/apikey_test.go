package apikey

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func configured(t *testing.T, cfg map[string]string) *module {
	t.Helper()
	m := &module{}
	if err := m.Configure(cfg); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return m
}

func TestConfigureParsesKeys(t *testing.T) {
	m := configured(t, map[string]string{
		"keys": "svc-a:s3cret:admin|ops, svc-b:k2",
	})
	if len(m.keys) != 2 {
		t.Fatalf("keys = %+v", m.keys)
	}
	if m.keys[0].id != "svc-a" || m.keys[0].secret != "s3cret" ||
		len(m.keys[0].roles) != 2 || m.keys[0].roles[1] != "ops" {
		t.Fatalf("keys[0] = %+v", m.keys[0])
	}
	if m.keys[1].id != "svc-b" || len(m.keys[1].roles) != 0 {
		t.Fatalf("keys[1] = %+v", m.keys[1])
	}
}

func TestConfigureRejectsBadInput(t *testing.T) {
	cases := map[string]map[string]string{
		"empty":        {},
		"no secret":    {"keys": "svc-a"},
		"empty secret": {"keys": "svc-a:"},
		"empty id":     {"keys": ":secret"},
		"duplicate":    {"keys": "a:same,b:same"},
		"blank only":   {"keys": " , "},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			m := &module{}
			if err := m.Configure(cfg); err == nil {
				t.Fatalf("expected error for %v", cfg)
			}
		})
	}
}

func TestAuthenticate(t *testing.T) {
	m := configured(t, map[string]string{
		"keys":   "svc-a:s3cret:admin|ops,svc-b:k2",
		"header": "X-Tenant-Key",
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Tenant-Key", "s3cret")
	claims, err := m.Authenticate(req)
	if err != nil {
		t.Fatal(err)
	}
	if claims["id"] != "svc-a" {
		t.Fatalf("claims = %v", claims)
	}
	roles, ok := claims["roles"].([]string)
	if !ok || len(roles) != 2 || roles[0] != "admin" {
		t.Fatalf("roles = %v", claims["roles"])
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Tenant-Key", "wrong")
	claims, err = m.Authenticate(req)
	if err != nil || claims != nil {
		t.Fatalf("wrong key: claims=%v err=%v", claims, err)
	}

	claims, err = m.Authenticate(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil || claims != nil {
		t.Fatalf("missing key: claims=%v err=%v", claims, err)
	}
}

func TestAuthenticateDefaultHeader(t *testing.T) {
	m := configured(t, map[string]string{"keys": "svc:k"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "k")
	claims, err := m.Authenticate(req)
	if err != nil || claims == nil || claims["id"] != "svc" {
		t.Fatalf("claims=%v err=%v", claims, err)
	}
}
