package discovery

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"code.sarnas.ru/api-gateway/internal/config"
)

// ParseOptions — параметры парсинга labels.
type ParseOptions struct {
	LabelPrefix       string
	ServiceNameLabels []string
	DefaultTimeout    time.Duration
	Network           string
}

// ParseContainers превращает список контейнеров в Result. Контейнеры без
// gateway.enable=true, без валидного порта или без хотя бы одного роутера
// пропускаются.
func ParseContainers(containers []Container, opts ParseOptions) Result {
	var res Result
	seen := make(map[string]bool, len(containers))

	for _, c := range containers {
		if !isEnabled(c.Labels, opts.LabelPrefix) {
			continue
		}
		if opts.Network != "" {
			if _, ok := c.Networks[opts.Network]; !ok {
				continue
			}
		}

		name := targetName(c, opts)
		if name == "" || seen[name] {
			continue
		}

		port, ok := targetPort(c, opts.LabelPrefix)
		if !ok {
			continue
		}

		routers := parseRouters(c.Labels, opts.LabelPrefix)
		if len(routers) == 0 {
			continue
		}

		scheme := labelValue(c.Labels, opts.LabelPrefix, "scheme")
		if scheme == "" {
			scheme = "http"
		}

		timeout := opts.DefaultTimeout
		if v := labelValue(c.Labels, opts.LabelPrefix, "timeout"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				timeout = d
			}
		}

		baseURL := scheme + "://" + name + ":" + strconv.Itoa(port)
		target := config.TargetConfig{
			Name:    name,
			URL:     baseURL,
			Timeout: timeout,
			Weight:  parseWeight(labelValue(c.Labels, opts.LabelPrefix, "weight")),
		}
		if health := labelValue(c.Labels, opts.LabelPrefix, "health"); health != "" {
			if strings.HasPrefix(health, "http://") || strings.HasPrefix(health, "https://") {
				target.HealthCheck = health
			} else {
				target.HealthCheck = baseURL + "/" + strings.TrimPrefix(health, "/")
			}
		}

		seen[name] = true
		res.Targets = append(res.Targets, target)
		for i := range routers {
			routers[i].TargetName = name
			res.Rules = append(res.Rules, routers[i])
		}
	}

	// Детерминированный порядок: Docker может вернуть контейнеры в любом
	// порядке, а стабильный JSON нужен, чтобы не дёргать Reload вхолостую.
	sort.Slice(res.Targets, func(i, j int) bool {
		return res.Targets[i].Name < res.Targets[j].Name
	})
	sort.SliceStable(res.Rules, func(i, j int) bool {
		if res.Rules[i].Host != res.Rules[j].Host {
			return res.Rules[i].Host < res.Rules[j].Host
		}
		return res.Rules[i].PathPrefix < res.Rules[j].PathPrefix
	})

	return res
}

func isEnabled(labels map[string]string, prefix string) bool {
	return parseBool(labelValue(labels, prefix, "enable"))
}

func targetName(c Container, opts ParseOptions) string {
	if v := labelValue(c.Labels, opts.LabelPrefix, "name"); v != "" {
		return v
	}
	for _, l := range opts.ServiceNameLabels {
		if v := c.Labels[l]; v != "" {
			return v
		}
	}
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return ""
}

func targetPort(c Container, prefix string) (int, bool) {
	if v := labelValue(c.Labels, prefix, "port"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return 0, false
		}
		return n, true
	}
	ports := make(map[int]struct{})
	for _, p := range c.Ports {
		if p.PrivatePort <= 0 {
			continue
		}
		// Пустой Type считаем совместимым (тестовые фикстуры его опускают),
		// иначе учитываем только TCP: UDP/SCTP не обслуживаются HTTP-прокси.
		if p.Type != "" && p.Type != "tcp" {
			continue
		}
		ports[p.PrivatePort] = struct{}{}
	}
	if len(ports) == 1 {
		for p := range ports {
			return p, true
		}
	}
	return 0, false
}

// labelValue возвращает значение label "<prefix>.<field>" или "".
func labelValue(labels map[string]string, prefix, field string) string {
	if labels == nil {
		return ""
	}
	return labels[prefix+"."+field]
}

