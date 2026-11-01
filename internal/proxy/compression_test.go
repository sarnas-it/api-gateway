package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"code.sarnas.ru/api-gateway/internal/config"
)

func compressionHandler(cfg config.CompressionConfig, next http.Handler) http.Handler {
	mp := &MultiProxy{}
	mp.config.Store(&config.Config{Compression: cfg})
	return compressionMiddleware(mp)(next)
}

func compressionRequest(t *testing.T, h http.Handler, accept, method string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/", nil)
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCompressionGzip(t *testing.T) {
	body := []byte(`{"hello":"world","n":42}`)
	h := compressionHandler(config.CompressionConfig{Enabled: true, Level: 5, MinSize: 1},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "27")
			_, _ = w.Write(body)
		}))

	rec := compressionRequest(t, h, "gzip", http.MethodGet, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", rec.Header().Get("Vary"))
	}
	if rec.Header().Get("Content-Length") != "" {
		t.Fatalf("Content-Length must be removed when compressing, got %q", rec.Header().Get("Content-Length"))
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("decoded body = %q, want %q", got, body)
	}
}

func TestCompressionSkippedWithoutAcceptEncoding(t *testing.T) {
	h := compressionHandler(config.CompressionConfig{Enabled: true, Level: 5, MinSize: 1},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("hello"))
		}))

	rec := compressionRequest(t, h, "", http.MethodGet, nil)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty", got)
	}
	if rec.Body.String() != "hello" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestCompressionSkippedForEventStream(t *testing.T) {
	h := compressionHandler(config.CompressionConfig{Enabled: true, Level: 5, MinSize: 1},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: 1\n\n"))
		}))

	rec := compressionRequest(t, h, "gzip", http.MethodGet, nil)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("SSE must not be compressed, got %q", got)
	}
}

func TestCompressionSkippedForSmallKnownBody(t *testing.T) {
	h := compressionHandler(config.CompressionConfig{Enabled: true, Level: 5, MinSize: 1024},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", "5")
			_, _ = w.Write([]byte("hello"))
		}))

	rec := compressionRequest(t, h, "gzip", http.MethodGet, nil)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("small body must not be compressed, got %q", got)
	}
}

func TestCompressionSkippedForAlreadyEncoded(t *testing.T) {
	h := compressionHandler(config.CompressionConfig{Enabled: true, Level: 5, MinSize: 1},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "br")
			_, _ = w.Write([]byte("encoded"))
		}))

	rec := compressionRequest(t, h, "gzip", http.MethodGet, nil)
	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("existing encoding must be preserved, got %q", got)
	}
}

func TestCompressionSkippedWhenGzipDisabledByQ(t *testing.T) {
	h := compressionHandler(config.CompressionConfig{Enabled: true, Level: 5, MinSize: 1},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("hello"))
		}))

	rec := compressionRequest(t, h, "gzip;q=0", http.MethodGet, nil)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("gzip;q=0 must disable compression, got %q", got)
	}
}
