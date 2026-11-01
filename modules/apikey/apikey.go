// Package apikey is an xgate auth connector that authenticates requests with
// static API keys from the gateway config.
//
// Маршрут выбирает его через auth.connector: apikey. Секреты задаются в
// plugins.apikey.keys в формате
//
//	"<id>:<secret>[:<role1>|<role2>],<id2>:<secret2>"
//
// Сравнение — constant-time по всем ключам, без раннего выхода, чтобы по
// времени ответа нельзя было перебирать секреты.
package apikey

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"code.sarnas.ru/api-gateway/pkg/plugin"
)

// key — разобранная запись из plugins.apikey.keys.
type key struct {
	id     string
	secret string
	roles  []string
}

type module struct {
	header string
	keys   []key
}

func (*module) Name() string { return "apikey" }

// Configure читает plugins.apikey.*.
func (m *module) Configure(cfg map[string]string) error {
	m.header = cfg["header"]
	if m.header == "" {
		m.header = "X-API-Key"
	}
	raw := strings.TrimSpace(cfg["keys"])
	if raw == "" {
		return errors.New("keys is required (\"id:secret[:role|role]\", comma-separated)")
	}
	keys, err := parseKeys(raw)
	if err != nil {
		return err
	}
	m.keys = keys
	return nil
}

func parseKeys(raw string) ([]key, error) {
	parts := strings.Split(raw, ",")
	keys := make([]key, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.SplitN(part, ":", 3)
		if len(fields) < 2 {
			return nil, fmt.Errorf("keys[%d]: expected \"id:secret[:roles]\", got %q", i, part)
		}
		id, secret := strings.TrimSpace(fields[0]), fields[1]
		if id == "" || secret == "" {
			return nil, fmt.Errorf("keys[%d]: id and secret are required", i)
		}
		if seen[secret] {
			return nil, fmt.Errorf("keys[%d]: duplicate secret for %q", i, id)
		}
		seen[secret] = true
		k := key{id: id, secret: secret}
		if len(fields) == 3 {
			for _, role := range strings.Split(fields[2], "|") {
				if role = strings.TrimSpace(role); role != "" {
					k.roles = append(k.roles, role)
				}
			}
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, errors.New("no keys configured")
	}
	return keys, nil
}

// Authenticate сравнивает ключ из запроса со всеми сконфигурированными.
// Несовпадение — (nil, nil): gateway ответит 401.
func (m *module) Authenticate(r *http.Request) (map[string]any, error) {
	presented := r.Header.Get(m.header)
	if presented == "" {
		return nil, nil
	}
	presentedBytes := []byte(presented)
	var match *key
	for i := range m.keys {
		if subtle.ConstantTimeCompare(presentedBytes, []byte(m.keys[i].secret)) == 1 {
			match = &m.keys[i]
		}
	}
	if match == nil {
		return nil, nil
	}
	claims := map[string]any{"id": match.id}
	if len(match.roles) > 0 {
		claims["roles"] = match.roles
	}
	return claims, nil
}

func init() { plugin.Register(&module{}) }
