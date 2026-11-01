// Package example is a template for an xgate HTTP middleware module.
package example

import (
	"net/http"

	"code.sarnas.ru/api-gateway/pkg/plugin"
)

type module struct {
	header string
}

func (*module) Name() string { return "example" }

// Configure reads plugins.example.* from the gateway config.
func (m *module) Configure(cfg map[string]string) error {
	m.header = cfg["header"]
	if m.header == "" {
		m.header = "X-Xgate-Example"
	}
	return nil
}

// Wrap adds a marker header. Replace this with the real middleware.
func (m *module) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(m.header, "1")
		next.ServeHTTP(w, r)
	})
}

func init() { plugin.Register(&module{}) }
