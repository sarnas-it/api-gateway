package proxy

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestBuildEvent_IPAndUserAgent(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/x", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID, "req-1"))
	r.RemoteAddr = "10.1.2.3:5555"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.1.2.3")
	r.Header.Set("User-Agent", "test-agent/1.0")
	e := (&Publisher{}).buildEvent(r, 200)
	if e.IP != "203.0.113.7" {
		t.Fatalf("IP = %q, want 203.0.113.7", e.IP)
	}
	if e.UserAgent != "test-agent/1.0" {
		t.Fatalf("UserAgent = %q", e.UserAgent)
	}
}

func TestBuildEvent_IPFallbackRemoteAddr(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/x", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID, "req-1"))
	r.RemoteAddr = "10.1.2.3:5555"
	e := (&Publisher{}).buildEvent(r, 200)
	if e.IP != "10.1.2.3" {
		t.Fatalf("IP = %q, want 10.1.2.3", e.IP)
	}
}

func TestBuildEvent_ResponseBody(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/x", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID, "req-1"))
	capture := &responseCapture{}
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyResponseBody, capture))
	capture.set([]byte(`{"data":{"id":42}}`))
	e := (&Publisher{}).buildEvent(r, 201)
	if string(e.ResponseBody) != `{"data":{"id":42}}` {
		t.Fatalf("ResponseBody = %q", e.ResponseBody)
	}
}

func TestResponseCapture_Truncates(t *testing.T) {
	c := &responseCapture{}
	c.set(make([]byte, 70000))
	if len(c.body) != maxAuditBodyBytes {
		t.Fatalf("len = %d, want %d", len(c.body), maxAuditBodyBytes)
	}
}
