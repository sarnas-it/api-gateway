package discovery

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"code.sarnas.ru/api-gateway/internal/config"
)

func testOpts() ParseOptions {
	return ParseOptions{
		LabelPrefix:       "gateway",
		ServiceNameLabels: []string{"com.docker.compose.service"},
		DefaultTimeout:    30 * time.Second,
	}
}

func TestParseTarget_Defaults(t *testing.T) {
	res := ParseContainers([]Container{{
		ID:     "c1",
		Names:  []string{"/sarnas-blog-1"},
		Labels: map[string]string{"gateway.enable": "true", "gateway.port": "8085", "gateway.path_prefix": "/api/blog"},
	}}, testOpts())

	if len(res.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(res.Targets))
	}
	got := res.Targets[0]
	if got.Name != "sarnas-blog-1" {
		t.Errorf("name: got %q", got.Name)
	}
	if got.URL != "http://sarnas-blog-1:8085" {
		t.Errorf("url: got %q", got.URL)
	}
	if got.Timeout != 30*time.Second {
		t.Errorf("timeout: got %v", got.Timeout)
	}
}

func TestParseTarget_UsesServiceNameLabel(t *testing.T) {
	res := ParseContainers([]Container{{
		ID:    "c1",
		Names: []string{"/random-name-1"},
		Labels: map[string]string{
			"gateway.enable":             "true",
			"gateway.port":               "8085",
			"gateway.path_prefix":        "/api/blog",
			"com.docker.compose.service": "blog",
		},
	}}, testOpts())

	if len(res.Targets) != 1 || res.Targets[0].Name != "blog" {
		t.Fatalf("expected name blog, got %+v", res.Targets)
	}
	if res.Targets[0].URL != "http://blog:8085" {
		t.Errorf("url: got %q", res.Targets[0].URL)
	}
}

func TestParseTarget_ExplicitNameSchemeTimeoutHealth(t *testing.T) {
	res := ParseContainers([]Container{{
		ID: "c1",
		Labels: map[string]string{
			"gateway.enable":      "true",
			"gateway.name":        "passport",
			"gateway.port":        "8085",
			"gateway.scheme":      "https",
			"gateway.timeout":     "10s",
			"gateway.health":      "/health",
			"gateway.path_prefix": "/api/auth",
		},
	}}, testOpts())

	if len(res.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(res.Targets))
	}
	got := res.Targets[0]
	if got.Name != "passport" || got.URL != "https://passport:8085" {
		t.Errorf("name/url: got %q %q", got.Name, got.URL)
	}
	if got.Timeout != 10*time.Second {
		t.Errorf("timeout: got %v", got.Timeout)
	}
	if got.HealthCheck != "https://passport:8085/health" {
		t.Errorf("health: got %q", got.HealthCheck)
	}
}

func TestParseTarget_HealthPathNormalized(t *testing.T) {
	res := ParseContainers([]Container{{
		ID: "c1",
		Labels: map[string]string{
			"gateway.enable": "true", "gateway.name": "svc", "gateway.port": "9000",
			"gateway.health": "health", "gateway.host": "x",
		},
	}}, testOpts())

	if len(res.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(res.Targets))
	}
	if got := res.Targets[0].HealthCheck; got != "http://svc:9000/health" {
		t.Errorf("health path must be normalized with a leading slash: got %q", got)
	}
}

func TestParseTarget_FullHealthURLKept(t *testing.T) {
	res := ParseContainers([]Container{{
		ID: "c1",
		Labels: map[string]string{
			"gateway.enable": "true",
			"gateway.name":   "svc",
			"gateway.port":   "9000",
			"gateway.health": "http://probe:1234/healthz",
			"gateway.host":   "svc.local",
		},
	}}, testOpts())

	if res.Targets[0].HealthCheck != "http://probe:1234/healthz" {
		t.Errorf("health: got %q", res.Targets[0].HealthCheck)
	}
}

func TestParseTarget_SingleExposedPort(t *testing.T) {
	res := ParseContainers([]Container{{
		ID:     "c1",
		Labels: map[string]string{"gateway.enable": "true", "gateway.name": "svc", "gateway.host": "x"},
		Ports:  []Port{{PrivatePort: 9000, Type: "tcp"}},
	}}, testOpts())

	if len(res.Targets) != 1 || res.Targets[0].URL != "http://svc:9000" {
		t.Fatalf("expected port 9000, got %+v", res.Targets)
	}
}

