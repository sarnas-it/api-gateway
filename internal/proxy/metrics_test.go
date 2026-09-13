package proxy

import (
	"net/http/httptest"
	"testing"
)

// TestMetricsHandler_MultipleCallsDoNotPanic проверяет, что /metrics можно
// запрашивать многократно: раньше Handler() регистрировал expvar-переменную
// на каждый вызов и паниковал на втором запросе.
func TestMetricsHandler_MultipleCallsDoNotPanic(t *testing.T) {
	m := NewMetrics(true)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		if rec.Code != 200 {
			t.Fatalf("call %d: status %d, want 200", i, rec.Code)
		}
	}
}
