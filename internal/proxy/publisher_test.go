package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"code.sarnas.ru/api-gateway/internal/config"
	"go.uber.org/zap"
)

// TestDoWebhook_DeliversDespiteCanceledContext проверяет, что вебхук доставляется
// даже если request-context уже отменён (on_response/async), и что тело события
// реально отправляется.
func TestDoWebhook_DeliversDespiteCanceledContext(t *testing.T) {
	got := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- b
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := &Publisher{log: zap.NewNop()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // эмулируем отменённый контекст запроса

	p.doWebhook(ctx, config.WebhookConfig{WebhookURL: srv.URL}, []byte(`{"k":"v"}`))

	select {
	case b := <-got:
		if string(b) != `{"k":"v"}` {
			t.Fatalf("body = %q, want {\"k\":\"v\"}", string(b))
		}
	default:
		t.Fatal("webhook not delivered with a canceled request context")
	}
}

func TestBuildEventIPAndUserAgent(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/x", nil)
	r = r.WithContext(ctxRequest(httptest.NewRecorder(), "r1", nil, nil, ""))
	r.RemoteAddr = "10.1.2.3:5555"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.1.2.3")
	r.Header.Set("User-Agent", "test-agent/1.0")

	p := &Publisher{trustedProxies: mustCIDRs(t, "10.1.2.3")}
	ev := p.buildEvent(r, 200, config.WebhookConfig{})
	if ev.IP != "203.0.113.7" {
		t.Fatalf("IP = %q, want 203.0.113.7", ev.IP)
	}
	if ev.UserAgent != "test-agent/1.0" {
		t.Fatalf("UserAgent = %q", ev.UserAgent)
	}
}

func TestBuildEventIPFallbackRemoteAddr(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/x", nil)
	r = r.WithContext(ctxRequest(httptest.NewRecorder(), "r1", nil, nil, ""))
	r.RemoteAddr = "10.1.2.3:5555"

	ev := (&Publisher{}).buildEvent(r, 200, config.WebhookConfig{})
	if ev.IP != "10.1.2.3" {
		t.Fatalf("IP = %q, want 10.1.2.3", ev.IP)
	}
}
