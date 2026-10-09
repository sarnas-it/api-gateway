package proxy

import (
	"net/http/httptest"
	"testing"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/jwtutil"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

func newScrubTestProxy(t *testing.T, authRequired bool) *MultiProxy {
	t.Helper()
	cfg := &config.Config{
		JWT: config.JWTConfig{
			SecretKey:   "test-secret",
			Algorithm:   "HS256",
			ValidateExp: true,
			Required:    authRequired,
		},
	}
	jv, err := jwtutil.NewJWTValidator(
		cfg.JWT.SecretKey, cfg.JWT.Algorithm,
		cfg.JWT.ValidateExp, false, "", false, "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	mp := &MultiProxy{jwtValidator: jv, logger: zap.NewNop()}
	mp.config.Store(cfg)
	return mp
}

var scrubTestRule = &config.RoutingRule{PathPrefix: "/api", TargetName: "x"}

// Клиентский identity-заголовок не должен пройти к downstream ни при каких
// обстоятельствах: gateway вычищает его даже без аутентификации.
func TestModifyRequest_ScrubsClientIdentityHeaders(t *testing.T) {
	mp := newScrubTestProxy(t, false)
	r := httptest.NewRequest("GET", "/api/anything", nil)
	r.Header.Set("X-User-ID", "999")
	r.Header.Set("X-User-Email", "evil@evil.test")
	r.Header.Set("X-User-Roles", "admin")
	r.Header.Set("X-User-Signature", "forged")
	r.Header.Set("X-User-Permissions", "forged")
	r.Header.Set("X-Service-ID", "forged")
	r.Header.Set("X-Key-ID", "1")
	r.Header.Set("X-Real-Header", "keep-me")

	if err := mp.modifyRequest(r, nil, scrubTestRule); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, h := range []string{
		"X-User-ID", "X-User-Email", "X-User-Roles", "X-User-Signature",
		"X-User-Permissions", "X-Service-ID", "X-Key-ID",
	} {
		if v := r.Header.Get(h); v != "" {
			t.Fatalf("client-supplied %s leaked to downstream: %q", h, v)
		}
	}
	if got := r.Header.Get("X-Real-Header"); got != "keep-me" {
		t.Fatalf("non-identity header must be preserved, got %q", got)
	}
}

// С валидным токеном клиентский X-User-ID не должен пережить установку
// заголовков из claims: gateway — единственный источник identity.
func TestModifyRequest_ClientIdentityOverriddenByToken(t *testing.T) {
	mp := newScrubTestProxy(t, true)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "7",
		"exp": float64(1893456000), // 2030-01-01
	})
	signed, _ := token.SignedString([]byte("test-secret"))

	r := httptest.NewRequest("GET", "/api/anything", nil)
	r.Header.Set("Authorization", "Bearer "+signed)
	r.Header.Set("X-User-ID", "999")
	r.Header.Set("X-User-Email", "evil@evil.test")

	if err := mp.modifyRequest(r, nil, scrubTestRule); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := r.Header.Get("X-User-ID"); got == "999" {
		t.Fatal("client X-User-ID must not survive authentication")
	}
	if got := r.Header.Get("X-User-Email"); got != "" {
		t.Fatalf("client X-User-Email must be scrubbed, got %q", got)
	}
}
