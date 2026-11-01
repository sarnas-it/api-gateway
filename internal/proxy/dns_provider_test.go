package proxy

import (
	"context"
	"testing"

	"github.com/libdns/libdns"

	"code.sarnas.ru/api-gateway/internal/config"
	"code.sarnas.ru/api-gateway/pkg/plugin"
)

type fakeDNS struct{}

func (fakeDNS) Name() string { return "test-dns-provider" }
func (fakeDNS) New(map[string]string) (libdns.RecordAppender, libdns.RecordDeleter, error) {
	return fakeLibDNS{}, fakeLibDNS{}, nil
}

type fakeLibDNS struct{}

func (fakeLibDNS) AppendRecords(context.Context, string, []libdns.Record) ([]libdns.Record, error) {
	return nil, nil
}
func (fakeLibDNS) DeleteRecords(context.Context, string, []libdns.Record) ([]libdns.Record, error) {
	return nil, nil
}

func TestDNSProviderFromRegistry(t *testing.T) {
	plugin.RegisterDNS(fakeDNS{})
	got, err := dnsProviderFor(&config.TLSConfig{DNSProvider: "test-dns-provider"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(*dnsPair); !ok {
		t.Fatalf("provider type = %T, want *dnsPair", got)
	}

	if _, err := dnsProviderFor(&config.TLSConfig{DNSProvider: "missing-provider"}); err == nil {
		t.Fatal("expected error for unregistered provider")
	}
}
