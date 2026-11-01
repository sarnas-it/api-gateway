// Package digitalocean is an xgate DNS-01 module backed by libdns/digitalocean.
package digitalocean

import (
	"errors"
	"os"

	"github.com/libdns/digitalocean"
	"github.com/libdns/libdns"

	"code.sarnas.ru/api-gateway/pkg/plugin"
)

type provider struct{}

func (provider) Name() string { return "digitalocean" }

func (provider) New(cfg map[string]string) (libdns.RecordAppender, libdns.RecordDeleter, error) {
	token := cfg["api_token"]
	if token == "" {
		token = os.Getenv("DO_API_TOKEN")
	}
	if token == "" {
		return nil, nil, errors.New("digitalocean: api_token (or DO_API_TOKEN) is required")
	}
	p := &digitalocean.Provider{APIToken: token}
	return p, p, nil
}

func init() { plugin.RegisterDNS(provider{}) }
