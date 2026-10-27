package proxy

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/acme/autocert"

	"code.sarnas.ru/api-gateway/internal/config"
)

func TestBuildCertManager_Staging(t *testing.T) {
	m := buildCertManager(&config.TLSConfig{
		Domains:  []string{"api.example.com"},
		Email:    "admin@example.com",
		CacheDir: "/var/lib/api-gateway/certs",
		Staging:  true,
	})

	if m.Client == nil || m.Client.DirectoryURL != letsEncryptStagingDirectory {
		t.Fatalf("staging DirectoryURL = %v, want %q", m.Client, letsEncryptStagingDirectory)
	}
	cache, ok := m.Cache.(autocert.DirCache)
	if !ok {
		t.Fatalf("cache type = %T, want autocert.DirCache", m.Cache)
	}
	if want := filepath.Join("/var/lib/api-gateway/certs", "staging"); string(cache) != want {
		t.Errorf("staging cache dir = %q, want %q", string(cache), want)
	}
}

func TestBuildCertManager_Production(t *testing.T) {
	m := buildCertManager(&config.TLSConfig{
		Domains:  []string{"api.example.com"},
		Email:    "admin@example.com",
		CacheDir: "/var/lib/api-gateway/certs",
	})

	if m.Client != nil && m.Client.DirectoryURL != "" {
		t.Errorf("production must use autocert's default directory, got %q", m.Client.DirectoryURL)
	}
	cache, ok := m.Cache.(autocert.DirCache)
	if !ok {
		t.Fatalf("cache type = %T, want autocert.DirCache", m.Cache)
	}
	if string(cache) != "/var/lib/api-gateway/certs" {
		t.Errorf("production cache dir = %q, want %q", string(cache), "/var/lib/api-gateway/certs")
	}
}

func TestBuildCertManager_DirectoryURLOverride(t *testing.T) {
	m := buildCertManager(&config.TLSConfig{
		Domains:      []string{"api.example.com"},
		Email:        "admin@example.com",
		CacheDir:     "/var/lib/api-gateway/certs",
		Staging:      true,
		DirectoryURL: "https://acme.internal/directory",
	})

	if m.Client == nil || m.Client.DirectoryURL != "https://acme.internal/directory" {
		t.Fatalf("DirectoryURL override = %v, want %q", m.Client, "https://acme.internal/directory")
	}
	cache, ok := m.Cache.(autocert.DirCache)
	if !ok {
		t.Fatalf("cache type = %T, want autocert.DirCache", m.Cache)
	}
	if want := filepath.Join("/var/lib/api-gateway/certs", "staging"); string(cache) != want {
		t.Errorf("staging cache dir = %q, want %q", string(cache), want)
	}
}

func TestMetricsOverHTTPHandler(t *testing.T) {
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("metrics"))
	})
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMovedPermanently)
	})
	h := metricsOverHTTPHandler(main, redirect)

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/metrics", http.StatusOK},
		{"/", http.StatusMovedPermanently},
		{"/api/blog", http.StatusMovedPermanently},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.path, rec.Code, tc.want)
		}
	}
}
