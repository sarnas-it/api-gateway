package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"code.sarnas.ru/api-gateway/internal/config"
	"code.sarnas.ru/api-gateway/internal/discovery"
	"code.sarnas.ru/api-gateway/pkg/plugin"
)

// validateDiscoveryModule проверяет, что discovery.module вкомпилирован.
// Вызывается и на старте, и в `gateway --check`.
func validateDiscoveryModule(cfg *config.Config) error {
	d := cfg.Discovery
	if d == nil || !d.Enabled || d.Module == "" {
		return nil
	}
	if _, ok := plugin.DiscoveryProviders()[d.Module]; !ok {
		return fmt.Errorf("discovery.module %q is not compiled", d.Module)
	}
	return nil
}

// moduleDiscovery адаптирует plugin.DiscoveryProvider к internal/discovery.Provider:
// ядро discovery не знает про pkg/plugin, а модули — про internal/config.
type moduleDiscovery struct {
	provider       plugin.DiscoveryProvider
	log            *zap.Logger
	defaultTimeout time.Duration
}

func (m moduleDiscovery) Start(ctx context.Context, onResult func(discovery.Result)) error {
	return m.provider.Watch(ctx, func(services []plugin.DiscoveredService) {
		onResult(servicesToResult(services, m.defaultTimeout, m.log))
	})
}

// Stop ничего не делает: Watch завершается по отмене ctx, которую делает Manager.
func (moduleDiscovery) Stop() error { return nil }

// servicesToResult превращает сервисы модуля в targets и правила.
// Невалидные записи (пустые имя/адрес) пропускаются, дубликаты имён — тоже.
func servicesToResult(services []plugin.DiscoveredService, defaultTimeout time.Duration, log *zap.Logger) discovery.Result {
	if defaultTimeout <= 0 {
		defaultTimeout = 10 * time.Second
	}
	var res discovery.Result
	seen := make(map[string]bool, len(services))
	rules := make(map[string]bool, len(services))
	for _, s := range services {
		name := strings.TrimSpace(s.Name)
		addr := strings.TrimSpace(s.Address)
		if name == "" || addr == "" || seen[name] {
			continue
		}
		seen[name] = true
		url := addr
		if !strings.Contains(addr, "://") {
			url = "http://" + addr
		}
		res.Targets = append(res.Targets, config.TargetConfig{
			Name:    name,
			URL:     url,
			Timeout: defaultTimeout,
		})
		prefix := strings.TrimSpace(s.PathPrefix)
		if prefix == "" {
			prefix = "/" + name
		}
		key := prefix + "|" + name
		if rules[key] {
			continue
		}
		rules[key] = true
		res.Rules = append(res.Rules, config.RoutingRule{
			PathPrefix: prefix,
			TargetName: name,
			StripPath:  s.StripPath,
		})
	}
	if log != nil && len(res.Targets) == 0 {
		log.Warn("discovery module returned no services")
	}
	return res
}

// discoveryModuleProvider строит источник discovery из скомпилированного модуля.
func discoveryModuleProvider(cfg *config.Config, log *zap.Logger) (discovery.Provider, error) {
	d := cfg.Discovery
	if d == nil || !d.Enabled || d.Module == "" {
		return nil, nil
	}
	p, ok := plugin.DiscoveryProviders()[d.Module]
	if !ok {
		return nil, fmt.Errorf("discovery.module %q is not compiled", d.Module)
	}
	return moduleDiscovery{provider: p, log: log, defaultTimeout: d.DefaultTimeout}, nil
}
