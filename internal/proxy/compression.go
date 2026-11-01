package proxy

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// shortcut: только gzip, без zstd/brotli; gzip закрывает основной сценарий и не тянет зависимость; добавить zstd, если появится требование к размеру трафика.
// gzipPool переиспользует writer'ы уровня по умолчанию: gzip.NewWriterLevel
// выделяет заметные таблицы, а сжатие стоит на каждом ответе.
var gzipPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
		return w
	},
}

// compressionMiddleware сжимает ответы gzip, если клиент их принимает.
// Конфиг читается на каждый запрос, поэтому SIGHUP может включать/выключать
// сжатие без рестарта.
func compressionMiddleware(mp *MultiProxy) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cfg := mp.config.Load().Compression
			if !cfg.Enabled || r.Method == http.MethodHead || isUpgradeRequest(r) || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
				next.ServeHTTP(w, r)
				return
			}
			cw := &compressResponseWriter{
				ResponseWriter: w,
				level:          cfg.Level,
				minSize:        cfg.MinSize,
				status:         http.StatusOK,
			}
			next.ServeHTTP(cw, r)
			cw.Close()
		})
	}
}

func isUpgradeRequest(r *http.Request) bool {
	return r.Header.Get("Upgrade") != "" || strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(fields[0]), "gzip") {
			continue
		}
		for _, param := range fields[1:] {
			param = strings.TrimSpace(param)
			if strings.HasPrefix(param, "q=") {
				q, err := strconv.ParseFloat(strings.TrimPrefix(param, "q="), 64)
				if err == nil && q <= 0 {
					return false
				}
			}
		}
		return true
	}
	return false
}

type compressResponseWriter struct {
	http.ResponseWriter
	level      int
	minSize    int
	status     int
	wrote      bool
	decided    bool
	compress   bool
	gz         *gzip.Writer
	pooledGzip bool
}

func (cw *compressResponseWriter) WriteHeader(code int) {
	if cw.wrote {
		return
	}
	cw.wrote = true
	cw.status = code
	cw.decide()
	cw.ResponseWriter.WriteHeader(code)
}

func (cw *compressResponseWriter) decide() {
	if cw.decided {
		return
	}
	cw.decided = true
	if cw.status < 200 || cw.status == http.StatusNoContent || cw.status == http.StatusNotModified {
		return
	}
	header := cw.Header()
	if header.Get("Content-Encoding") != "" {
		return
	}
	if !compressibleType(header.Get("Content-Type")) {
		return
	}
	if raw := header.Get("Content-Length"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n < cw.minSize {
			return
		}
	}
	cw.compress = true
	header.Set("Content-Encoding", "gzip")
	header.Del("Content-Length")
	addVary(header, "Accept-Encoding")
}

func (cw *compressResponseWriter) Write(p []byte) (int, error) {
	if !cw.wrote {
		cw.WriteHeader(http.StatusOK)
	}
	if !cw.compress {
		return cw.ResponseWriter.Write(p)
	}
	if cw.gz == nil {
		cw.gz = cw.newGzip()
	}
	return cw.gz.Write(p)
}

func (cw *compressResponseWriter) newGzip() *gzip.Writer {
	if cw.level == gzip.DefaultCompression {
		gz := gzipPool.Get().(*gzip.Writer)
		gz.Reset(cw.ResponseWriter)
		cw.pooledGzip = true
		return gz
	}
	gz, err := gzip.NewWriterLevel(cw.ResponseWriter, cw.level)
	if err != nil {
		// Валидация конфига не пропускает такой уровень; на всякий случай
		// используем уровень по умолчанию, а не рвём запрос.
		gz = gzip.NewWriter(cw.ResponseWriter)
	}
	return gz
}

func (cw *compressResponseWriter) Flush() {
	if !cw.wrote {
		cw.WriteHeader(http.StatusOK)
	}
	if cw.gz != nil {
		_ = cw.gz.Flush()
	}
	if f, ok := cw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (cw *compressResponseWriter) Close() {
	// Заголовок Content-Encoding уже отправлен, поэтому даже пустой ответ
	// должен быть валидным gzip-потоком.
	if cw.compress && cw.gz == nil && cw.wrote {
		cw.gz = cw.newGzip()
	}
	if cw.gz != nil {
		_ = cw.gz.Close()
		if cw.pooledGzip {
			gzipPool.Put(cw.gz)
			cw.gz = nil
			cw.pooledGzip = false
		}
	}
}

// Unwrap нужен http.ResponseController (Hijack/дедлайны), как и у responseWriter.
func (cw *compressResponseWriter) Unwrap() http.ResponseWriter {
	return cw.ResponseWriter
}

func compressibleType(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if ct == "text/event-stream" {
		return false
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/json", "application/javascript", "application/xml",
		"application/xhtml+xml", "image/svg+xml":
		return true
	}
	return strings.HasSuffix(ct, "+json") || strings.HasSuffix(ct, "+xml")
}

func addVary(header http.Header, value string) {
	for _, existing := range header.Values("Vary") {
		for _, token := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(token), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}
