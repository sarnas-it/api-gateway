package proxy

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/quic-go/quic-go/http3"
)

func TestHTTP3Server(t *testing.T) {
	// Берём готовый тестовый сертификат у httptest, чтобы не генерировать свой.
	certSrv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certSrv.Close()
	cert := certSrv.TLS.Certificates[0]

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	h3s := &http3.Server{
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h3"}},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, r.Proto)
		}),
	}
	go func() { _ = h3s.Serve(conn) }()
	defer h3s.Close()

	tr := &http3.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test cert
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}

	port := conn.LocalAddr().(*net.UDPAddr).Port
	resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 3 {
		t.Fatalf("proto = %s, want HTTP/3", resp.Proto)
	}
}
