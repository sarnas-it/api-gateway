package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"code.sarnas.ru/api-gateway/internal/config"
)

func TestAdminAuthAndEndpoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded := false
	s := New(config.AdminConfig{Token: "secret"}, path,
		func() error { reloaded = true; return nil },
		func() map[string]any { return map[string]any{"targets": 2} },
		zap.NewNop())
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"targets":2`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/reload", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !reloaded {
		t.Fatalf("reload = %d, called = %v", rec.Code, reloaded)
	}

	req = httptest.NewRequest(http.MethodGet, "/config", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"path"`) {
		t.Fatalf("config = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAdminReloadError(t *testing.T) {
	s := New(config.AdminConfig{}, "/tmp/x", func() error { return errors.New("requires restart") },
		func() map[string]any { return nil }, zap.NewNop())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/reload", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "requires restart") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
