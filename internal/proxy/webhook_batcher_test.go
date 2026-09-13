package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"go.uber.org/zap"
)

func batchSink(t *testing.T) (*httptest.Server, chan batchPayload) {
	t.Helper()
	got := make(chan batchPayload, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p batchPayload
		if err := json.Unmarshal(b, &p); err != nil {
			t.Errorf("bad batch body: %v", err)
		}
		got <- p
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestWebhookBatcher_FlushesOnSize(t *testing.T) {
	srv, got := batchSink(t)
	wh := config.WebhookConfig{
		Name:          "audit",
		Transport:     config.TransportWebhook,
		WebhookURL:    srv.URL,
		BatchSize:     3,
		FlushInterval: time.Hour, // чтобы флаш случился именно по размеру
	}
	b := newWebhookBatcher(wh, srv.Client(), zap.NewNop())
	defer b.close()

	for i := 0; i < 3; i++ {
		b.enqueue(AuditEvent{Method: "GET", Path: "/x"})
	}

	select {
	case p := <-got:
		if p.Count != 3 || len(p.Events) != 3 {
			t.Fatalf("count=%d events=%d, want 3/3", p.Count, len(p.Events))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("batch not flushed on size")
	}
}

func TestWebhookBatcher_FlushesOnInterval(t *testing.T) {
	srv, got := batchSink(t)
	wh := config.WebhookConfig{
		Name:          "audit",
		Transport:     config.TransportWebhook,
		WebhookURL:    srv.URL,
		BatchSize:     100,
		FlushInterval: 50 * time.Millisecond,
	}
	b := newWebhookBatcher(wh, srv.Client(), zap.NewNop())
	defer b.close()

	b.enqueue(AuditEvent{Method: "POST", Path: "/y"})

	select {
	case p := <-got:
		if p.Count != 1 || len(p.Events) != 1 || p.Events[0].Method != "POST" {
			t.Fatalf("unexpected batch: %+v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("batch not flushed on interval")
	}
}

func TestWebhookBatcher_FlushRemainingOnClose(t *testing.T) {
	srv, got := batchSink(t)
	wh := config.WebhookConfig{
		Name:          "audit",
		Transport:     config.TransportWebhook,
		WebhookURL:    srv.URL,
		BatchSize:     100,
		FlushInterval: time.Hour,
	}
	b := newWebhookBatcher(wh, srv.Client(), zap.NewNop())
	b.enqueue(AuditEvent{Method: "PUT", Path: "/z"})
	b.close() // должен слить остаток

	select {
	case p := <-got:
		if p.Count != 1 {
			t.Fatalf("count=%d, want 1", p.Count)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remaining batch not flushed on close")
	}
}

// TestWebhookBatcher_BlocksWhenFull проверяет backpressure: при переполнении
// очереди постановка события блокируется (потерь быть не должно).
func TestWebhookBatcher_BlocksWhenFull(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := config.WebhookConfig{
		Name:          "audit",
		Transport:     config.TransportWebhook,
		WebhookURL:    srv.URL,
		BatchSize:     1,
		FlushInterval: time.Hour,
	}
	b := newWebhookBatcherWithQueue(wh, srv.Client(), zap.NewNop(), 1)
	var once sync.Once
	releaseSink := func() { once.Do(func() { close(release) }) }
	defer func() { releaseSink(); b.close() }()

	b.enqueue(AuditEvent{Path: "/1"}) // воркер заберёт и зависнет на sink
	time.Sleep(100 * time.Millisecond)
	b.enqueue(AuditEvent{Path: "/2"}) // заполняет канал (cap=1)

	done := make(chan struct{})
	go func() { b.enqueue(AuditEvent{Path: "/3"}); close(done) }()
	select {
	case <-done:
		t.Fatal("enqueue must block when the queue is full")
	case <-time.After(200 * time.Millisecond):
	}

	releaseSink() // разблокируем sink — очередь должна рассосаться
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue did not unblock after delivery caught up")
	}
}
