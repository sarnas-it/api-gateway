package plugin

import (
	"fmt"
	"sync"

	"github.com/libdns/libdns"
)

// DNSProvider is the xgate extension point for ACME DNS-01. Implementations
// normally wrap a libdns provider; this is the compatibility bridge to the
// Caddy DNS ecosystem without importing Caddy modules.
type DNSProvider interface {
	Module
	// New builds a libdns record manager from provider-specific config.
	New(config map[string]string) (libdns.RecordAppender, libdns.RecordDeleter, error)
}

var (
	dnsMu        sync.RWMutex
	dnsProviders = map[string]DNSProvider{}
)

func RegisterDNS(p DNSProvider) {
	if p == nil || p.Name() == "" {
		return
	}
	dnsMu.Lock()
	defer dnsMu.Unlock()
	if _, exists := dnsProviders[p.Name()]; exists {
		panic(fmt.Sprintf("xgate: duplicate DNS provider %q", p.Name()))
	}
	dnsProviders[p.Name()] = p
}

func DNSProviderByName(name string) (DNSProvider, bool) {
	dnsMu.RLock()
	defer dnsMu.RUnlock()
	p, ok := dnsProviders[name]
	return p, ok
}

func DNSProviderNames() []string {
	dnsMu.RLock()
	defer dnsMu.RUnlock()
	out := make([]string, 0, len(dnsProviders))
	for name := range dnsProviders {
		out = append(out, name)
	}
	return out
}
