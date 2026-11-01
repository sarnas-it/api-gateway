package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"code.sarnas.ru/api-gateway/internal/config"
)

func mustCIDRs(t *testing.T, values ...string) []*net.IPNet {
	t.Helper()
	nets, err := config.ParseTrustedProxies(values)
	if err != nil {
		t.Fatal(err)
	}
	return nets
}

func TestGetClientIP_IgnoresXFFWithoutTrustedProxies(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	r.RemoteAddr = "192.168.1.1:12345"

	if ip := getClientIP(r, nil); ip != "192.168.1.1" {
		t.Fatalf("ip = %q, want RemoteAddr when no trusted proxies are configured", ip)
	}
}

func TestGetClientIP_UsesXFFFromTrustedProxy(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	r.RemoteAddr = "10.0.0.5:12345"

	if ip := getClientIP(r, mustCIDRs(t, "10.0.0.0/8")); ip != "203.0.113.1" {
		t.Fatalf("ip = %q, want 203.0.113.1", ip)
	}
}

func TestGetClientIP_WalksTrustedChainFromTheRight(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 192.168.1.1, 10.0.0.5")
	r.RemoteAddr = "10.0.0.5:12345"

	if ip := getClientIP(r, mustCIDRs(t, "10.0.0.0/8", "192.168.0.0/16")); ip != "203.0.113.7" {
		t.Fatalf("ip = %q, want first untrusted address 203.0.113.7", ip)
	}
}

func TestGetClientIP_UntrustedRemoteIgnoresXFF(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	r.RemoteAddr = "198.51.100.5:12345"

	if ip := getClientIP(r, mustCIDRs(t, "10.0.0.0/8")); ip != "198.51.100.5" {
		t.Fatalf("ip = %q, want untrusted RemoteAddr", ip)
	}
}

func TestGetClientIP_ExactIPTrustedProxy(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	r.RemoteAddr = "10.0.0.5:12345"

	if ip := getClientIP(r, mustCIDRs(t, "10.0.0.5")); ip != "203.0.113.1" {
		t.Fatalf("ip = %q, want 203.0.113.1", ip)
	}
}

func TestMetricsEndpointIgnoresSpoofedXFF(t *testing.T) {
	metrics := NewMetrics(false)
	handler := metricsEndpointMiddleware(metrics, []string{"10.0.0.1"}, func(r *http.Request) string {
		return getClientIP(r, nil)
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/metrics", nil)
	r.RemoteAddr = "198.51.100.5:12345"
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; spoofed XFF must not satisfy the metrics allowlist", rec.Code)
	}
}