func TestParseTarget_MultiplePortsSkipped(t *testing.T) {
	res := ParseContainers([]Container{{
		ID:     "c1",
		Labels: map[string]string{"gateway.enable": "true", "gateway.name": "svc", "gateway.host": "x"},
		Ports:  []Port{{PrivatePort: 9000}, {PrivatePort: 9001}},
	}}, testOpts())

	if len(res.Targets) != 0 {
		t.Fatalf("expected skip, got %+v", res.Targets)
	}
}

func TestParseTarget_IgnoresNonTCPPorts(t *testing.T) {
	res := ParseContainers([]Container{{
		ID:     "c1",
		Labels: map[string]string{"gateway.enable": "true", "gateway.name": "svc", "gateway.host": "x"},
		Ports:  []Port{{PrivatePort: 9000, Type: "tcp"}, {PrivatePort: 9001, Type: "udp"}},
	}}, testOpts())

	if len(res.Targets) != 1 || res.Targets[0].URL != "http://svc:9000" {
		t.Fatalf("expected single tcp port to win, got %+v", res.Targets)
	}
}

func TestParseTarget_OnlyUDPPortSkipped(t *testing.T) {
	res := ParseContainers([]Container{{
		ID:     "c1",
		Labels: map[string]string{"gateway.enable": "true", "gateway.name": "svc", "gateway.host": "x"},
		Ports:  []Port{{PrivatePort: 9001, Type: "udp"}},
	}}, testOpts())

	if len(res.Targets) != 0 {
		t.Fatalf("udp-only container must be skipped, got %+v", res.Targets)
	}
}

func TestParseTarget_NotEnabledIgnored(t *testing.T) {
	res := ParseContainers([]Container{{
		ID:     "c1",
		Labels: map[string]string{"gateway.port": "9000"},
	}}, testOpts())

	if len(res.Targets) != 0 {
		t.Fatalf("expected no targets, got %+v", res.Targets)
	}
}

func TestParseTarget_ReplicasDeduped(t *testing.T) {
	res := ParseContainers([]Container{
		{ID: "c1", Labels: map[string]string{"gateway.enable": "true", "gateway.name": "svc", "gateway.port": "9000", "gateway.host": "x"}},
		{ID: "c2", Labels: map[string]string{"gateway.enable": "true", "gateway.name": "svc", "gateway.port": "9000", "gateway.host": "x"}},
	}, testOpts())

	if len(res.Targets) != 1 {
		t.Fatalf("expected replicas deduped to 1, got %d", len(res.Targets))
	}
	if len(res.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(res.Rules))
	}
}

func TestParseTarget_NetworkFilter(t *testing.T) {
	opts := testOpts()
	opts.Network = "backend"
	res := ParseContainers([]Container{{
		ID:       "c1",
		Labels:   map[string]string{"gateway.enable": "true", "gateway.name": "svc", "gateway.port": "9000", "gateway.host": "x"},
		Networks: map[string]Network{"frontend": {}},
	}}, opts)

	if len(res.Targets) != 0 {
		t.Fatalf("container outside network must be skipped, got %+v", res.Targets)
	}
}

func TestParseTarget_Weight(t *testing.T) {
	res := ParseContainers([]Container{{
		ID: "c1",
		Labels: map[string]string{
			"gateway.enable": "true", "gateway.name": "svc", "gateway.port": "9000",
			"gateway.host": "x", "gateway.weight": "5",
		},
	}}, testOpts())

	if len(res.Targets) != 1 || res.Targets[0].EffectiveWeight() != 5 {
		t.Fatalf("expected weight 5, got %+v", res.Targets)
	}
}

func TestParseTarget_WeightZeroPreserved(t *testing.T) {
	res := ParseContainers([]Container{{
		ID: "c1",
		Labels: map[string]string{
			"gateway.enable": "true", "gateway.name": "svc", "gateway.port": "9000",
			"gateway.host": "x", "gateway.weight": "0",
		},
	}}, testOpts())

	if len(res.Targets) != 1 {
		t.Fatalf("expected 1 target, got %+v", res.Targets)
	}
	if res.Targets[0].Weight == nil || *res.Targets[0].Weight != 0 {
		t.Fatalf("label weight 0 must be preserved as explicit 0, got %v", res.Targets[0].Weight)
	}
	if res.Targets[0].EffectiveWeight() != 0 {
		t.Fatalf("explicit label weight 0 effective = %d, want 0 (excluded)", res.Targets[0].EffectiveWeight())
	}
}

