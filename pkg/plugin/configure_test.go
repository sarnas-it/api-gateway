package plugin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type configurableModule struct{ value string }

func (*configurableModule) Name() string { return "test.configurable" }
func (m *configurableModule) Configure(cfg map[string]string) error {
	m.value = cfg["value"]
	return nil
}

type plainModule struct{}

func (plainModule) Name() string { return "test.plain" }

func TestConfigure(t *testing.T) {
	m := &configurableModule{}
	Register(m)
	Register(plainModule{})

	if err := Configure(map[string]map[string]string{
		"test.configurable": {"value": "ok"},
	}); err != nil {
		t.Fatal(err)
	}
	if m.value != "ok" {
		t.Fatalf("value = %q", m.value)
	}

	if err := Configure(map[string]map[string]string{"missing": {}}); err == nil {
		t.Fatal("unknown module must fail")
	}
	if err := Configure(map[string]map[string]string{"test.plain": {}}); err == nil {
		t.Fatal("non-configurable module must fail")
	}
}

type middlewareModule struct{ tag string }

func (m *middlewareModule) Name() string { return "test.middleware." + m.tag }
func (m *middlewareModule) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Chain", m.tag)
		next.ServeHTTP(w, r)
	})
}

func TestHTTPMiddlewaresOrder(t *testing.T) {
	first, second := &middlewareModule{tag: "first"}, &middlewareModule{tag: "second"}
	Register(first)
	Register(second)

	mws := HTTPMiddlewares()
	var tags []string
	for _, mw := range mws {
		tags = append(tags, mw.Name())
	}
	if len(tags) < 2 || tags[len(tags)-2] != first.Name() || tags[len(tags)-1] != second.Name() {
		t.Fatalf("middlewares = %v", tags)
	}

	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i].Wrap(h)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	got := rec.Header().Values("X-Chain")
	if len(got) < 2 || got[len(got)-2] != "first" || got[len(got)-1] != "second" {
		t.Fatalf("chain order = %v", got)
	}
}
