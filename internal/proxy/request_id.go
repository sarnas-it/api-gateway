package proxy

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"net/http"
)

type bodyCapture struct {
	buf         []byte
	limit       int
	contentType string
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
	capture    *bodyCapture
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
}

// enableCapture включает запись тела ответа (с ограничением limit байт)
// и Content-Type ответа. Используется только когда хотя бы один вебхук
// запросил публикацию тела ответа (include_response_body).
func (rw *responseWriter) enableCapture(limit int) {
	if limit <= 0 {
		limit = defaultMaxResponseBodyBytes
	}
	rw.capture = &bodyCapture{limit: limit}
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
		if rw.capture != nil {
			rw.capture.contentType = rw.Header().Get("Content-Type")
		}
		rw.ResponseWriter.WriteHeader(code)
	}
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.WriteHeader(http.StatusOK)
	}
	if rw.capture != nil {
		if len(rw.capture.buf) < rw.capture.limit {
			need := rw.capture.limit - len(rw.capture.buf)
			if len(b) < need {
				need = len(b)
			}
			rw.capture.buf = append(rw.capture.buf, b[:need]...)
		}
	}
	return rw.ResponseWriter.Write(b)
}

// Unwrap отдаёт базовый ResponseWriter. Через него http.ResponseController
// доходит до Hijack (WebSocket/101) и SetReadDeadline/SetWriteDeadline
// (per-route таймауты), которые сама обёртка не реализует.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// Flush пробрасывает flush к базовому writer — нужно для SSE (text/event-stream),
// иначе потоковые ответы буферизуются и клиент не получает события.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// CapturedResponseBody возвращает захваченное тело ответа (nil, если сбор не
// включён или тело пустое).
func (rw *responseWriter) CapturedResponseBody() []byte {
	if rw.capture == nil || len(rw.capture.buf) == 0 {
		return nil
	}
	// Точный срез: не возвращаем переиспользуемую память вне контекста запроса.
	return bytes.Clone(rw.capture.buf)
}

// CapturedContentType возвращает Content-Type ответа.
func (rw *responseWriter) CapturedContentType() string {
	if rw.capture == nil {
		return ""
	}
	return rw.capture.contentType
}

// generateRequestID возвращает короткий ID для трассировки/логов.
// Не требует криптографической стойкости, поэтому используется быстрый
// math/rand/v2 (без syscall на каждый вызов) вместо crypto/rand.
func generateRequestID() string {
	return fmt.Sprintf("%016x", rand.Uint64())
}