func TestParseTarget_NoWeightLabelDefaultsToOne(t *testing.T) {
	res := ParseContainers([]Container{{
		ID: "c1",
		Labels: map[string]string{
			"gateway.enable": "true", "gateway.name": "svc", "gateway.port": "9000",
			"gateway.host": "x",
		},
	}}, testOpts())

	if len(res.Targets) != 1 {
		t.Fatalf("expected 1 target, got %+v", res.Targets)
	}
	if res.Targets[0].Weight != nil {
		t.Fatalf("omitted weight label must stay nil (default), got %d", *res.Targets[0].Weight)
	}
	if res.Targets[0].EffectiveWeight() != 1 {
		t.Fatalf("omitted weight effective = %d, want 1", res.Targets[0].EffectiveWeight())
	}
}

func TestParseTarget_NoRouteSkipped(t *testing.T) {
	res := ParseContainers([]Container{{
		ID:     "c1",
		Labels: map[string]string{"gateway.enable": "true", "gateway.name": "svc", "gateway.port": "9000"},
	}}, testOpts())

	if len(res.Targets) != 0 {
		t.Fatalf("target without any route must be skipped, got %+v", res.Targets)
	}
}

func TestParseTarget_RouterFields(t *testing.T) {
	stripFalse := false
	stripTrue := true

	tests := []struct {
		name  string
		extra map[string]string
		want  config.RoutingRule
	}{
		{
			name:  "methods trimmed and split",
			extra: map[string]string{"gateway.methods": "GET, POST , DELETE"},
			want:  config.RoutingRule{Host: "x", PathPrefix: "/", Methods: []string{"GET", "POST", "DELETE"}},
		},
		{
			name:  "strip_path true",
			extra: map[string]string{"gateway.strip_path": "true"},
			want:  config.RoutingRule{Host: "x", PathPrefix: "/", StripPath: true},
		},
		{
			name:  "auth required true",
			extra: map[string]string{"gateway.auth.required": "true"},
			want:  config.RoutingRule{Host: "x", PathPrefix: "/", Auth: &config.AuthRule{Required: true}},
		},
		{
			name:  "auth roles split",
			extra: map[string]string{"gateway.auth.roles": "admin, user"},
			want:  config.RoutingRule{Host: "x", PathPrefix: "/", Auth: &config.AuthRule{Roles: []string{"admin", "user"}}},
		},
		{
			name:  "auth roles_all split",
			extra: map[string]string{"gateway.auth.roles_all": "mfa, verified"},
			want:  config.RoutingRule{Host: "x", PathPrefix: "/", Auth: &config.AuthRule{RolesAll: []string{"mfa", "verified"}}},
		},
		{
			name:  "auth strip_token false",
			extra: map[string]string{"gateway.auth.strip_token": "false"},
			want:  config.RoutingRule{Host: "x", PathPrefix: "/", Auth: &config.AuthRule{StripToken: &stripFalse}},
		},
		{
			name: "auth required and strip_token true",
			extra: map[string]string{
				"gateway.auth.required":    "true",
				"gateway.auth.strip_token": "true",
			},
			want: config.RoutingRule{Host: "x", PathPrefix: "/", Auth: &config.AuthRule{Required: true, StripToken: &stripTrue}},
		},
		{
			name: "rate limit rps and burst",
			extra: map[string]string{
				"gateway.rate_limit.rps":   "12.5",
				"gateway.rate_limit.burst": "20",
			},
			want: config.RoutingRule{Host: "x", PathPrefix: "/", RateLimit: &config.RateLimitRule{RequestsPerSecond: 12.5, Burst: 20}},
		},
		{
			name:  "rate limit rps only leaves burst zero",
			extra: map[string]string{"gateway.rate_limit.rps": "3"},
			want:  config.RoutingRule{Host: "x", PathPrefix: "/", RateLimit: &config.RateLimitRule{RequestsPerSecond: 3, Burst: 0}},
		},
		{
			name:  "rate limit invalid rps yields nil",
			extra: map[string]string{"gateway.rate_limit.rps": "not-a-number"},
			want:  config.RoutingRule{Host: "x", PathPrefix: "/"},
		},
		{
			name:  "path_prefix used as-is",
			extra: map[string]string{"gateway.path_prefix": "/api/v1"},
			want:  config.RoutingRule{Host: "x", PathPrefix: "/api/v1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels := map[string]string{
				"gateway.enable": "true",
				"gateway.name":   "svc",
				"gateway.port":   "9000",
				"gateway.host":   "x",
			}
			for k, v := range tt.extra {
				labels[k] = v
			}

			res := ParseContainers([]Container{{ID: "c1", Labels: labels}}, testOpts())
			if len(res.Rules) != 1 {
				t.Fatalf("expected 1 rule, got %d: %+v", len(res.Rules), res.Rules)
			}

			want := tt.want
			want.TargetName = "svc"
			if !reflect.DeepEqual(res.Rules[0], want) {
				t.Fatalf("rule mismatch:\n got %+v\nwant %+v", res.Rules[0], want)
			}
		})
	}
}

