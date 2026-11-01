package config

import (
	"net/http/httptest"
	"testing"
)

func matcherConfig(t *testing.T, yaml string) *Config {
	t.Helper()
	cfg, _, err := Load(writeTempConfig(t, yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestFindTargetForRequest_PathRegex(t *testing.T) {
	cfg := matcherConfig(t, `
targets:
  - name: api
    url: http://api:1
  - name: rest
    url: http://rest:1
routing:
  rules:
    - path_prefix: "/"
      target_name: rest
    - path_regex: "^/api/v[0-9]+/users/[0-9]+$"
      target_name: api
`)
	req := httptest.NewRequest("GET", "/api/v2/users/42", nil)
	if tg, _ := cfg.FindTargetForRequest(req); tg == nil || tg.Name != "api" {
		t.Fatalf("regex rule must win: %+v", tg)
	}
	req = httptest.NewRequest("GET", "/api/v2/users/abc", nil)
	if tg, _ := cfg.FindTargetForRequest(req); tg == nil || tg.Name != "rest" {
		t.Fatalf("regex must not match non-numeric id: %+v", tg)
	}
}

func TestFindTargetForRequest_Headers(t *testing.T) {
	cfg := matcherConfig(t, `
targets:
  - name: v2
    url: http://v2:1
  - name: fallback
    url: http://fallback:1
routing:
  rules:
    - path_prefix: "/"
      target_name: fallback
    - path_prefix: "/"
      target_name: v2
      headers:
        X-Api-Version: "2"
`)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Api-Version", "2")
	if tg, _ := cfg.FindTargetForRequest(req); tg == nil || tg.Name != "v2" {
		t.Fatalf("header rule must win: %+v", tg)
	}

	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Api-Version", "3")
	if tg, _ := cfg.FindTargetForRequest(req); tg == nil || tg.Name != "fallback" {
		t.Fatalf("wrong header value must fall through: %+v", tg)
	}

	req = httptest.NewRequest("GET", "/", nil)
	if tg, _ := cfg.FindTargetForRequest(req); tg == nil || tg.Name != "fallback" {
		t.Fatalf("missing header must fall through: %+v", tg)
	}
}

func TestFindTargetForRequest_HeaderPresence(t *testing.T) {
	cfg := matcherConfig(t, `
targets:
  - name: beta
    url: http://beta:1
  - name: stable
    url: http://stable:1
routing:
  rules:
    - path_prefix: "/"
      target_name: stable
    - path_prefix: "/"
      target_name: beta
      headers:
        X-Beta: ""
`)
	req := httptest.NewRequest("GET", "/", nil)
	// Пустое значение в матчере — «заголовок присутствует», даже пустой.
	req.Header["X-Beta"] = []string{""}
	if tg, _ := cfg.FindTargetForRequest(req); tg == nil || tg.Name != "beta" {
		t.Fatalf("presence matcher: %+v", tg)
	}
	req = httptest.NewRequest("GET", "/", nil)
	if tg, _ := cfg.FindTargetForRequest(req); tg == nil || tg.Name != "stable" {
		t.Fatalf("absent header must not match: %+v", tg)
	}
}

func TestFindTargetForRequest_Query(t *testing.T) {
	cfg := matcherConfig(t, `
targets:
  - name: debug
    url: http://debug:1
  - name: normal
    url: http://normal:1
routing:
  rules:
    - path_prefix: "/"
      target_name: normal
    - path_prefix: "/"
      target_name: debug
      query:
        debug: "1"
`)
	if tg, _ := cfg.FindTargetForRequest(httptest.NewRequest("GET", "/?debug=1", nil)); tg == nil || tg.Name != "debug" {
		t.Fatalf("query rule must win: %+v", tg)
	}
	if tg, _ := cfg.FindTargetForRequest(httptest.NewRequest("GET", "/?debug=0", nil)); tg == nil || tg.Name != "normal" {
		t.Fatalf("wrong query value must fall through: %+v", tg)
	}
	if tg, _ := cfg.FindTargetForRequest(httptest.NewRequest("GET", "/?other=1", nil)); tg == nil || tg.Name != "normal" {
		t.Fatalf("missing query must fall through: %+v", tg)
	}
}

func TestFindTargetForPath_IgnoresRequestMatchers(t *testing.T) {
	cfg := matcherConfig(t, `
targets:
  - name: plain
    url: http://plain:1
  - name: header
    url: http://header:1
routing:
  rules:
    - path_prefix: "/"
      target_name: plain
    - path_prefix: "/"
      target_name: header
      headers:
        X-Only: "1"
`)
	if tg, _ := cfg.FindTargetForPath("/", "GET"); tg == nil || tg.Name != "plain" {
		t.Fatalf("path-only matcher must not choose header rules: %+v", tg)
	}
}

func TestValidate_MatcherErrors(t *testing.T) {
	cases := map[string]string{
		"prefix and regex": `
targets:
  - name: api
    url: http://api:1
routing:
  rules:
    - path_prefix: "/api"
      path_regex: "^/api$"
      target_name: api
`,
		"invalid regex": `
targets:
  - name: api
    url: http://api:1
routing:
  rules:
    - path_regex: "([unclosed"
      target_name: api
`,
		"neither": `
targets:
  - name: api
    url: http://api:1
routing:
  rules:
    - target_name: api
`,
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Load(writeTempConfig(t, yaml)); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestRuleRouteKey_MatchersSeparatePools(t *testing.T) {
	base := RoutingRule{Host: "a.example.com", PathPrefix: "/", TargetName: "api"}
	withHeader := base
	withHeader.Headers = map[string]string{"X-Tenant": "a"}
	otherCase := base
	otherCase.Headers = map[string]string{"x-tenant": "a"}

	if RuleRouteKey(base) == RuleRouteKey(withHeader) {
		t.Fatal("rules with different headers must not share a pool")
	}
	if RuleRouteKey(withHeader) != RuleRouteKey(otherCase) {
		t.Fatal("header names must be case-insensitive in the pool key")
	}
}
