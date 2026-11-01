package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/libdns"
	"go.uber.org/zap"

	"code.sarnas.ru/api-gateway/internal/config"
)

func TestHostMatchesWildcard(t *testing.T) {
	patterns := []string{"*.chatcom.online"}
	tests := []struct {
		host string
		want bool
	}{
		{"chatcom.online", false},
		{"test.chatcom.online", true},
		{"a.b.chatcom.online", false},
		{"evilchatcom.online", false},
		{"TEST.CHATCOM.ONLINE.", true},
		{"chatcom.online.evil", false},
	}
	for _, tc := range tests {
		if got := hostMatchesWildcard(tc.host, patterns); got != tc.want {
			t.Errorf("hostMatchesWildcard(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestCombineTLSConfigSelectsWildcardBySNI(t *testing.T) {
	cert := func(name string) *tls.Certificate {
		return &tls.Certificate{Certificate: [][]byte{[]byte(name)}}
	}
	autoCfg := &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return cert("auto"), nil
		},
	}
	wildcardCfg := &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return cert("wildcard"), nil
		},
		NextProtos: []string{"h2", "http/1.1"},
	}
	combined := combineTLSConfig(autoCfg, wildcardCfg, []string{"*.example.com"})

	got, _ := combined.GetCertificate(&tls.ClientHelloInfo{ServerName: "a.example.com"})
	if string(got.Certificate[0]) != "wildcard" {
		t.Fatalf("subdomain cert = %q, want wildcard", got.Certificate[0])
	}
	got, _ = combined.GetCertificate(&tls.ClientHelloInfo{ServerName: "example.com"})
	if string(got.Certificate[0]) != "auto" {
		t.Fatalf("apex cert = %q, want auto", got.Certificate[0])
	}
	if !containsString(combined.NextProtos, "http/1.1") {
		t.Fatalf("NextProtos = %v, want http/1.1", combined.NextProtos)
	}
}

func TestNewWildcardCertmagicInitializesIssuer(t *testing.T) {
	cfg := &config.TLSConfig{
		Domains:     []string{"*.example.com"},
		Email:       "admin@example.com",
		DNSUsername: "user",
		DNSPassword: "pass",
		CacheDir:    t.TempDir(),
		DNSProvider: "regru",
	}
	magic, stop, wildcards, err := newWildcardCertmagic(cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if stop != nil {
		t.Cleanup(stop)
	}
	if len(wildcards) != 1 || wildcards[0] != "*.example.com" {
		t.Fatalf("wildcards = %v", wildcards)
	}
	if len(magic.Issuers) != 1 {
		t.Fatalf("issuers = %d, want 1", len(magic.Issuers))
	}

	// Литерал ACMEIssuer без NewACMEIssuer паникует здесь на неинициализированном
	// mutex; с правильной инициализацией должен вернуться только error/timeout.
	issuer, ok := magic.Issuers[0].(*certmagic.ACMEIssuer)
	if !ok {
		t.Fatalf("issuer type = %T, want *certmagic.ACMEIssuer", magic.Issuers[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_ = issuer.PreCheck(ctx, wildcards, true)
}

func TestRegruDNSAppendAndDelete(t *testing.T) {
	var calls []struct {
		path  string
		input map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		var input map[string]any
		_ = json.Unmarshal([]byte(r.Form.Get("input_data")), &input)
		calls = append(calls, struct {
			path  string
			input map[string]any
		}{r.URL.Path, input})
		_ = json.NewEncoder(w).Encode(map[string]string{"result": "success"})
	}))
	defer srv.Close()

	p := newRegruDNS("user", "pass")
	p.baseURL = srv.URL
	recs := []libdns.Record{libdns.TXT{Name: "_acme-challenge", Text: "token"}}

	if _, err := p.AppendRecords(context.Background(), "chatcom.online", recs); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DeleteRecords(context.Background(), "chatcom.online", recs); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[0].path != "/zone/add_txt" {
		t.Fatalf("append path = %q", calls[0].path)
	}
	if calls[0].input["subdomain"] != "_acme-challenge" || calls[0].input["text"] != "token" {
		t.Fatalf("append input = %#v", calls[0].input)
	}
	if calls[1].path != "/zone/remove_record" {
		t.Fatalf("delete path = %q", calls[1].path)
	}
	if calls[1].input["record_type"] != "TXT" || calls[1].input["content"] != "token" {
		t.Fatalf("delete input = %#v", calls[1].input)
	}
}

func TestRegruDNSReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"result":     "error",
			"error_code": "ACCESS_DENIED_FROM_IP",
			"error_text": "Access to API from this IP denied",
		})
	}))
	defer srv.Close()

	p := newRegruDNS("user", "pass")
	p.baseURL = srv.URL
	_, err := p.AppendRecords(context.Background(), "chatcom.online", []libdns.Record{libdns.TXT{Name: "_acme-challenge", Text: "token"}})
	if err == nil {
		t.Fatal("expected API error")
	}
}
