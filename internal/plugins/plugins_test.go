package plugins

import (
	"testing"

	"github.com/sarnas-it/pluginrpc"
)

func TestParseTransport(t *testing.T) {
	cases := map[string]pluginrpc.Transport{
		"so": pluginrpc.TransportSO, "shared": pluginrpc.TransportShared,
		"fast": pluginrpc.TransportFast, "grpc": pluginrpc.TransportGRPC,
		"": pluginrpc.TransportGRPC,
	}
	for in, want := range cases {
		got, err := parseTransport(in)
		if err != nil {
			t.Fatalf("parseTransport(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("parseTransport(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := parseTransport("bogus"); err == nil {
		t.Fatal("expected error for unknown transport")
	}
}
