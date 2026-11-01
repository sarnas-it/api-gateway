package example

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWrap(t *testing.T) {
	h := module{}.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent || rec.Header().Get("X-Xgate-Example") != "1" {
		t.Fatalf("status=%d header=%q", rec.Code, rec.Header().Get("X-Xgate-Example"))
	}
}
