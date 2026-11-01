package app

import (
	"fmt"
	"io"

	"code.sarnas.ru/api-gateway/internal/config"
	"code.sarnas.ru/api-gateway/pkg/plugin"
)

// runCheck загружает и валидирует конфиг, печатает предупреждения и ошибки.
// Возвращает код выхода: 0 — конфиг валиден, 1 — ошибка (или предупреждения
// при strict). Используется флагом -check и пригодно для CI.
func runCheck(path string, strict bool, stdout, stderr io.Writer) int {
	cfg, warnings, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(stderr, "config invalid: %v\n", err)
		return 1
	}

	for _, w := range warnings {
		fmt.Fprintf(stderr, "warning: unknown config key: %s\n", w)
	}
	if err := plugin.ValidateConfig(cfg.Plugins); err != nil {
		fmt.Fprintf(stderr, "config invalid: %v\n", err)
		return 1
	}
	if _, err := authConnectors(cfg); err != nil {
		fmt.Fprintf(stderr, "config invalid: %v\n", err)
		return 1
	}
	if err := validateDiscoveryModule(cfg); err != nil {
		fmt.Fprintf(stderr, "config invalid: %v\n", err)
		return 1
	}
	if strict && len(warnings) > 0 {
		fmt.Fprintf(stderr, "config invalid: %d warning(s) treated as errors (-strict)\n", len(warnings))
		return 1
	}

	discovery := cfg.Discovery != nil && cfg.Discovery.Enabled
	fmt.Fprintf(stdout, "config OK: %s\n", path)
	fmt.Fprintf(stdout, "  targets: %d, routing rules: %d, discovery: %t, permissions: %t\n",
		len(cfg.Targets), len(cfg.Routing.Rules), discovery, cfg.Permissions.Enabled)
	return 0
}
