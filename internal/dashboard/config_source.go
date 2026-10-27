package dashboard

import (
	"fmt"
	"net/url"
	"sort"

	"code.sarnas.ru/api-gateway/internal/config"
)

// ConfigSummary is a secret-free digest of the gateway configuration. Secret
// fields (jwt.secret_key, basic_auth.password, permissions.api_key,
// permissions.invalidate_token) are deliberately never copied into it.
type ConfigSummary struct {
	Loaded             bool         `json:"loaded"`
	Port               int          `json:"port"`
	TLSEnabled         bool         `json:"tls_enabled"`
	DiscoveryEnabled   bool         `json:"discovery_enabled"`
	PermissionsEnabled bool         `json:"permissions_enabled"`
	TargetCount        int          `json:"target_count"`
	RuleCount          int          `json:"rule_count"`
	WebhookCount       int          `json:"webhook_count"`
	WebhookTransports  []string     `json:"webhook_transports"`
	Targets            []TargetView `json:"targets"`
	Rules              []RuleView   `json:"rules"`
	Warnings           []string     `json:"warnings"`
}

// LoadConfigSummary reads and summarizes the gateway config at path. It uses
// the lenient loader so the dashboard does not need the gateway's secret
// environment variables: unresolved ${VAR} references stay literal and a
// config without targets still parses.
func LoadConfigSummary(path string) (*ConfigSummary, error) {
	cfg, warnings, err := config.LoadLenient(path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return summarizeConfig(cfg, warnings), nil
}

func summarizeConfig(cfg *config.Config, warnings []string) *ConfigSummary {
	if warnings == nil {
		warnings = []string{}
	}
	return &ConfigSummary{
		Loaded:             true,
		Port:               cfg.Server.Port,
		TLSEnabled:         cfg.TLS != nil && cfg.TLS.Enabled,
		DiscoveryEnabled:   cfg.Discovery != nil && cfg.Discovery.Enabled,
		PermissionsEnabled: cfg.Permissions.Enabled,
		TargetCount:        len(cfg.Targets),
		RuleCount:          len(cfg.Routing.Rules),
		WebhookCount:       len(cfg.Webhooks),
		WebhookTransports:  webhookTransports(cfg.Webhooks),
		Targets:            viewTargets(cfg.Targets),
		Rules:              viewRules(cfg.Routing.Rules),
		Warnings:           warnings,
	}
}

func webhookTransports(webhooks []config.WebhookConfig) []string {
	seen := make(map[string]bool)
	out := []string{}
	for _, w := range webhooks {
		transport := string(w.Transport)
		if transport == "" || seen[transport] {
			continue
		}
		seen[transport] = true
		out = append(out, transport)
	}
	sort.Strings(out)
	return out
}

func viewTargets(targets []config.TargetConfig) []TargetView {
	out := make([]TargetView, 0, len(targets))
	for _, t := range targets {
		out = append(out, TargetView{
			Name:        t.Name,
			URL:         redactURL(t.URL),
			PathPrefix:  t.PathPrefix,
			Weight:      t.EffectiveWeight(),
			Timeout:     t.Timeout.String(),
			HealthCheck: redactURL(t.HealthCheck),
		})
	}
	return out
}

func viewRules(rules []config.RoutingRule) []RuleView {
	out := make([]RuleView, 0, len(rules))
	for _, r := range rules {
		out = append(out, RuleView{
			Host:       r.Host,
			PathPrefix: r.PathPrefix,
			TargetName: r.TargetName,
			Methods:    r.Methods,
			StripPath:  r.StripPath,
			Auth:       r.Auth != nil,
			RateLimit:  r.RateLimit != nil,
		})
	}
	return out
}

// redactURL strips embedded userinfo (credentials) from a URL. URLs that fail
// to parse fall back to a best-effort regex strip so credentials are never
// rendered even for malformed targets. Empty values are returned unchanged.
func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return redactCredentials(raw)
	}
	if u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}
