package proxy

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"code.sarnas.ru/api-gateway/internal/config"
)

// ErrRestartRequired означает, что конфиг содержит изменения, которые нельзя
// применить на лету: они захвачены при создании листенеров, логгера или
// auth-компонентов. Reload отклоняет такой конфиг целиком, а не применяет
// половину и не рапортует успех.
var ErrRestartRequired = errors.New("configuration change requires restart")

// immutableReloadChanges перечисляет поля, которые Reload не умеет применять.
func immutableReloadChanges(old, next *config.Config) []string {
	var changed []string

	if old.Server.Port != next.Server.Port {
		changed = append(changed, "server.port")
	}
	if old.Server.ReadTimeout != next.Server.ReadTimeout {
		changed = append(changed, "server.read_timeout")
	}
	if old.Server.WriteTimeout != next.Server.WriteTimeout {
		changed = append(changed, "server.write_timeout")
	}
	if old.Server.IdleTimeout != next.Server.IdleTimeout {
		changed = append(changed, "server.idle_timeout")
	}
	if !reflect.DeepEqual(tlsForCompare(old.TLS), tlsForCompare(next.TLS)) {
		changed = append(changed, "tls")
	}
	if !reflect.DeepEqual(old.JWT, next.JWT) {
		changed = append(changed, "jwt")
	}
	if !reflect.DeepEqual(old.BasicAuth, next.BasicAuth) {
		changed = append(changed, "basic_auth")
	}
	if !reflect.DeepEqual(old.Identity, next.Identity) {
		changed = append(changed, "identity")
	}
	if !reflect.DeepEqual(old.Permissions, next.Permissions) {
		changed = append(changed, "permissions")
	}
	if !reflect.DeepEqual(old.Webhooks, next.Webhooks) {
		changed = append(changed, "webhooks")
	}
	if old.MetricsEnabled != next.MetricsEnabled || !reflect.DeepEqual(old.MetricsAllowedIPs, next.MetricsAllowedIPs) {
		changed = append(changed, "metrics")
	}
	if old.Logging.Level != next.Logging.Level || old.Logging.Format != next.Logging.Format {
		changed = append(changed, "logging")
	}
	if !reflect.DeepEqual(old.Discovery, next.Discovery) {
		changed = append(changed, "discovery")
	}

	return changed
}

// tlsForCompare копирует TLS-конфиг без redirect_http: этот флаг читается
// per-request и применяется на лету.
func tlsForCompare(t *config.TLSConfig) *config.TLSConfig {
	if t == nil {
		return nil
	}
	c := *t
	c.RedirectHTTP = false
	return &c
}

func restartRequiredError(fields []string) error {
	return fmt.Errorf("%w: %s", ErrRestartRequired, strings.Join(fields, ", "))
}
