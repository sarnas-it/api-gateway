package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"code.sarnas.ru/api-gateway/pkg/plugin"
)

type testMiddleware struct{}

func (testMiddleware) Name() string { return "test.middleware" }
func (testMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Middleware", "1")
		next.ServeHTTP(w, r)
	})
}

func TestWrapPluginMiddleware(t *testing.T) {
	h := wrapPluginMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), []plugin.Module{testMiddleware{}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("X-Test-Middleware") != "1" {
		t.Fatal("plugin middleware was not applied")
	}
}
