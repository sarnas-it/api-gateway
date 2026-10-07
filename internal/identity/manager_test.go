package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/basili4-1982/api-gateway/internal/config"
)

func signHS256(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func lookupServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("email") {
		case "active@example.com":
			_, _ = w.Write([]byte(`{"data":{"id":42,"email":"active@example.com","full_name":"A","is_active":true,"roles":["viewer"]}}`))
		case "inactive@example.com":
			_, _ = w.Write([]byte(`{"data":{"id":43,"email":"inactive@example.com","is_active":false}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func managerCfg(serviceURL string) config.IdentityConfig {
	return config.IdentityConfig{
		Enabled:   true,
		Selection: []string{"partner"},
		UserLookup: config.IdentityUserLookupConfig{
			ServiceURL: serviceURL, HMACSecret: "internal-secret", CacheTTL: time.Minute,
		},
		Providers: []config.IdentityProviderConfig{{
			Name: "partner", Enabled: true, Algorithm: "HS256",
			SecretKey: "partner-secret", ValidateExp: true, EmailClaim: "email",
		}},
	}
}

func TestResolve_ValidPartnerToken(t *testing.T) {
	srv := lookupServer(t)
	defer srv.Close()

	m, err := NewManager(managerCfg(srv.URL), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	token := signHS256(t, "partner-secret", jwt.MapClaims{
		"email": "active@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	u, err := m.Resolve(context.Background(), token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.ID != 42 || u.Email != "active@example.com" {
		t.Fatalf("unexpected user: %+v", u)
	}
}

func TestResolve_WrongSecret(t *testing.T) {
	srv := lookupServer(t)
	defer srv.Close()
	m, _ := NewManager(managerCfg(srv.URL), zap.NewNop())

	token := signHS256(t, "our-secret", jwt.MapClaims{
		"email": "active@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	if _, err := m.Resolve(context.Background(), token); err == nil {
		t.Fatal("expected error for wrong secret")
	}
}

func TestResolve_InactiveUser(t *testing.T) {
	srv := lookupServer(t)
	defer srv.Close()
	m, _ := NewManager(managerCfg(srv.URL), zap.NewNop())

	token := signHS256(t, "partner-secret", jwt.MapClaims{
		"email": "inactive@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	if _, err := m.Resolve(context.Background(), token); err == nil {
		t.Fatal("expected error for inactive user")
	}
}

func TestResolve_NoEmailClaim(t *testing.T) {
	srv := lookupServer(t)
	defer srv.Close()
	m, _ := NewManager(managerCfg(srv.URL), zap.NewNop())

	token := signHS256(t, "partner-secret", jwt.MapClaims{
		"sub": "123",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := m.Resolve(context.Background(), token); err == nil {
		t.Fatal("expected error when email claim missing")
	}
}

func TestResolve_SelectionExcludesProvider(t *testing.T) {
	srv := lookupServer(t)
	defer srv.Close()
	cfg := managerCfg(srv.URL)
	cfg.Selection = []string{"other"}
	m, _ := NewManager(cfg, zap.NewNop())

	token := signHS256(t, "partner-secret", jwt.MapClaims{
		"email": "active@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	if _, err := m.Resolve(context.Background(), token); err == nil {
		t.Fatal("expected error when provider not selected")
	}
}