func TestParseRouters_ShortFormIsDefault(t *testing.T) {
	res := ParseContainers([]Container{{
		ID: "c1",
		Labels: map[string]string{
			"gateway.enable":        "true",
			"gateway.name":          "passport",
			"gateway.port":          "8085",
			"gateway.path_prefix":   "/api/auth/login",
			"gateway.auth.required": "false",
		},
	}}, testOpts())

	if len(res.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(res.Rules))
	}
	r := res.Rules[0]
	if r.PathPrefix != "/api/auth/login" || r.TargetName != "passport" {
		t.Errorf("rule: got %+v", r)
	}
	if r.Auth == nil || r.Auth.Required {
		t.Errorf("auth.required should be false, got %+v", r.Auth)
	}
}

func TestParseRouters_NamedRouters(t *testing.T) {
	res := ParseContainers([]Container{{
		ID: "c1",
		Labels: map[string]string{
			"gateway.enable":                           "true",
			"gateway.name":                             "passport",
			"gateway.port":                             "8085",
			"gateway.router.auth.path_prefix":          "/api/auth",
			"gateway.router.auth.auth.required":        "true",
			"gateway.router.auth.auth.roles":           "user,admin",
			"gateway.router.auth.auth.roles_all":       "mfa",
			"gateway.router.sessions.path_prefix":      "/api/admin/sessions",
			"gateway.router.sessions.strip_path":       "true",
			"gateway.router.sessions.methods":          "GET,POST",
			"gateway.router.sessions.rate_limit.rps":   "20",
			"gateway.router.sessions.rate_limit.burst": "40",
		},
	}}, testOpts())

	if len(res.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(res.Rules))
	}
	byPrefix := map[string]config.RoutingRule{}
	for _, r := range res.Rules {
		byPrefix[r.PathPrefix] = r
	}

	auth := byPrefix["/api/auth"]
	if auth.Auth == nil || !auth.Auth.Required {
		t.Errorf("auth rule required: got %+v", auth.Auth)
	}
	if len(auth.Auth.Roles) != 2 || auth.Auth.Roles[0] != "user" {
		t.Errorf("auth roles: got %v", auth.Auth.Roles)
	}
	if len(auth.Auth.RolesAll) != 1 || auth.Auth.RolesAll[0] != "mfa" {
		t.Errorf("auth roles_all: got %v", auth.Auth.RolesAll)
	}

	sessions := byPrefix["/api/admin/sessions"]
	if !sessions.StripPath {
		t.Errorf("strip_path should be true")
	}
	if len(sessions.Methods) != 2 || sessions.Methods[0] != "GET" {
		t.Errorf("methods: got %v", sessions.Methods)
	}
	if sessions.RateLimit == nil || sessions.RateLimit.RequestsPerSecond != 20 || sessions.RateLimit.Burst != 40 {
		t.Errorf("rate limit: got %+v", sessions.RateLimit)
	}
}

func TestParseRouters_MixedShortAndNamed(t *testing.T) {
	res := ParseContainers([]Container{{
		ID: "c1",
		Labels: map[string]string{
			"gateway.enable":                   "true",
			"gateway.name":                     "svc",
			"gateway.port":                     "9000",
			"gateway.path_prefix":              "/default",
			"gateway.router.extra.path_prefix": "/extra",
		},
	}}, testOpts())

	if len(res.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(res.Rules))
	}
}

