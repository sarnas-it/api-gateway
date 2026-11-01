package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"code.sarnas.ru/api-gateway/internal/config"
)

func h2cClient() *http.Client {
	return &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
}

func TestHTTPHandlerH2C(t *testing.T) {
	mp := &MultiProxy{
		logger: zap.NewNop(),
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, r.Proto)
		}),
	}
	mp.config.Store(&config.Config{Server: config.ServerConfig{H2C: true}})
	srv := httptest.NewServer(mp.httpHandler())
	defer srv.Close()

	resp, err := h2cClient().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("proto = %s, want HTTP/2", resp.Proto)
	}
}

func TestGRPCThroughProxy(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer()
	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(gs, hs)
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	cfg := &config.Config{
		Targets: []config.TargetConfig{{Name: "grpc", URL: "http://" + lis.Addr().String(), Protocol: "h2c"}},
	}
	mp := &MultiProxy{targets: map[string]*TargetProxy{}, logger: zap.NewNop()}
	mp.config.Store(cfg)
	tp, err := mp.createTargetProxy(&cfg.Targets[0], false)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(h2c.NewHandler(tp.reverseProxy, &http2.Server{}))
	defer front.Close()

	conn, err := grpc.NewClient(front.Listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := grpc_health_v1.NewHealthClient(conn)
	resp, err := client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{Service: ""})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v", resp.Status)
	}
}

func TestUpstreamH2C(t *testing.T) {
	seen := make(chan int, 1)
	backend := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.ProtoMajor
		_, _ = io.WriteString(w, "ok")
	}), &http2.Server{}))
	defer backend.Close()

	cfg := &config.Config{
		Targets: []config.TargetConfig{{Name: "grpc", URL: backend.URL, Protocol: "h2c"}},
	}
	mp := &MultiProxy{targets: map[string]*TargetProxy{}, logger: zap.NewNop()}
	mp.config.Store(cfg)
	tp, err := mp.createTargetProxy(&cfg.Targets[0], false)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(tp.reverseProxy)
	defer front.Close()

	resp, err := http.Get(front.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := <-seen; got != 2 {
		t.Fatalf("backend proto major = %d, want 2", got)
	}
}
