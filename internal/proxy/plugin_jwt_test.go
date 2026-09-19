package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

func buildJWTPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "jwt")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/plugins/jwt")
	cmd.Dir = "../.." // репозиторий — два уровня выше internal/proxy
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build jwt plugin: %v\n%s", err, out)
	}
	return bin
}

func signToken(t *testing.T) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id": "42", "email": "a@b.c", "roles": []string{"user"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString([]byte("benchsecret"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMultiProxyJWTPlugin(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	bin := buildJWTPlugin(t)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{Enabled: true, JWT: &config.PluginSpec{Path: bin, Transport: "grpc"}},
		JWT:     config.JWTConfig{SecretKey: "benchsecret", Algorithm: "HS256", ValidateExp: true, Required: true},
		Targets: []config.TargetConfig{{Name: "b", URL: backend.URL}},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{{PathPrefix: "/", TargetName: "b"}}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mp, err := NewMultiProxyWithPlugins(ctx, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	defer mp.Stop(context.Background())

	tok := signToken(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	mp.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Authorization", "Bearer garbage")
	rec2 := httptest.NewRecorder()
	mp.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token: got %d, want 401", rec2.Code)
	}
}
