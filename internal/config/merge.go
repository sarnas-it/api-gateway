package config

import (
	"sort"
	"strings"
)

// Merge возвращает новый конфиг: base + обнаруженные таргеты и правила.
// Base не мутируется. При конфликте приоритет у статики: таргет с уже
// существующим именем и правило с уже существующей парой (host, path_prefix,
// methods, target_name) пропускаются. Обнаруженные правила, ссылающиеся на
// неизвестный таргет, отбрасываются.
//
// Merge копирует Config поверхностно: вложенные срезы и указатели (Targets,
// Routing.Rules, TLS и т.п.) переиспользуются. Мутировать их на месте нельзя —
// заменяйте на новые срезы, как это делается ниже.
func Merge(base *Config, targets []TargetConfig, rules []RoutingRule) *Config {
	merged := *base

	merged.Targets = make([]TargetConfig, 0, len(base.Targets)+len(targets))
	merged.Targets = append(merged.Targets, base.Targets...)

	staticNames := make(map[string]bool, len(base.Targets))
	for _, t := range base.Targets {
		staticNames[t.Name] = true
	}
	discoveredNames := make(map[string]bool, len(targets))
	for _, t := range targets {
		if staticNames[t.Name] || discoveredNames[t.Name] {
			continue
		}
		discoveredNames[t.Name] = true
		merged.Targets = append(merged.Targets, t)
	}

	// Дефолт веса применяется и к обнаруженным таргетам: они минуют
	// Config.setDefaults. nil (label не задан) → 1; явный 0 или отрицательное
	// значение сохраняется и исключает таргет из пула.
	for i := range merged.Targets {
		if merged.Targets[i].Weight == nil {
			weight := 1
			merged.Targets[i].Weight = &weight
		}
	}

	merged.Routing.Rules = make([]RoutingRule, 0, len(base.Routing.Rules)+len(rules))
	merged.Routing.Rules = append(merged.Routing.Rules, base.Routing.Rules...)

	seenRules := make(map[string]bool, len(base.Routing.Rules))
	for _, r := range base.Routing.Rules {
		seenRules[ruleKey(r)] = true
	}
	for _, r := range rules {
		key := ruleKey(r)
		if seenRules[key] {
			continue
		}
		// Правило принимаем только если его таргет реально добавлен из discovery:
		// при коллизии имени со статикой обнаруженный таргет отброшен, значит и
		// его правила не должны маршрутизироваться на статику.
		if !discoveredNames[r.TargetName] {
			continue
		}
		seenRules[key] = true
		merged.Routing.Rules = append(merged.Routing.Rules, r)
	}

	return &merged
}

// RuleRouteKey возвращает канонический ключ route, который матчит правило:
// host, path_prefix и множество методов (без учёта порядка и регистра).
// Правила с одинаковым ключом образуют один пул балансировки.
func RuleRouteKey(r RoutingRule) string {
	return r.Host + "\x00" + r.PathPrefix + "\x00" + r.PathRegex + "\x00" +
		methodsKey(r.Methods) + "\x00" + matchersKey(r.Headers) + "\x00" + matchersKey(r.Query)
}

// matchersKey сериализует map-матчеры детерминированно: имена заголовков
// сравниваются без регистра, поэтому и в ключе они в нижнем регистре.
func matchersKey(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(strings.ToLower(k))
		b.WriteString("=")
		b.WriteString(m[k])
		b.WriteString("\x00")
	}
	return b.String()
}

// ruleKey идентифицирует правило для дедупликации: route + имя таргета.
// Разные таргеты одного route сохраняются — из них собирается пул.
func ruleKey(r RoutingRule) string {
	return RuleRouteKey(r) + "\x00" + r.TargetName
}

func methodsKey(methods []string) string {
	if len(methods) == 0 {
		return ""
	}
	normalized := make([]string, len(methods))
	for i, m := range methods {
		normalized[i] = strings.ToUpper(strings.TrimSpace(m))
	}
	sort.Strings(normalized)
	return strings.Join(normalized, ",")
}
