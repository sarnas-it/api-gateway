package plugin

import "net/http"

// HTTPMiddleware is a compile-time middleware module. Wrap is applied around the
// gateway handler, so the module sees requests and can short-circuit responses.
type HTTPMiddleware interface {
	Module
	Wrap(http.Handler) http.Handler
}
