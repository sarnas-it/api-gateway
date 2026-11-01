// Package desec is an xgate DNS-01 module backed by libdns/desec.
package desec

import (
	"errors"
	"os"

	"github.com/libdns/desec"
	"github.com/libdns/libdns"

	"code.sarnas.ru/api-gateway/pkg/plugin"
)

type provider struct{}

func (provider) Name() string { return "desec" }

func (provider) New(cfg map[string]string) (libdns.RecordAppender, libdns.RecordDeleter, error) {
	token := cfg["token"]
	if token == "" {
		token = os.Getenv("DESEC_TOKEN")
	}
	if token == "" {
		return nil, nil, errors.New("desec: token (or DESEC_TOKEN) is required")
	}
	p := &desec.Provider{Token: token}
	return p, p, nil
}

func init() { plugin.RegisterDNS(provider{}) }
