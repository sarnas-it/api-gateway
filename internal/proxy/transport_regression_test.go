package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"code.sarnas.ru/api-gateway/internal/config"
)

// deadlineRecorder — ResponseWriter, который умеет выставлять дедлайны. Нужен,
// чтобы проверить, что per-route таймауты доходят через обёртку responseWriter.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	readDeadline  time.Time
	writeDeadline time.Time
}

func (d *deadlineRecorder) SetReadDeadline(t time.Time) error {
	d.readDeadline = t
	return nil
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.writeDeadline = t
	return nil
}

func TestRouteDeadlinesReachUnderlyingWriter(t *testing.T) {
	f := newRouteProxy(t, config.RoutingRule{
		Host:         "dav.example",
		TargetName:   "dav",
		ReadTimeout:  time.Minute,
		WriteTimeout: time.Minute,
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = f.host
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	f.mp.handler.ServeHTTP(rec, req)

	if rec.readDeadline.IsZero() {
		t.Error("per-route read timeout did not reach the underlying writer")
	}
	if rec.writeDeadline.IsZero() {
		t.Error("per-route write timeout did not reach the underlying writer")
	}
}

func TestProxyWebSocketUpgrade(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("upstream ResponseWriter is not a Hijacker")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			t.Errorf("upstream hijack: %v", err)
			return
		}
		defer conn.Close()

		if _, err := brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"); err != nil {
			return
		}
		if err := brw.Flush(); err != nil {
			return
		}
		line, err := brw.ReadString('\n')
		if err != nil {
			return
		}
		_, _ = brw.WriteString("echo:" + line)
		_ = brw.Flush()
	}))
	t.Cleanup(upstream.Close)

	cfg := &config.Config{
		Targets: []config.TargetConfig{{Name: "ws", URL: upstream.URL}},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{{
			Host:       "ws.example",
			PathPrefix: "/",
			TargetName: "ws",
		}}},
	}
	mp := &MultiProxy{
		targets:     map[string]*TargetProxy{},
		routeByRule: map[*config.RoutingRule]*RouteConfig{},
		logger:      zap.NewNop(),
		metrics:     NewMetrics(false),
	}
	mp.config.Store(cfg)
	tp, err := mp.createTargetProxy(&cfg.Targets[0], false)
	if err != nil {
		t.Fatal(err)
	}
	mp.targets["ws"] = tp
	mp.rebuildRouteConfigs(cfg)
	mp.handler = mp.proxyHandler()

	front := httptest.NewServer(mp.handler)
	t.Cleanup(front.Close)

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: ws.example\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("status line = %q, want 101 Switching Protocols", strings.TrimSpace(status))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	if _, err := fmt.Fprintf(conn, "ping\n"); err != nil {
		t.Fatal(err)
	}
	echo, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if echo != "echo:ping\n" {
		t.Fatalf("echo = %q, want %q", echo, "echo:ping\n")
	}
}

func TestRouteRejectsChunkedOversizedBody(t *testing.T) {
	limit := int64(1024)
	f := newRouteProxy(t, config.RoutingRule{
		Host:               "dav.example",
		TargetName:         "dav",
		MaxRequestBodySize: &limit,
	})

	payload := bytes.Repeat([]byte("x"), 4096)
	req := httptest.NewRequest(http.MethodPut, "/dav/ws/big.bin", bytes.NewReader(payload))
	req.Host = f.host
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	rec := httptest.NewRecorder()
	f.mp.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %q)", rec.Code, rec.Body.String())
	}
	select {
	case n := <-f.bodyLen:
		t.Fatalf("upstream was called with %d bytes, want no call", n)
	default:
	}
}
