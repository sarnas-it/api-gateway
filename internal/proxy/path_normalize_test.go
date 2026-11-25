package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"code.sarnas.ru/api-gateway/internal/config"
)

// TestNormalizeRequestPath — таблица кодировок и форм traversal.
func TestNormalizeRequestPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // ожидаемый r.URL.Path после вызова
		raw  string // ожидаемый r.URL.RawPath после вызова
		err  bool
	}{
		{name: "обычный путь не меняется", in: "/api/v1/items", want: "/api/v1/items"},
		{name: "корень", in: "/", want: "/"},
		{name: "завершающий слэш сохраняется", in: "/dav/ws/", want: "/dav/ws/"},
		{name: "двойные слэши убираются", in: "/dav//ws/file.bin", want: "/dav/ws/file.bin"},
		{name: "двойные слэши и trailing slash", in: "/dav//ws/", want: "/dav/ws/"},
		{name: "dot-сегмент убирается", in: "/dav/./ws/file.bin", want: "/dav/ws/file.bin"},

		{name: "encoded traversal", in: "/%2e%2e/%2e%2e/etc/passwd", err: true},
		{name: "encoded traversal верхний регистр", in: "/%2E%2E/etc/passwd", err: true},
		{name: "double-encoded traversal", in: "/%252e%252e/%252e%252e/etc/passwd", err: true},
		{name: "matrix traversal", in: "/..;/admin", err: true},
		{name: "traversal с encoded слэшем", in: "/..%2fadmin", err: true},
		{name: "encoded traversal с encoded слэшем", in: "/%2e%2e%2fadmin", err: true},
		{name: "backslash traversal", in: "/%5c..%5cetc%5cpasswd", err: true},
		{name: "traversal в середине", in: "/foo/%2e%2e/bar", err: true},
		{name: "traversal в конце", in: "/foo/..", err: true},
		{name: "NUL в пути", in: "/foo%00bar", err: true},
		{name: "невалидный UTF-8", in: "/%ff", err: true},

		{name: "encoded dot безопасен и нормализуется", in: "/foo/%2e/bar", want: "/foo/bar", raw: ""},
		{name: "encoded слэш не остаётся закодированным", in: "/files/a%2Fb", want: "/files/a/b", raw: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.in, nil)
			err := normalizeRequestPath(r)
			if tc.err {
				if err == nil {
					t.Fatalf("path %q accepted, want error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("path %q rejected: %v", tc.in, err)
			}
			if r.URL.Path != tc.want {
				t.Fatalf("path %q normalized to %q, want %q", tc.in, r.URL.Path, tc.want)
			}
			if r.URL.RawPath != tc.raw {
				t.Fatalf("path %q RawPath = %q, want %q", tc.in, r.URL.RawPath, tc.raw)
			}
		})
	}
}

// TestProxyRejectsTraversalEncodings: traversal во всех формах — 400 с
// CORS-заголовками, бэкенд не вызывается.
func TestProxyRejectsTraversalEncodings(t *testing.T) {
	f := newForwardedFixture(t, config.RoutingRule{TargetName: "backend"}, nil)

	for _, target := range []string{
		"/%2e%2e/%2e%2e/etc/passwd",
		"/%252e%252e/%252e%252e/etc/passwd",
		"/..;/admin",
		"/..%2fadmin",
		"/%2e%2e%2fadmin",
		"/%5c..",
	} {
		t.Run(target, func(t *testing.T) {
			r := newForwardedRequest(t, target)
			r.Header.Set("Origin", "https://app.example")
			rec := f.do(t, r)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
				t.Fatalf("CORS header = %q, want *", got)
			}
		})
	}

	if hits := f.hits.Load(); hits != 0 {
		t.Fatalf("backend called %d times, want 0 for rejected paths", hits)
	}
}

// TestProxyNormalizesSafePath: подозрительный, но безопасный путь
// проксируется нормализованным; бэкенд не получает ни "//", ни "/./".
func TestProxyNormalizesSafePath(t *testing.T) {
	f := newForwardedFixture(t, config.RoutingRule{TargetName: "backend"}, nil)

	rec := f.do(t, newForwardedRequest(t, "/dav//ws/./file.bin"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got := <-f.seen
	if got.path != "/dav/ws/file.bin" {
		t.Fatalf("backend path = %q, want /dav/ws/file.bin", got.path)
	}
	if got.raw != "/dav/ws/file.bin" {
		t.Fatalf("backend RequestURI = %q, want normalized path", got.raw)
	}
}

// TestProxyKeepsNormalPathUntouched: обычный путь (в том числе WebDAV с
// завершающим слэшем) доходит до бэкенда без изменений.
func TestProxyKeepsNormalPathUntouched(t *testing.T) {
	f := newForwardedFixture(t, config.RoutingRule{TargetName: "backend", PreserveHost: true}, nil)

	for _, target := range []string{"/dav/ws/", "/files/my%20file.txt", "/api/v1/items"} {
		r := newForwardedRequest(t, target)
		want := r.URL.EscapedPath()
		if rec := f.do(t, r); rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", target, rec.Code)
		}
		got := <-f.seen
		if got.raw != want {
			t.Fatalf("%s: backend RequestURI = %q, want %q", target, got.raw, want)
		}
	}
}

// TestProxyNormalizesPathBeforeRegexMatch: нормализация идёт до матчинга,
// поэтому path_regex видит очищенный путь, а traversal до матчинга не доходит.
func TestProxyNormalizesPathBeforeRegexMatch(t *testing.T) {
	f := newForwardedFixture(t, config.RoutingRule{
		PathRegex:  `^/api/v1/items$`,
		TargetName: "backend",
	}, nil)

	rec := f.do(t, newForwardedRequest(t, "/api//v1/items"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := <-f.seen; got.path != "/api/v1/items" {
		t.Fatalf("backend path = %q, want /api/v1/items", got.path)
	}

	// traversal, который после нормализации попал бы в regex-маршрут,
	// отклоняется до матчинга и не уходит в бэкенд.
	rec = f.do(t, newForwardedRequest(t, "/api/v1/x/../items"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("traversal status = %d, want 400", rec.Code)
	}
	if hits := f.hits.Load(); hits != 1 {
		t.Fatalf("backend called %d times, want 1", hits)
	}
}

// TestProxyRejectsTraversalBeforePrefixMatch: "/public/../admin" не должен
// маршрутизироваться по правилу "/public" и уходить в бэкенд.
func TestProxyRejectsTraversalBeforePrefixMatch(t *testing.T) {
	f := newForwardedFixture(t, config.RoutingRule{PathPrefix: "/public", TargetName: "backend"}, nil)

	rec := f.do(t, newForwardedRequest(t, "/public/../admin/secret"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if hits := f.hits.Load(); hits != 0 {
		t.Fatalf("backend called %d times, want 0", hits)
	}
}
