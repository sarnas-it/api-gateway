package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWrapMiddleware(t *testing.T) {
	mp := &MultiProxy{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	mp.wrapMiddleware(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent || rec.Header().Get("X-Module") != "" {
		t.Fatalf("without middleware: code=%d header=%q", rec.Code, rec.Header().Get("X-Module"))
	}

	mp.SetMiddleware(func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Module", "on")
			h.ServeHTTP(w, r)
		})
	})
	rec = httptest.NewRecorder()
	mp.wrapMiddleware(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent || rec.Header().Get("X-Module") != "on" {
		t.Fatalf("with middleware: code=%d header=%q", rec.Code, rec.Header().Get("X-Module"))
	}
}