// parseBool разбирает булево значение label. Принимаются true/1/yes
// (регистронезависимо, с обрезкой пробелов) — см. контракт labels в спецификации.
func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}

// parseWeight разбирает label веса. Пустое или невалидное значение → nil
// (дефолт 1); явный 0 и отрицательные сохраняются, чтобы исключать таргет
// из пула.
func parseWeight(v string) *int {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return nil
	}
	return &n
}

// routerFields — множество полей роутера, допустимых в labels.
var routerFields = map[string]struct{}{
	"host":             {},
	"path_prefix":      {},
	"methods":          {},
	"strip_path":       {},
	"auth.required":    {},
	"auth.roles":       {},
	"auth.roles_all":   {},
	"auth.strip_token": {},
	"rate_limit.rps":   {},
	"rate_limit.burst": {},
}

// parseRouters собирает роутеры из labels. Короткая форма "<prefix>.<field>"
// даёт роутер "default"; именованная "<prefix>.router.<id>.<field>" — роутер id.
// Для роутера "default" именованная форма переопределяет короткую.
// Роутеры сортируются по id для детерминированного порядка.
func parseRouters(labels map[string]string, prefix string) []config.RoutingRule {
	shortPrefix := prefix + "."
	routerPrefix := prefix + ".router."
	fields := make(map[string]map[string]string)

	setField := func(id, field, v string) {
		if fields[id] == nil {
			fields[id] = map[string]string{}
		}
		fields[id][field] = v
	}

	// Сначала короткая форма — база для роутера "default".
	for k, v := range labels {
		if !strings.HasPrefix(k, shortPrefix) {
			continue
		}
		field := strings.TrimPrefix(k, shortPrefix)
		if _, ok := routerFields[field]; ok {
			setField("default", field, v)
		}
	}

	// Затем именованная форма — переопределяет короткую для совпавших полей.
	for k, v := range labels {
		if !strings.HasPrefix(k, routerPrefix) {
			continue
		}
		rest := strings.TrimPrefix(k, routerPrefix)
		id, field, ok := strings.Cut(rest, ".")
		if !ok || id == "" {
			continue
		}
		if _, ok := routerFields[field]; !ok {
			continue
		}
		setField(id, field, v)
	}

	ids := make([]string, 0, len(fields))
	for id := range fields {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var out []config.RoutingRule
	for _, id := range ids {
		rule := buildRule(fields[id])
		if rule.Host == "" && rule.PathPrefix == "" {
			continue
		}
		if rule.Host != "" && rule.PathPrefix == "" {
			rule.PathPrefix = "/"
		}
		out = append(out, rule)
	}
	return out
}

func buildRule(f map[string]string) config.RoutingRule {
	rule := config.RoutingRule{
		Host:       f["host"],
		PathPrefix: f["path_prefix"],
		StripPath:  parseBool(f["strip_path"]),
	}
	if m := f["methods"]; m != "" {
		rule.Methods = splitCSV(m)
	}

	if v, ok := f["auth.required"]; ok {
		required := parseBool(v)
		rule.Auth = ensureAuth(rule.Auth)
		rule.Auth.Required = required
	}
	if v, ok := f["auth.roles"]; ok {
		rule.Auth = ensureAuth(rule.Auth)
		rule.Auth.Roles = splitCSV(v)
	}
	if v, ok := f["auth.roles_all"]; ok {
		rule.Auth = ensureAuth(rule.Auth)
		rule.Auth.RolesAll = splitCSV(v)
	}
	if v, ok := f["auth.strip_token"]; ok {
		strip := parseBool(v)
		rule.Auth = ensureAuth(rule.Auth)
		rule.Auth.StripToken = &strip
	}

	if v, ok := f["rate_limit.rps"]; ok {
		if rps, err := strconv.ParseFloat(v, 64); err == nil {
			burst := 0
			if b, err := strconv.Atoi(f["rate_limit.burst"]); err == nil {
				burst = b
			}
			rule.RateLimit = &config.RateLimitRule{RequestsPerSecond: rps, Burst: burst}
		}
	}

	return rule
}

func ensureAuth(a *config.AuthRule) *config.AuthRule {
	if a == nil {
		return &config.AuthRule{}
	}
	return a
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
