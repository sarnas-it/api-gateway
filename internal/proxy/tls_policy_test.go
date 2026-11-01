package proxy

import (
	"crypto/tls"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"code.sarnas.ru/api-gateway/internal/config"
)

func TestApplyTLSPolicy(t *testing.T) {
	cfg := &tls.Config{}
	err := applyTLSPolicy(cfg, &config.TLSConfig{
		MinVersion:   "1.3",
		CipherSuites: []string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"},
		Curves:       []string{"X25519", "P-256"},
		ClientAuth:   "request",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %x", cfg.MinVersion)
	}
	if len(cfg.CipherSuites) != 1 {
		t.Fatalf("CipherSuites = %v", cfg.CipherSuites)
	}
	if len(cfg.CurvePreferences) != 2 {
		t.Fatalf("Curves = %v", cfg.CurvePreferences)
	}
	if cfg.ClientAuth != tls.RequestClientCert {
		t.Fatalf("ClientAuth = %v", cfg.ClientAuth)
	}
}

func TestBuildUpstreamTLSWithCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	caPath := filepath.Join(t.TempDir(), "ca.pem")
	f, err := os.Create(caPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	upstreamTLS, err := buildUpstreamTLS(&config.UpstreamTLSConfig{CAFile: caPath})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: upstreamTLS}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestBuildUpstreamTLSInsecureSkipVerify(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	upstreamTLS, err := buildUpstreamTLS(&config.UpstreamTLSConfig{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: upstreamTLS}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestBuildUpstreamTLSBadCA(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildUpstreamTLS(&config.UpstreamTLSConfig{CAFile: path}); err == nil {
		t.Fatal("expected error for a CA file without certificates")
	}
}
