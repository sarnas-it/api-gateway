package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/basili4-1982/api-gateway/internal/config"
)

func TestModifyRequest_ExternalTokenMapsToOurUser(t *testing.T) {
	lookupSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/user-by-email" {
			t.Errorf("unexpected lookup path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":42,"email":"partner@example.com","full_name":"P","is_active":true,"roles":["viewer"]}}`))
	}))
	defer lookupSrv.Close()

	cfg := &config.Config{}
	cfg.Server.Port = 8080
	cfg.JWT.SecretKey = "our-secret"
	cfg.JWT.Algorithm = "HS256"
	cfg.Permissions.APIKey = "php-secret"
	cfg.Headers.SignHeader = "X-User-Signature"
	cfg.Targets = []config.TargetConfig{{Name: "api", URL: "http://127.0.0.1:1", Timeout: time.Second}}
	cfg.Identity = config.IdentityConfig{
		Enabled: true,
		UserLookup: config.IdentityUserLookupConfig{
			ServiceURL: lookupSrv.URL, HMACSecret: "internal-secret", CacheTTL: time.Minute,
		},
		Providers: []config.IdentityProviderConfig{{
			Name: "partner", Enabled: true, Algorithm: "HS256",
			SecretKey: "partner-secret", ValidateExp: true, EmailClaim: "email",
		}},
	}

	mp, err := NewMultiProxy(cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"email": "partner@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	signed, _ := tok.SignedString([]byte("partner-secret"))

	req := httptest.NewRequest("GET", "/api/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	rule := &config.RoutingRule{Auth: &config.AuthRule{Required: true}}

	if err := mp.modifyRequest(req, &cfg.Targets[0], rule); err != nil {
		t.Fatalf("modifyRequest error: %v", err)
	}
	if got := req.Header.Get("X-User-ID"); got != "42" {
		t.Fatalf("expected X-User-ID 42, got %q", got)
	}
	if got := req.Header.Get("X-User-Email"); got != "partner@example.com" {
		t.Fatalf("unexpected X-User-Email %q", got)
	}

	mac := hmac.New(sha256.New, []byte("php-secret"))
	mac.Write([]byte("42"))
	want := hex.EncodeToString(mac.Sum(nil))
	if got := req.Header.Get("X-User-Signature"); got != want {
		t.Fatalf("signature mismatch: got %q want %q", got, want)
	}
}

// TestModifyRequest_ExternalTokenRoleGate verifies that the external identity
// path mirrors the our-JWT role semantics: Auth.Roles is any-of.
func TestModifyRequest_ExternalTokenRoleGate(t *testing.T) {
	lookupSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":42,"email":"partner@example.com","is_active":true,"roles":["viewer","editor"]}}`))
	}))
	defer lookupSrv.Close()

	cfg := &config.Config{}
	cfg.Server.Port = 8080
	cfg.JWT.SecretKey = "our-secret"
	cfg.JWT.Algorithm = "HS256"
	cfg.Permissions.APIKey = "php-secret"
	cfg.Headers.SignHeader = "X-User-Signature"
	cfg.Targets = []config.TargetConfig{{Name: "api", URL: "http://127.0.0.1:1", Timeout: time.Second}}
	cfg.Identity = config.IdentityConfig{
		Enabled: true,
		UserLookup: config.IdentityUserLookupConfig{
			ServiceURL: lookupSrv.URL, HMACSecret: "internal-secret", CacheTTL: time.Minute,
		},
		Providers: []config.IdentityProviderConfig{{
			Name: "partner", Enabled: true, Algorithm: "HS256",
			SecretKey: "partner-secret", ValidateExp: true, EmailClaim: "email",
		}},
	}

	mp, err := NewMultiProxy(cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"email": "partner@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	signed, _ := tok.SignedString([]byte("partner-secret"))

	cases := []struct {
		name    string
		auth    *config.AuthRule
		wantErr bool
	}{
		{"any-of one of several allowed", &config.AuthRule{Required: true, Roles: []string{"editor", "admin"}}, false},
		{"any-of none rejected", &config.AuthRule{Required: true, Roles: []string{"admin"}}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/anything", nil)
			req.Header.Set("Authorization", "Bearer "+signed)
			err := mp.modifyRequest(req, &cfg.Targets[0], &config.RoutingRule{Auth: tc.auth})
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
