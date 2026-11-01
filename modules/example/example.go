// Package example is a template for an xgate HTTP middleware module.
package example

import (
	"net/http"

	"code.sarnas.ru/api-gateway/pkg/plugin"
)

type module struct{}

func (module) Name() string { return "example" }

// Wrap adds a marker header. Replace this with the real middleware.
func (module) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Xgate-Example", "1")
		next.ServeHTTP(w, r)
	})
}

func init() { plugin.Register(module{}) }
