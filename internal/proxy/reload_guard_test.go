package proxy

import (
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"code.sarnas.ru/api-gateway/internal/config"
)

func reloadBaseConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         8080,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			IdleTimeout:  time.Second,
		},
		Targets: []config.TargetConfig{{Name: "svc", URL: "http://svc:9000"}},
		Routing: config.RoutingConfig{Rules: []config.RoutingRule{{
			PathPrefix: "/api",
			TargetName: "svc",
		}}},
		TLS: &config.TLSConfig{
			Enabled:      true,
			Port:         443,
			HTTPPort:     80,
			Domains:      []string{"api.example.com"},
			Email:        "admin@example.com",
			RedirectHTTP: true,
		},
		JWT:       config.JWTConfig{SecretKey: "secret", Algorithm: "HS256"},
		BasicAuth: config.BasicAuthConfig{Enabled: true, Username: "u", Password: "p"},
		Permissions: config.PermissionsConfig{
			Enabled:         true,
			ServiceURL:      "http://permissions",
			InvalidateToken: "token",
		},
		Identity: config.IdentityConfig{Enabled: true},
		Webhooks: []config.WebhookConfig{{
			Name:       "audit",
			Transport:  config.TransportWebhook,
			WebhookURL: "http://audit",
		}},
		Discovery: &config.DiscoveryConfig{Enabled: true, Provider: "docker"},
		Logging:   config.LoggingConfig{Level: "info", Format: "console"},
		App:       config.App{MetricsEnabled: true, MetricsAllowedIPs: []string{"10.0.0.0/8"}},
	}
}

func newReloadProxy(cfg *config.Config) *MultiProxy {
	mp := &MultiProxy{
		targets:     map[string]*TargetProxy{},
		routeByRule: map[*config.RoutingRule]*RouteConfig{},
		logger:      zap.NewNop(),
		metrics:     NewMetrics(false),
	}
	mp.config.Store(cfg)
	mp.rebuildRouteConfigs(cfg)
	return mp
}

func TestReload_RejectsChangesRequiringRestart(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{"jwt", func(c *config.Config) { c.JWT.SecretKey = "new-secret" }},
		{"tls domains", func(c *config.Config) { c.TLS.Domains = []string{"other.example.com"} }},
		{"tls worker", func(c *config.Config) { c.TLS.WorkerPort = 8444 }},
		{"basic auth", func(c *config.Config) { c.BasicAuth.Password = "new" }},
		{"permissions", func(c *config.Config) { c.Permissions.ServiceURL = "http://new-permissions" }},
		{"identity", func(c *config.Config) { c.Identity.Enabled = false }},
		{"webhooks", func(c *config.Config) { c.Webhooks[0].WebhookURL = "http://new-audit" }},
		{"server port", func(c *config.Config) { c.Server.Port = 9090 }},
		{"server read timeout", func(c *config.Config) { c.Server.ReadTimeout = 2 * time.Second }},
		{"logging level", func(c *config.Config) { c.Logging.Level = "debug" }},
		{"discovery provider", func(c *config.Config) { c.Discovery.Provider = "podman" }},
		{"metrics allowlist", func(c *config.Config) { c.MetricsAllowedIPs = []string{"192.168.0.0/16"} }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := reloadBaseConfig()
			mp := newReloadProxy(base)
			next := reloadBaseConfig()
			tc.mutate(next)

			err := mp.Reload(next)
			if !errors.Is(err, ErrRestartRequired) {
				t.Fatalf("err = %v, want ErrRestartRequired", err)
			}
			if mp.config.Load() != base {
				t.Fatal("rejected reload must keep the old config")
			}
		})
	}
}

func TestReload_AllowsChangesApplicableAtRuntime(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{"routing", func(c *config.Config) { c.Routing.Rules[0].PathPrefix = "/v2" }},
		{"target url", func(c *config.Config) { c.Targets[0].URL = "http://svc2:9000" }},
		{"trusted proxies", func(c *config.Config) { c.Server.TrustedProxies = []string{"10.0.0.0/8"} }},
		{"http redirect", func(c *config.Config) { c.TLS.RedirectHTTP = false }},
		{"body limit", func(c *config.Config) { c.Server.MaxRequestBodySize = int64Ptr(1024) }},
		{"cors", func(c *config.Config) { c.Headers.CORS = &config.CORSConfig{Enabled: true} }},
		{"static", func(c *config.Config) { c.Static = &config.StaticConfig{} }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := reloadBaseConfig()
			mp := newReloadProxy(base)
			next := reloadBaseConfig()
			tc.mutate(next)

			if err := mp.Reload(next); err != nil {
				t.Fatalf("reload = %v, want nil", err)
			}
			if mp.config.Load() != next {
				t.Fatal("reload must install the new config")
			}
		})
	}
}

func int64Ptr(v int64) *int64 { return &v }
