// Package route53 is an xgate DNS-01 module backed by libdns/route53.
package route53

import (
	"strconv"

	"github.com/libdns/libdns"
	"github.com/libdns/route53"

	"code.sarnas.ru/api-gateway/pkg/plugin"
)

type provider struct{}

func (provider) Name() string { return "route53" }

func (provider) New(cfg map[string]string) (libdns.RecordAppender, libdns.RecordDeleter, error) {
	p := &route53.Provider{
		Region:          cfg["region"],
		Profile:         cfg["profile"],
		AccessKeyId:     cfg["access_key_id"],
		SecretAccessKey: cfg["secret_access_key"],
		SessionToken:    cfg["session_token"],
	}
	if raw := cfg["max_retries"]; raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			p.MaxRetries = n
		}
	}
	return p, p, nil
}

func init() { plugin.RegisterDNS(provider{}) }