func TestParseRouters_HostOnlyGetsRootPath(t *testing.T) {
	res := ParseContainers([]Container{{
		ID: "c1",
		Labels: map[string]string{
			"gateway.enable": "true",
			"gateway.name":   "svc",
			"gateway.port":   "9000",
			"gateway.host":   "svc.local",
		},
	}}, testOpts())

	if len(res.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(res.Rules))
	}
	if res.Rules[0].Host != "svc.local" || res.Rules[0].PathPrefix != "/" {
		t.Errorf("rule: got %+v", res.Rules[0])
	}
}

func TestParseRouters_NoHostNoPathSkipped(t *testing.T) {
	res := ParseContainers([]Container{{
		ID: "c1",
		Labels: map[string]string{
			"gateway.enable":        "true",
			"gateway.name":          "svc",
			"gateway.port":          "9000",
			"gateway.auth.required": "true",
		},
	}}, testOpts())

	if len(res.Targets) != 0 || len(res.Rules) != 0 {
		t.Fatalf("container without host/path must be skipped, got %+v", res)
	}
}

func TestParseRouters_AuthStripToken(t *testing.T) {
	res := ParseContainers([]Container{{
		ID: "c1",
		Labels: map[string]string{
			"gateway.enable":           "true",
			"gateway.name":             "svc",
			"gateway.port":             "9000",
			"gateway.path_prefix":      "/api",
			"gateway.auth.required":    "true",
			"gateway.auth.strip_token": "true",
		},
	}}, testOpts())

	if len(res.Rules) != 1 || res.Rules[0].Auth == nil {
		t.Fatalf("expected rule with auth, got %+v", res.Rules)
	}
	if res.Rules[0].Auth.StripToken == nil || !*res.Rules[0].Auth.StripToken {
		t.Errorf("strip_token should be true, got %+v", res.Rules[0].Auth.StripToken)
	}
}

func TestParseRouters_NamedDefaultOverridesShort(t *testing.T) {
	res := ParseContainers([]Container{{
		ID: "c1",
		Labels: map[string]string{
			"gateway.enable":                     "true",
			"gateway.name":                       "svc",
			"gateway.port":                       "9000",
			"gateway.path_prefix":                "/short",
			"gateway.router.default.path_prefix": "/named",
		},
	}}, testOpts())

	if len(res.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(res.Rules))
	}
	if res.Rules[0].PathPrefix != "/named" {
		t.Errorf("named default router must override short form: got %q", res.Rules[0].PathPrefix)
	}
}

// TestParseBool_AcceptedValues фиксирует документированный набор значений
// (true/1/yes, регистронезависимо); on намеренно не принимается.
func TestParseBool_AcceptedValues(t *testing.T) {
	for _, v := range []string{"true", "TRUE", "True", "1", "yes", "YES"} {
		if !parseBool(v) {
			t.Errorf("parseBool(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "false", "0", "no", "on", "garbage"} {
		if parseBool(v) {
			t.Errorf("parseBool(%q) = true, want false", v)
		}
	}
}

// TestParseContainers_DeterministicOrder проверяет, что порядок контейнеров от
// Docker не влияет на Result: targets сортируются по имени, rules — по host/path.
func TestParseContainers_DeterministicOrder(t *testing.T) {
	mk := func(order ...string) []Container {
		byName := map[string]Container{
			"a": {ID: "a", Labels: map[string]string{"gateway.enable": "true", "gateway.name": "a", "gateway.port": "9000", "gateway.path_prefix": "/a"}},
			"b": {ID: "b", Labels: map[string]string{"gateway.enable": "true", "gateway.name": "b", "gateway.port": "9001", "gateway.path_prefix": "/b"}},
			"c": {ID: "c", Labels: map[string]string{"gateway.enable": "true", "gateway.name": "c", "gateway.port": "9002", "gateway.path_prefix": "/c"}},
		}
		out := make([]Container, 0, len(order))
		for _, n := range order {
			out = append(out, byName[n])
		}
		return out
	}

	first, err := json.Marshal(ParseContainers(mk("a", "b", "c"), testOpts()))
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(ParseContainers(mk("c", "b", "a"), testOpts()))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("ParseContainers output depends on container order:\n%s\n%s", first, second)
	}
}
