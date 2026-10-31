package proxy

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"code.sarnas.ru/api-gateway/internal/config"
	"go.uber.org/zap"
)

// streamingFirstEvent поднимает upstream, который отдаёт одно событие, делает
// flush и держит соединение до release, и проверяет, что клиент шлюза получает
// первое событие, не дожидаясь конца ответа.
func streamingFirstEvent(t *testing.T, contentType string) {
	t.Helper()
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: 1\n\n"))
		w.(http.Flusher).Flush()
		<-release
		_, _ = w.Write([]byte("data: 2\n\n"))
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	mp := &MultiProxy{logger: zap.NewNop()}
	mp.config.Store(&config.Config{})
	tp, err := mp.createTargetProxy(&config.TargetConfig{Name: "t", URL: upstream.URL}, false)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(tp.reverseProxy)
	t.Cleanup(front.Close)

	resp, err := http.Get(front.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	first := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if line != "data: 1\n" {
			t.Fatalf("first line = %q, want %q", line, "data: 1\n")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first streamed event did not reach the client before the upstream finished")
	}
}

func TestReverseProxy_StreamsEventStream(t *testing.T) {
	streamingFirstEvent(t, "text/event-stream")
}

func TestReverseProxy_StreamsChunkedWithoutContentLength(t *testing.T) {
	streamingFirstEvent(t, "")
}
