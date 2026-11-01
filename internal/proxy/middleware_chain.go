package proxy

import (
	"bytes"
	"code.sarnas.ru/api-gateway/internal/config"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// contextKey тип для ключей контекста
type contextKey string

const (
	ctxKeyRequestIDs          contextKey = "request_ids"
	ctxKeyTarget              contextKey = "target"
	ctxKeyRule                contextKey = "rule"
	ctxKeyTargetProxy         contextKey = "target_proxy"
	ctxKeyRequestBody         contextKey = "request_body"
	ctxKeyResponseBody        contextKey = "response_body"
	ctxKeyResponseContentType contextKey = "response_content_type"
)

// requestIDs хранит request_id и trace_id одним значением контекста —
// одна аллокация вместо двух отдельных context.WithValue.
type requestIDs struct {
	reqID   string
	traceID string
}

func requestIDsFromContext(ctx context.Context) (reqID, traceID string) {
	if ids, ok := ctx.Value(ctxKeyRequestIDs).(requestIDs); ok {
		return ids.reqID, ids.traceID
	}
	return "", ""
}

// ──────── Panic Recovery ────────

func recoveryMiddleware(logger *zap.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("Panic recovered",
						zap.Any("panic", rec),
						zap.String("method", r.Method),
						zap.String("path", r.URL.Path),
						zap.String("remote_addr", r.RemoteAddr),
						zap.Stack("stack"),
					)
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ──────── Request ID ────────

func requestIDMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get("X-Request-ID")
			if reqID == "" {
				reqID = generateRequestID()
			}
			r.Header.Set("X-Request-ID", reqID)
			w.Header().Set("X-Request-ID", reqID)

			traceID := r.Header.Get("X-Trace-ID")
			if traceID == "" {
				traceID = reqID
			}
			r.Header.Set("X-Trace-ID", traceID)

			ctx := context.WithValue(r.Context(), ctxKeyRequestIDs, requestIDs{reqID: reqID, traceID: traceID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ──────── OpenTelemetry tracing ────────

func tracingMiddleware(tp *TracerProvider) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tp == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Извлекаем propagation context из заголовков (traceparent)
			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

			spanName := fmt.Sprintf("%s %s", r.Method, r.URL.Path)
			ctx, span := tp.Tracer().Start(ctx, spanName)
			defer span.End()

			span.SetAttributes(
				attribute.String("http.method", r.Method),
				attribute.String("http.url", r.URL.String()),
				attribute.String("http.host", r.Host),
				attribute.String("http.user_agent", r.UserAgent()),
			)

			if reqID, _ := requestIDsFromContext(ctx); reqID != "" {
				span.SetAttributes(attribute.String("request_id", reqID))
			}

			// Пропагируем trace context в заголовки upstream
			otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(r.Header))

			next.ServeHTTP(w, r.WithContext(ctx))

			// Записываем статус ответа после завершения
			if rw, ok := w.(*responseWriter); ok {
				span.SetAttributes(attribute.Int("http.status_code", rw.statusCode))
			}
		})
	}
}

// ──────── Metrics: active requests ────────

func activeRequestMetricsMiddleware(metrics *Metrics) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			metrics.IncActiveRequests()
			defer metrics.DecActiveRequests()
			next.ServeHTTP(w, r)
		})
	}
}

// ──────── Health probes ────────

var (
	GitSHA     string
	BuildTime  string
	BuildRunID string
)

// ──────── Metrics endpoint ────────

func metricsEndpointMiddleware(metrics *Metrics, allowedIPs []string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/metrics" {
				next.ServeHTTP(w, r)
				return
			}
			if len(allowedIPs) > 0 && !ipAllowed(getClientIP(r), allowedIPs) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			metrics.Handler().ServeHTTP(w, r)
		})
	}
}

