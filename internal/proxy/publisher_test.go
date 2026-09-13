package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basili4-1982/api-gateway/internal/config"
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
