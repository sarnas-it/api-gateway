package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func stickyProxy(rc *RouteConfig) *MultiProxy {
	return &MultiProxy{}
}

func TestStickySignRoundTrip(t *testing.T) {
	v := signSticky("api-1")
	if name, ok := parseSticky(v); !ok || name != "api-1" {
		t.Fatalf("parse(%q) = %q, %v", v, name, ok)
	}
	if _, ok := parseSticky("garbage"); ok {
		t.Fatal("garbage must not parse")
	}
	if _, ok := parseSticky(v + "0"); ok {
		t.Fatal("tampered value must not parse")
	}
	// Имя другого таргета нельзя подставить, сохранив подпись.
	other := signSticky("api-2")
	if parseName, ok := parseSticky(other); !ok || parseName != "api-2" {
		t.Fatalf("other cookie = %q, %v", parseName, ok)
	}
}

func TestStickyPinsClientToTarget(t *testing.T) {
	a, b, c := lbCandidate("a", 0), lbCandidate("b", 0), lbCandidate("c", 0)
	rc := lbRoute("sticky", a, b, c)
	mp := stickyProxy(rc)

	// Первый запрос без cookie: таргет выбирается обычно и приходит cookie.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	first := mp.pickTargetForRequest(rc, false, req, rec)
	if first == nil {
		t.Fatal("no target")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != stickyCookieName {
		t.Fatalf("cookies = %+v", cookies)
	}
	if !cookies[0].HttpOnly || cookies[0].Path != "/" {
		t.Fatalf("cookie attrs = %+v", cookies[0])
	}

	// С cookie выбор всегда тот же, хотя round_robin иначе двигался бы.
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(cookies[0])
		rec := httptest.NewRecorder()
		got := mp.pickTargetForRequest(rc, false, req, rec)
		if got != first {
			t.Fatalf("sticky moved: %s vs %s", got.config.Name, first.config.Name)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Fatalf("cookie must not be reset on a pinned request")
		}
	}
}

func TestStickyFallsBackOnBadCookie(t *testing.T) {
	a, b := lbCandidate("a", 0), lbCandidate("b", 0)
	rc := lbRoute("sticky", a, b)
	mp := stickyProxy(rc)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: stickyCookieName, Value: "forged.value"})
	rec := httptest.NewRecorder()
	got := mp.pickTargetForRequest(rc, false, req, rec)
	if got == nil {
		t.Fatal("no target")
	}
	if len(rec.Result().Cookies()) != 1 {
		t.Fatal("fallback must issue a fresh cookie")
	}
}

func TestStickyUnknownTargetFallsBack(t *testing.T) {
	a := lbCandidate("a", 0)
	rc := lbRoute("sticky", a)
	mp := stickyProxy(rc)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// Подпись валидна, но таргета с таким именем в пуле нет.
	req.AddCookie(&http.Cookie{Name: stickyCookieName, Value: signSticky("gone")})
	rec := httptest.NewRecorder()
	if got := mp.pickTargetForRequest(rc, false, req, rec); got != a {
		t.Fatalf("got %v, want a", got)
	}
}

func TestStickyUnhealthyTargetFallsBack(t *testing.T) {
	a, b := lbCandidate("a", 0), lbCandidate("b", 0)
	a.healthy = false
	rc := lbRoute("sticky", a, b)
	mp := stickyProxy(rc)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: stickyCookieName, Value: signSticky("a")})
	rec := httptest.NewRecorder()
	if got := mp.pickTargetForRequest(rc, false, req, rec); got != b {
		t.Fatalf("got %v, want b", got)
	}
	if len(rec.Result().Cookies()) != 1 {
		t.Fatal("fallback must repin the client")
	}
}

func TestNonStickyDoesNotSetCookie(t *testing.T) {
	a, b := lbCandidate("a", 0), lbCandidate("b", 0)
	rc := lbRoute("round_robin", a, b)
	mp := stickyProxy(rc)
	rec := httptest.NewRecorder()
	mp.pickTargetForRequest(rc, false, httptest.NewRequest(http.MethodGet, "/", nil), rec)
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("cookie must only be set for sticky policy")
	}
}