// ipAllowed сообщает, разрешён ли clientIP: точное совпадение или вхождение в
// один из CIDR-диапазонов (например, 172.30.0.0/24).
func ipAllowed(clientIP string, allowed []string) bool {
	ip := net.ParseIP(clientIP)
	for _, a := range allowed {
		if a == clientIP {
			return true
		}
		if ip != nil {
			if _, cidr, err := net.ParseCIDR(a); err == nil && cidr.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// ──────── Global rate limiter ────────

func globalRateLimitMiddleware(limiter *rate.Limiter, metrics *Metrics) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limiter != nil && !limiter.Allow() {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "Too many requests", http.StatusTooManyRequests)
				metrics.IncRateLimitDenial()
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ──────── SPA static ────────

func spaStaticMiddleware(mp *MultiProxy) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if mp.config.Load().Static != nil {
				if handled := mp.serveStatic(w, r); handled {
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ──────── CORS preflight ────────

func corsPreflightMiddleware(mp *MultiProxy, metrics *Metrics) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				mp.setCORSHeaders(w.Header(), r)
				w.WriteHeader(http.StatusOK)
				metrics.IncRequests(r.Method, r.URL.Path, "200")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ──────── Route matching + health + rate limit + auth + proxy ────────

// proxyHandler финальный handler для проксирования запроса
// effectiveBodyLimit — лимит тела запроса: per-route перекрывает общий.
func effectiveBodyLimit(serverLimit int64, rule *config.RoutingRule) int64 {
	if rule == nil {
		return serverLimit
	}
	return rule.EffectiveMaxRequestBodySize(serverLimit)
}

func (mp *MultiProxy) proxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()
		reqID, traceID := requestIDsFromContext(r.Context())

		target, rule := mp.config.Load().FindTargetForPath(r.URL.Path, r.Method, r.Host)

		rw := newResponseWriter(w)
		// Захват тела ответа нужен только когда хотя бы один on_response вебхук
		// запросил include_response_body (иначе — ноль накладных расходов).
		if mp.publisher != nil && mp.publisher.CaptureResponseBodies() {
			rw.enableCapture(defaultMaxResponseBodyBytes)
		}

		// Route
		if target == nil {
			mp.setCORSHeaders(rw.Header(), r)
			http.Error(rw, "No route found", http.StatusNotFound)
			mp.metrics.IncRequests(r.Method, r.URL.Path, "404")
			mp.logAccess(reqID, traceID, r, 404, 0, nil)
			return
		}

		// Health + взвешенный выбор таргета из пула роута. Правила,
		// совпадающие по (host, path_prefix, methods), делят один пул,
		// поэтому несколько статических таргетов балансируются.
		cbEnabled := mp.config.Load().CircuitBreaker
		rc := mp.findRouteConfig(rule)
		var targetProxy *TargetProxy
		if rc != nil {
			targetProxy = rc.pickTarget(cbEnabled)
		} else {
			mp.mu.RLock()
			targetProxy = mp.targets[target.Name]
			mp.mu.RUnlock()
			if targetProxy != nil && !targetProxy.acquire(cbEnabled) {
				targetProxy = nil
			}
		}

		if targetProxy == nil {
			mp.setCORSHeaders(rw.Header(), r)
			http.Error(rw, "Target unavailable", http.StatusServiceUnavailable)
			mp.metrics.IncRequests(r.Method, r.URL.Path, "503")
			mp.logAccess(reqID, traceID, r, 503, 0, target)
			return
		}

		// Держим выбранный таргет в синхроне для modifyRequest/logAccess.
		target = targetProxy.config

		// Per-route rate limit
		if rc != nil && rc.RateLimit != nil {
			clientIP := getClientIP(r)
			if !rc.RateLimit.Allow(clientIP) {
				mp.setCORSHeaders(rw.Header(), r)
				http.Error(rw, "Too many requests", http.StatusTooManyRequests)
				mp.metrics.IncRateLimitDenial()
				mp.metrics.IncRequests(r.Method, r.URL.Path, "429")
				mp.logAccess(reqID, traceID, r, 429, 0, target)
				return
			}
		}

		// Strip path
		remainingPath := r.URL.Path
		if rule != nil && rule.StripPath {
			remainingPath = strings.TrimPrefix(r.URL.Path, rule.PathPrefix)
			if !strings.HasPrefix(remainingPath, "/") {
				remainingPath = "/" + remainingPath
			}
		}

		// Per-route дедлайны: общий server.read_timeout короткий (защита от
		// медленных заголовков), а большие тела (WebDAV PUT) живут дольше.
		if rule != nil && (rule.ReadTimeout > 0 || rule.WriteTimeout > 0) {
			rc := http.NewResponseController(rw)
			if rule.ReadTimeout > 0 {
				if err := rc.SetReadDeadline(time.Now().Add(rule.ReadTimeout)); err != nil {
					mp.logger.Warn("failed to set per-route read deadline", zap.Error(err))
				}
			}
			if rule.WriteTimeout > 0 {
				if err := rc.SetWriteDeadline(time.Now().Add(rule.WriteTimeout)); err != nil {
					mp.logger.Warn("failed to set per-route write deadline", zap.Error(err))
				}
			}
		}

		if rule != nil && rule.PreserveHost {
			r.Header.Set(internalHostHeader, r.Host)
			if r.Header.Get("X-Forwarded-Host") == "" {
				r.Header.Set("X-Forwarded-Host", r.Host)
			}
		}

		// Быстрый отказ по заявленному размеру: обрезать тело молча нельзя
		// (бэкенд получил бы битый файл), клиент должен увидеть 413.
		if limit := effectiveBodyLimit(mp.config.Load().Server.EffectiveMaxRequestBodySize(), rule); limit > 0 &&
			r.ContentLength > limit {
			mp.setCORSHeaders(rw.Header(), r)
			http.Error(rw, "request body too large", http.StatusRequestEntityTooLarge)
			mp.metrics.IncRequests(r.Method, r.URL.Path, "413")
			mp.logAccess(reqID, traceID, r, 413, 0, target)
			return
		}

		// JWT + headers
		if err := mp.modifyRequest(r, target, rule); err != nil {
			mp.setCORSHeaders(rw.Header(), r)
			http.Error(rw, err.Error(), http.StatusUnauthorized)
			mp.metrics.IncRequests(r.Method, r.URL.Path, "401")
			mp.logAccess(reqID, traceID, r, 401, 0, target)
			return
		}

		// Тело читается в память только когда маршрут этого требует (аудит-
		// вебхуки). Для WebDAV это неприемлемо: 50 МиБ PUT буферизовались бы
		// и обрезались по лимиту. buffer_request_body: false → поток идёт
		// напрямую в бэкенд.
		if r.Body != nil && (r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH" || r.Method == "QUERY") &&
			(rule == nil || rule.BufferRequestBodyEnabled()) {
			limit := mp.config.Load().Server.EffectiveMaxRequestBodySize()
			if rule != nil {
				limit = rule.EffectiveMaxRequestBodySize(limit)
			}
			bodyBytes, err := readBodyLimited(r.Body, limit)
			r.Body.Close()
			if err != nil {
				status := http.StatusBadRequest
				message := "failed to read request body"
				if errors.Is(err, errBodyTooLarge) {
					status = http.StatusRequestEntityTooLarge
					message = "request body too large"
				}
				mp.setCORSHeaders(rw.Header(), r)
				http.Error(rw, message, status)
				mp.metrics.IncRequests(r.Method, r.URL.Path, strconv.Itoa(status))
				mp.logAccess(reqID, traceID, r, status, time.Since(startTime), target)
				return
			}
			if len(bodyBytes) > 0 && len(bodyBytes) < 65536 {
				r = r.WithContext(context.WithValue(r.Context(), ctxKeyRequestBody, bodyBytes))
			}
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}

		// Publish on_request webhooks
		if mp.publisher != nil {
			mp.publisher.PublishOnRequest(r.Context(), r)
		}

		if mp.config.Load().Logging.AccessLog {
			mp.logger.Debug("Proxying request",
				zap.String("request_id", reqID),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.String("target", target.Name),
				zap.String("remaining_path", remainingPath),
			)
		}

		mp.proxyRequest(rw, r, targetProxy, remainingPath, effectiveBodyLimit(mp.config.Load().Server.EffectiveMaxRequestBodySize(), rule))

		// Передаём захваченное тело ответа издателю (on_response webhooks).
		if body := rw.CapturedResponseBody(); body != nil {
			r = r.WithContext(context.WithValue(r.Context(), ctxKeyResponseBody, body))
			r = r.WithContext(context.WithValue(r.Context(), ctxKeyResponseContentType, rw.CapturedContentType()))
		}

		mp.metrics.IncRequests(r.Method, r.URL.Path, strconv.Itoa(rw.statusCode))
		mp.metrics.ObserveDuration(r.Method, r.URL.Path, time.Since(startTime))

		// Publish on_response webhooks
		if mp.publisher != nil {
			mp.publisher.PublishOnResponse(r.Context(), r, rw.statusCode)
		}

		mp.logAccess(reqID, traceID, r, rw.statusCode, time.Since(startTime), target)
	})
}

// errBodyTooLarge сообщает, что тело запроса превысило разрешённый лимит.
var errBodyTooLarge = errors.New("request body too large")

// readBodyLimited читает тело не более чем limit байт. limit <= 0 — без лимита.
// Читает limit+1 байт, чтобы отличить «ровно по лимиту» от «больше лимита»,
// и возвращает errBodyTooLarge вместо молчаливого обрезания.
func readBodyLimited(body io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(body)
	}
	b, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errBodyTooLarge
	}
	return b, nil
}
