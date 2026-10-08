package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/jwtutil"
	"github.com/basili4-1982/api-gateway/internal/permissions"
)

// TestModifyRequest_OurJWTUnchanged locks the byte-for-byte behaviour of the
// our-JWT path when identity is disabled: headers, signature and permissions
// must be produced exactly as before the external-identity refactor.
//
// The id is a large JSON number so that jwt/v5 decodes it to float64 and
// fmt.Sprintf("%v", id) renders scientific notation ("1e+06") — the signature
// and X-User-ID must both use that exact string, not strconv.Itoa.
//
// MultiProxy is built directly (not via NewMultiProxy) because NewMetrics uses
// package-global expvar names and panics on a second registration in the same
// test binary.
func TestModifyRequest_OurJWTUnchanged(t *testing.T) {
	permsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user_id":1000000,"permissions":["cml.read"]}`))
	}))
	defer permsSrv.Close()

	cfg := &config.Config{}
	cfg.Server.Port = 8080
	cfg.JWT.SecretKey = "our-secret"
	cfg.JWT.Algorithm = "HS256"
	cfg.JWT.ClaimMappings = []string{"id"}
	cfg.Headers.SignHeader = "X-User-Signature"
	cfg.Headers.ClaimToHeader = map[string]string{"id": "X-User-ID"}
	cfg.Permissions = config.PermissionsConfig{
		Enabled:    true,
		ServiceURL: permsSrv.URL,
		APIKey:     "php-secret",
		HeaderName: "X-User-Permissions",
		CacheTTL:   time.Minute,
	}
	cfg.Targets = []config.TargetConfig{{Name: "api", URL: "http://127.0.0.1:1", Timeout: time.Second}}
	// Identity absent: cfg.Identity.Enabled is false.

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
	mp.permissionsManager = permissions.NewManager(&cfg.Permissions, zap.NewNop())
	if mp.identity != nil {
		t.Fatal("identity must be nil when identity is disabled")
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":  1000000, // decoded as float64 after JWT round-trip
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	signed, _ := tok.SignedString([]byte("our-secret"))

	req := httptest.NewRequest("GET", "/api/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	rule := &config.RoutingRule{Auth: &config.AuthRule{Required: true}}

	if err := mp.modifyRequest(req, &cfg.Targets[0], rule); err != nil {
		t.Fatalf("modifyRequest error: %v", err)
	}

	// The exact fmt.Sprintf("%v", float64(1000000)) substitution string.
	idStr := fmt.Sprintf("%v", float64(1000000))
	if got := req.Header.Get("X-User-ID"); got != idStr {
		t.Fatalf("X-User-ID: got %q want %q", got, idStr)
	}

	mac := hmac.New(sha256.New, []byte("php-secret"))
	mac.Write([]byte(idStr))
	want := hex.EncodeToString(mac.Sum(nil))
	if got := req.Header.Get("X-User-Signature"); got != want {
		t.Fatalf("X-User-Signature: got %q want %q", got, want)
	}

	// Permissions module still calls SetHeader with the integer id.
	if got := req.Header.Get("X-User-Permissions"); got != "cml.read" {
		t.Fatalf("X-User-Permissions: got %q want %q", got, "cml.read")
	}
}
