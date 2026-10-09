package proxy

import (
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// Клиентский identity-заголовок не должен пройти к downstream ни при каких
// обстоятельствах: gateway вычищает его даже без аутентификации.
func TestModifyRequest_ScrubsClientIdentityHeaders(t *testing.T) {
	mp := newTestMultiProxy(t, false)
	r := httptest.NewRequest("GET", "/api/anything", nil)
	r.Header.Set("X-User-ID", "999")
	r.Header.Set("X-User-Email", "evil@evil.test")
	r.Header.Set("X-User-Roles", "admin")
	r.Header.Set("X-User-Signature", "forged")
	r.Header.Set("X-User-Permissions", "forged")
	r.Header.Set("X-Service-ID", "forged")
	r.Header.Set("X-Key-ID", "1")
	r.Header.Set("X-Real-Header", "keep-me")

	if err := mp.modifyRequest(r, nil, noAuthBlockRule); err != nil {
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
	mp := newTestMultiProxy(t, true)
	token := signTestToken(jwt.MapClaims{
		"sub": "7",
		"exp": float64(1893456000), // 2030-01-01
	}, "test-secret")

	r := httptest.NewRequest("GET", "/api/anything", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-User-ID", "999")
	r.Header.Set("X-User-Email", "evil@evil.test")

	if err := mp.modifyRequest(r, nil, noAuthBlockRule); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := r.Header.Get("X-User-ID"); got != "7" {
		t.Fatalf("X-User-ID must come from the token claim, got %q", got)
	}
	if got := r.Header.Get("X-User-Email"); got != "" {
		t.Fatalf("client X-User-Email must be scrubbed, got %q", got)
	}
}

// Identity-заголовки, заданные в конфиге под произвольными именами
// (claim_to_header, permissions.header_name), тоже вычищаются: иначе клиент
// подделал бы их на маршруте без аутентификации.
func TestModifyRequest_ScrubsConfiguredIdentityHeaders(t *testing.T) {
	mp := newTestMultiProxy(t, false)
	// Правки конфига — на копии, чтобы не мутировать общий указатель
	// (иначе тест оставляет за собой изменённые значения).
	base := mp.config.Load()
	cfg := *base
	cfg.Headers = base.Headers
	cfg.Headers.ClaimToHeader = map[string]string{
		"sub":   "X-User-ID",
		"email": "X-Email",
	}
	cfg.Permissions = base.Permissions
	cfg.Permissions.HeaderName = "X-Tenant"
	mp.config.Store(&cfg)

	r := httptest.NewRequest("GET", "/api/anything", nil)
	r.Header.Set("X-Email", "evil@evil.test")
	r.Header.Set("X-Tenant", "forged")
	r.Header.Set("X-Real-Header", "keep-me")

	if err := mp.modifyRequest(r, nil, noAuthBlockRule); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, h := range []string{"X-Email", "X-Tenant"} {
		if v := r.Header.Get(h); v != "" {
			t.Fatalf("configured identity header %s leaked: %q", h, v)
		}
	}
	if got := r.Header.Get("X-Real-Header"); got != "keep-me" {
		t.Fatalf("non-identity header must be preserved, got %q", got)
	}
}
