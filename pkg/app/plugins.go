package app

import (
	"fmt"

	"code.sarnas.ru/api-gateway/internal/config"
	"code.sarnas.ru/api-gateway/internal/proxy"
	"code.sarnas.ru/api-gateway/pkg/plugin"
)

// authConnectors собирает скомпилированные auth-коннекторы и проверяет, что
// каждый `auth.connector` из маршрутов действительно вкомпилирован: иначе
// маршрут молча отвечал бы 401 из-за опечатки в имени.
func authConnectors(cfg *config.Config) (map[string]proxy.AuthConnector, error) {
	compiled := plugin.AuthConnectors()
	for _, rule := range cfg.Routing.Rules {
		if rule.Auth == nil || rule.Auth.Connector == "" {
			continue
		}
		if _, ok := compiled[rule.Auth.Connector]; !ok {
			return nil, fmt.Errorf("routing rule %q: auth.connector %q is not compiled",
				rule.PathPrefix, rule.Auth.Connector)
		}
	}
	out := make(map[string]proxy.AuthConnector, len(compiled))
	for name, c := range compiled {
		out[name] = c
	}
	return out, nil
}
