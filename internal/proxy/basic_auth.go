package proxy

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"code.sarnas.ru/api-gateway/internal/config"
)

func basicAuthMiddleware(cfg config.BasicAuthConfig) Middleware {
	expectedUserHash := sha256Hex(cfg.Username)
	expectedPassHash := sha256Hex(cfg.Password)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, skipPath := range cfg.SkipPaths {
				if r.URL.Path == skipPath || strings.HasPrefix(r.URL.Path, skipPath+"/") {
					next.ServeHTTP(w, r)
					return
				}
			}

			u, p, ok := r.BasicAuth()
			if !ok {
				w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			uHash := sha256Hex(u)
			pHash := sha256Hex(p)

			if subtle.ConstantTimeCompare([]byte(uHash), []byte(expectedUserHash)) != 1 ||
				subtle.ConstantTimeCompare([]byte(pHash), []byte(expectedPassHash)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			r.Header.Del("Authorization")
			next.ServeHTTP(w, r)
		})
	}
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
