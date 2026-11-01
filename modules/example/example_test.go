package example

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWrap(t *testing.T) {
	m := &module{}
	if err := m.Configure(map[string]string{"header": "X-Custom-Example"}); err != nil {
		t.Fatal(err)
	}
	h := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent || rec.Header().Get("X-Custom-Example") != "1" {
		t.Fatalf("status=%d header=%q", rec.Code, rec.Header().Get("X-Custom-Example"))
	}
}
