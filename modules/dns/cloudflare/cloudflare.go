// Package cloudflare is an xgate DNS-01 module backed by libdns/cloudflare.
package cloudflare

import (
	"errors"
	"os"

	cf "github.com/libdns/cloudflare"
	"github.com/libdns/libdns"

	"code.sarnas.ru/api-gateway/pkg/plugin"
)

type provider struct{}

func (provider) Name() string { return "cloudflare" }

func (provider) New(cfg map[string]string) (libdns.RecordAppender, libdns.RecordDeleter, error) {
	token := cfg["api_token"]
	if token == "" {
		token = os.Getenv("CF_API_TOKEN")
	}
	if token == "" {
		return nil, nil, errors.New("cloudflare: api_token (or CF_API_TOKEN) is required")
	}
	p := &cf.Provider{
		APIToken:  token,
		ZoneToken: cfg["zone_token"],
	}
	return p, p, nil
}

func init() { plugin.RegisterDNS(provider{}) }
