package proxy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"golang.org/x/time/rate"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/jwtutil"
	"github.com/basili4-1982/api-gateway/internal/permissions"
	"github.com/golang-jwt/jwt/v5"
)

type RouteConfig struct {
	Rule      *config.RoutingRule
	Target    *config.TargetConfig
	RateLimit *IPRateLimiter
}

var corsHeaders = map[string]string{
	"Access-Control-Allow-Origin":      "", // Будет заполняться динамически
	"Access-Control-Allow-Methods":     "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD",
	"Access-Control-Allow-Headers":     "Origin, Content-Type, Accept, Authorization, X-Request-ID, X-User-ID, X-User-Email, X-User-Roles",
	"Access-Control-Expose-Headers":    "X-User-ID, X-User-Email, X-User-Roles, X-Proxy, X-Request-ID, Authorization",
	"Access-Control-Allow-Credentials": "true",
	"Access-Control-Max-Age":           "86400",
}

type circuitState int

const (
	stateClosed   circuitState = iota //正常工作, запросы проходят
	stateOpen                         // цепь разомкнута, запросы падают с 503
	stateHalfOpen                     // пробный запрос
)

// TargetProxy представляет прокси для конкретного таргета
type TargetProxy struct {
	config      *config.TargetConfig
	targetURL   *url.URL
	healthCheck *HealthChecker
	mu          sync.RWMutex
	healthy     bool
	client      *http.Client

	cbState          circuitState
	failureCount     int
	failureThreshold int
	cbLastFailure    time.Time
	cbTimeout        time.Duration

	halfOpenProbe atomic.Bool
}

const (
	defaultFailureThreshold = 3
	defaultCBTimeout        = 30 * time.Second
)

// MultiProxy основной прокси сервер с поддержкой множественных таргетов
type MultiProxy struct {
	config         atomic.Pointer[config.Config]
	targets        map[string]*TargetProxy
	routeConfigs   []RouteConfig
	routeByRule    map[*config.RoutingRule]*RouteConfig
	jwtValidator   *jwtutil.JWTValidator
	logger         *zap.Logger
	metrics        *Metrics
	mu             sync.RWMutex
	httpServer     *http.Server
	httpsServer    *http.Server
	globalLimiter  *rate.Limiter
	handler              http.Handler
	tracerProvider       *TracerProvider
	permissionsManager   *permissions.Manager
	publisher            *Publisher
}

// HealthChecker проверяет здоровье таргета
type HealthChecker struct {
	url     string
	period  time.Duration
	timeout time.Duration
	stopCh  chan struct{}
}

// NewMultiProxy создает новый мульти-прокси сервер
func NewMultiProxy(cfg *config.Config, logger *zap.Logger) (*MultiProxy, error) {
	jwtValidator, err := jwtutil.NewJWTValidator(
		cfg.JWT.SecretKey,
		cfg.JWT.Algorithm,
		cfg.JWT.ValidateExp,
		cfg.JWT.ValidateIss,
		cfg.JWT.ExpectedIss,
		cfg.JWT.ValidateAud,
		cfg.JWT.ExpectedAud,
		cfg.JWT.PublicKeyFile,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWT validator: %w", err)
	}

	mp := &MultiProxy{
		targets:      make(map[string]*TargetProxy),
		routeByRule:  make(map[*config.RoutingRule]*RouteConfig),
		jwtValidator: jwtValidator,
		logger:       logger,
		metrics:      NewMetrics(),
	}
	mp.config.Store(cfg)
	mp.initGlobalLimiter(cfg)
	mp.tracerProvider, _ = NewTracerProvider("api-gateway", logger)

	for _, targetCfg := range cfg.Targets {
		targetProxy, err := mp.createTargetProxy(&targetCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create proxy for target %s: %w", targetCfg.Name, err)
		}
		mp.targets[targetCfg.Name] = targetProxy
	}

	for i := range cfg.Routing.Rules {
		rule := &cfg.Routing.Rules[i]
		rc := RouteConfig{Rule: rule, Target: cfg.GetTargetByName(rule.TargetName)}
		if rule.RateLimit != nil {
			rc.RateLimit = NewIPRateLimiter(rule.RateLimit.RequestsPerSecond, rule.RateLimit.Burst)
		}
		mp.routeConfigs = append(mp.routeConfigs, rc)
		mp.routeByRule[rule] = &mp.routeConfigs[len(mp.routeConfigs)-1]
	}

	// Строим middleware цепочку
	handler := mp.proxyHandler()
	handler = corsPreflightMiddleware(mp, mp.metrics)(handler)
	handler = spaStaticMiddleware(mp)(handler)
	handler = globalRateLimitMiddleware(mp.globalLimiter, mp.metrics)(handler)
	handler = metricsEndpointMiddleware(mp.metrics, cfg.MetricsAllowedIPs)(handler)
	handler = activeRequestMetricsMiddleware(mp.metrics)(handler)
	handler = tracingMiddleware(mp.tracerProvider)(handler)
	handler = requestIDMiddleware()(handler)
	handler = recoveryMiddleware(logger)(handler)
	if cfg.BasicAuth.Enabled {
		logger.Info("Basic Auth enabled",
			zap.String("username", cfg.BasicAuth.Username),
			zap.Strings("skip_paths", cfg.BasicAuth.SkipPaths),
		)
		handler = basicAuthMiddleware(cfg.BasicAuth)(handler)
	}

	if cfg.Permissions.Enabled {
		mp.permissionsManager = permissions.NewManager(&cfg.Permissions, logger)
		handler = cacheInvalidateMiddleware(mp)(handler)
		logger.Info("Permissions module enabled",
			zap.String("service_url", cfg.Permissions.ServiceURL),
			zap.Duration("cache_ttl", cfg.Permissions.CacheTTL),
		)
	}

	if len(cfg.Webhooks) > 0 {
		publisher, err := NewPublisher(cfg, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to create publisher: %w", err)
		}
		mp.publisher = publisher
		logger.Info("Webhook publisher enabled",
			zap.Int("webhooks", len(cfg.Webhooks)),
		)
	}

	mp.handler = handler

	return mp, nil
}

// setCORSHeaders устанавливает CORS заголовки
func (mp *MultiProxy) setCORSHeaders(header http.Header, r *http.Request) {
	cfg := mp.config.Load()
	if cfg.Headers.CORS != nil && cfg.Headers.CORS.Enabled {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		allowed := false
		for _, o := range cfg.Headers.CORS.AllowedOrigins {
			if o == "*" || o == origin {
				allowed = true
				break
			}
		}
		if !allowed {
			return
		}
		header.Set("Access-Control-Allow-Origin", origin)
		if len(cfg.Headers.CORS.AllowedMethods) > 0 {
			header.Set("Access-Control-Allow-Methods", joinStrings(cfg.Headers.CORS.AllowedMethods))
		} else {
			header.Set("Access-Control-Allow-Methods", corsHeaders["Access-Control-Allow-Methods"])
		}
		if len(cfg.Headers.CORS.AllowedHeaders) > 0 {
			header.Set("Access-Control-Allow-Headers", joinStrings(cfg.Headers.CORS.AllowedHeaders))
		} else {
			header.Set("Access-Control-Allow-Headers", corsHeaders["Access-Control-Allow-Headers"])
		}
		if len(cfg.Headers.CORS.ExposeHeaders) > 0 {
			header.Set("Access-Control-Expose-Headers", joinStrings(cfg.Headers.CORS.ExposeHeaders))
		} else {
			header.Set("Access-Control-Expose-Headers", corsHeaders["Access-Control-Expose-Headers"])
		}
		header.Set("Access-Control-Allow-Credentials", corsHeaders["Access-Control-Allow-Credentials"])
		if cfg.Headers.CORS.MaxAge > 0 {
			header.Set("Access-Control-Max-Age", fmt.Sprintf("%d", cfg.Headers.CORS.MaxAge))
		} else {
			header.Set("Access-Control-Max-Age", corsHeaders["Access-Control-Max-Age"])
		}
		header.Add("Vary", "Origin")
		return
	}

	if cfg.Env == "dev" {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		header.Set("Access-Control-Allow-Origin", origin)
		header.Set("Access-Control-Allow-Methods", corsHeaders["Access-Control-Allow-Methods"])
		header.Set("Access-Control-Allow-Headers", corsHeaders["Access-Control-Allow-Headers"])
		header.Set("Access-Control-Expose-Headers", corsHeaders["Access-Control-Expose-Headers"])
		header.Set("Access-Control-Allow-Credentials", corsHeaders["Access-Control-Allow-Credentials"])
		header.Set("Access-Control-Max-Age", corsHeaders["Access-Control-Max-Age"])
		header.Add("Vary", "Origin")
	}
}

func joinStrings(strs []string) string {
	return strings.Join(strs, ", ")
}

// findRouteConfig возвращает RouteConfig для данного RoutingRule
func (mp *MultiProxy) findRouteConfig(rule *config.RoutingRule) *RouteConfig {
	if rule == nil {
		return nil
	}
	mp.mu.RLock()
	defer mp.mu.RUnlock()
	return mp.routeByRule[rule]
}

// createTargetProxy создает прокси для одного таргета
func (mp *MultiProxy) createTargetProxy(targetCfg *config.TargetConfig) (*TargetProxy, error) {
	targetURL, err := url.Parse(targetCfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}

	tp := &TargetProxy{
		config:           targetCfg,
		targetURL:        targetURL,
		healthy:          true,
		cbState:          stateClosed,
		failureThreshold: defaultFailureThreshold,
		cbTimeout:        defaultCBTimeout,
		client: &http.Client{
			Timeout: targetCfg.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  false,
			},
		},
	}

	if targetCfg.HealthCheck != "" && mp.config.Load().HealthCheck {
		tp.healthCheck = &HealthChecker{
			url:     targetCfg.HealthCheck,
			period:  30 * time.Second,
			timeout: 5 * time.Second,
			stopCh:  make(chan struct{}),
		}
		go tp.startHealthCheck(mp.logger, mp)
	}

	return tp, nil
}

// modifyRequest модифицирует запрос перед отправкой
func (mp *MultiProxy) modifyRequest(r *http.Request, targetCfg *config.TargetConfig, rule *config.RoutingRule) error {
	authHeader := r.Header.Get("Authorization")

	// если нет Authorization header — пробуем JWT из cookie
	if authHeader == "" {
		if c, err := r.Cookie("cml_access"); err == nil && c.Value != "" {
			authHeader = "Bearer " + c.Value
			r.Header.Set("Authorization", authHeader)
		}
	}

	authRequired := mp.config.Load().JWT.Required
	stripToken := mp.config.Load().Headers.StripAuthorization
	if rule != nil && rule.Auth != nil {
		authRequired = rule.Auth.Required
		if rule.Auth.StripToken != nil {
			stripToken = *rule.Auth.StripToken
		}
	}

	if authHeader == "" && authRequired {
		return fmt.Errorf("missing authorization token")
	}

	if authHeader != "" {
		if rule != nil && rule.Auth != nil && rule.Auth.Required {
			claims, err := mp.jwtValidator.ParseAndValidate(authHeader)
			if err != nil {
				return fmt.Errorf("invalid token: %w", err)
			}
			if err := mp.jwtValidator.ValidateClaims(claims); err != nil {
				return fmt.Errorf("invalid token claims: %w", err)
			}
			if len(rule.Auth.Roles) > 0 {
				if err := mp.checkRoles(claims, rule.Auth.Roles); err != nil {
					return err
				}
			}
			extracted := jwtutil.ExtractClaims(claims, mp.config.Load().JWT.ClaimMappings)
			for claimName, headerName := range mp.config.Load().Headers.ClaimToHeader {
				if val, ok := extracted[claimName]; ok {
					r.Header.Set(headerName, fmt.Sprintf("%v", val))
				}
			}

			// X-User-Signature: HMAC(user_id, api_secret) для верификации между сервисами
			signHeader := mp.config.Load().Headers.SignHeader
			if signHeader != "" {
				if userIDVal, ok := extracted["id"]; ok {
					userIDStr := fmt.Sprintf("%v", userIDVal)
					secret := mp.config.Load().Permissions.APIKey
					if secret != "" {
						mac := hmac.New(sha256.New, []byte(secret))
						mac.Write([]byte(userIDStr))
						sig := hex.EncodeToString(mac.Sum(nil))
						r.Header.Set(signHeader, sig)
					}
				}
			}

			// X-User-Permissions: если включён модуль permissions
			if mp.permissionsManager != nil {
				userIDVal, ok := extracted["id"]
				if ok {
					userID, err := toInt(userIDVal)
					if err == nil {
						if err := mp.permissionsManager.SetHeader(r, userID); err != nil {
							mp.logger.Warn("failed to set permissions header",
								zap.Int("user_id", userID),
								zap.Error(err),
							)
						}
					}
				}
			}
		}
	}

	for header, value := range mp.config.Load().Headers.AddHeaders {
		if r.Header.Get(header) == "" {
			r.Header.Set(header, value)
		}
	}

	if stripToken {
		r.Header.Del("Authorization")
	}

	if clientIP := r.Header.Get("X-Forwarded-For"); clientIP == "" {
		r.Header.Set("X-Forwarded-For", r.RemoteAddr)
	}

	return nil
}

func (mp *MultiProxy) checkRoles(claims jwt.MapClaims, requiredRoles []string) error {
	rolesRaw, ok := claims["roles"]
	if !ok {
		return fmt.Errorf("missing roles claim")
	}

	roles, ok := rolesRaw.([]interface{})
	if !ok {
		if roleStr, ok := rolesRaw.(string); ok {
			roles = []interface{}{roleStr}
		} else {
			return fmt.Errorf("invalid roles format")
		}
	}

	roleSet := make(map[string]bool, len(roles))
	for _, r := range roles {
		roleSet[fmt.Sprintf("%v", r)] = true
	}

	for _, required := range requiredRoles {
		if !roleSet[required] {
			return fmt.Errorf("missing required role: %s", required)
		}
	}

	return nil
}

// proxyRequest выполняет проксирование запроса
func (mp *MultiProxy) proxyRequest(w http.ResponseWriter, r *http.Request, target *TargetProxy, remainingPath string) {
	// Создаем URL для целевого сервера
	targetURL := *target.targetURL
	targetURL.Path = remainingPath
	if targetURL.Path == "" {
		targetURL.Path = "/"
	}
	targetURL.RawQuery = r.URL.RawQuery

	mp.logger.Debug("Proxying to target",
		zap.String("target_url", targetURL.String()),
		zap.String("method", r.Method),
	)

	// Логируем тело запроса если есть
	var requestBody []byte
	if r.Body != nil {
		bodyReader := io.Reader(r.Body)
		if mp.config.Load().Server.MaxRequestBodySize > 0 {
			bodyReader = io.LimitReader(bodyReader, mp.config.Load().Server.MaxRequestBodySize)
		}
		requestBody, _ = io.ReadAll(bodyReader)
		err := r.Body.Close()
		if err != nil {
			mp.logger.Error("failed to close body", zap.Error(err))
			return
		}
		if mp.logger.Core().Enabled(zap.DebugLevel) && len(requestBody) > 0 {
			mp.logger.Debug("Request body",
				zap.String("target", target.config.Name),
				zap.ByteString("body", requestBody),
			)
		}
		// Восстанавливаем тело для дальнейшего использования
		r.Body = io.NopCloser(bytes.NewBuffer(requestBody))
	}

	// Создаем новый запрос
	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL.String(), r.Body)
	if err != nil {
		mp.logger.Error("Failed to create proxy request",
			zap.Error(err),
			zap.String("target", target.config.Name),
		)
		mp.setCORSHeaders(w.Header(), r)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Копируем заголовки
	proxyReq.Header = r.Header.Clone()

	// Remove Accept-Encoding to prevent Go transport from auto-decompressing.
	// Otherwise Go strips Content-Encoding from response but keeps compressed body,
	// browser gets gzip bytes without Content-Encoding header -> SyntaxError -> white screen.
	proxyReq.Header.Del("Accept-Encoding")

	// Выполняем запрос
	mp.logger.Info("Sending request to target",
		zap.String("target", target.config.Name),
		zap.String("url", targetURL.String()),
		zap.String("host", targetURL.Host),
		zap.String("port", targetURL.Port()),
		zap.Any("headers", r.Header),
	)

	resp, err := target.client.Do(proxyReq)
	if err != nil {
		if mp.config.Load().App.CircuitBreaker {
			target.recordCall(err)
		}
		mp.setCORSHeaders(w.Header(), r)
		mp.logger.Error("Proxy request failed",
			zap.Error(err),
			zap.String("target", target.config.Name),
		)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			mp.logger.Error("failed to close body", zap.Error(err))
			return
		}
	}(resp.Body)

	mp.logger.Debug("Received response from target",
		zap.String("target", target.config.Name),
		zap.Int("status", resp.StatusCode),
	)

	if mp.config.Load().App.CircuitBreaker {
		if resp.StatusCode >= 500 {
			target.recordCall(fmt.Errorf("upstream %d", resp.StatusCode))
		} else {
			target.recordCall(nil)
		}
	}

	// Копируем заголовки ответа от бэкенда
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Добавляем CORS заголовки
	mp.setCORSHeaders(w.Header(), r)

	// Устанавливаем статус код
	w.WriteHeader(resp.StatusCode)

	// Стримим тело ответа (без полной буферизации в память)
	// Читаем тело полностью для отладки
	bodyBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		mp.logger.Error("Failed to read response body",
			zap.Error(readErr),
			zap.String("target", target.config.Name),
		)
		http.Error(w, readErr.Error(), http.StatusBadGateway)
		return
	}
	mp.logger.Debug("Response body read",
		zap.String("target", target.config.Name),
		zap.Int("body_len", len(bodyBytes)),
	)
	bytesWritten, writeErr := w.Write(bodyBytes)
	if writeErr != nil {
		mp.logger.Error("Failed to write response body",
			zap.Error(writeErr),
			zap.String("target", target.config.Name),
		)
		return
	}

	mp.logger.Debug("Response sent to client",
		zap.String("target", target.config.Name),
		zap.Int("status", resp.StatusCode),
		zap.Int("bytes_written", bytesWritten),
	)
}

// Обработчик OPTIONS запросов
func (mp *MultiProxy) handlePreflight(w http.ResponseWriter, r *http.Request) {
	mp.setCORSHeaders(w.Header(), r)
	w.WriteHeader(http.StatusOK)

	mp.logger.Debug("Handled preflight request",
		zap.String("origin", r.Header.Get("Origin")),
		zap.String("method", r.Header.Get("Access-Control-Request-Method")),
	)
}

// ServeHTTP реализует http.Handler — делегирует в middleware цепочку
func (mp *MultiProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mp.handler.ServeHTTP(w, r)
}

// logAccess пишет единую строчку access log
func (mp *MultiProxy) logAccess(reqID, traceID string, r *http.Request, statusCode int, duration time.Duration, target *config.TargetConfig) {
	mp.logger.Info("Access",
		zap.String("request_id", reqID),
		zap.String("trace_id", traceID),
		zap.String("method", r.Method),
		zap.String("path", r.URL.Path),
		zap.Int("status", statusCode),
		zap.Duration("duration", duration),
		zap.String("remote_addr", r.RemoteAddr),
		zap.String("user_agent", r.UserAgent()),
		zap.String("target", func() string {
			if target != nil {
				return target.Name
			}
			return "-"
		}()),
	)
}

// isHealthy возвращает статус здоровья таргета (учитывая circuit breaker)
func (tp *TargetProxy) isHealthy(cbEnabled bool) bool {
	tp.mu.RLock()
	defer tp.mu.RUnlock()

	if !tp.healthy {
		return false
	}

	if !cbEnabled {
		return true
	}

	switch tp.cbState {
	case stateClosed:
		return true
	case stateOpen:
		if time.Since(tp.cbLastFailure) > tp.cbTimeout {
			tp.cbState = stateHalfOpen
			tp.halfOpenProbe.Store(true)
			return true
		}
		return false
	case stateHalfOpen:
		return tp.halfOpenProbe.CompareAndSwap(true, false)
	}
	return true
}

// setHealthy устанавливает статус здоровья
func (tp *TargetProxy) setHealthy(healthy bool) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.healthy = healthy
}

// recordCall регистрирует результат запроса для circuit breaker
func (tp *TargetProxy) recordCall(err error) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	switch tp.cbState {
	case stateClosed:
		if err != nil {
			tp.failureCount++
			if tp.failureCount >= tp.failureThreshold {
				tp.cbState = stateOpen
				tp.cbLastFailure = time.Now()
			}
		} else {
			tp.failureCount = 0
		}

	case stateHalfOpen:
		if err != nil {
			tp.cbState = stateOpen
			tp.cbLastFailure = time.Now()
		} else {
			tp.cbState = stateClosed
			tp.failureCount = 0
		}

	case stateOpen:
		if err == nil {
			tp.cbState = stateClosed
			tp.failureCount = 0
		}
	}
}

// startHealthCheck запускает проверку здоровья
func (tp *TargetProxy) startHealthCheck(logger *zap.Logger, mp *MultiProxy) {
	ticker := time.NewTicker(tp.healthCheck.period)
	defer ticker.Stop()

	tp.checkHealth(logger, mp)

	for {
		select {
		case <-ticker.C:
			tp.checkHealth(logger, mp)
		case <-tp.healthCheck.stopCh:
			return
		}
	}
}

// checkHealth выполняет проверку здоровья
func (tp *TargetProxy) checkHealth(logger *zap.Logger, mp *MultiProxy) {
	client := &http.Client{
		Timeout: tp.healthCheck.timeout,
	}

	resp, err := client.Get(tp.healthCheck.url)
	if err != nil {
		logger.Warn("Health check failed",
			zap.String("target", tp.config.Name),
			zap.Error(err),
		)
		tp.setHealthy(false)
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			zap.L().Error("Error closing body", zap.Error(err))
		}
	}(resp.Body)

	// Читаем тело ответа health check
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Warn("Health check failed")
		return
	}

	healthy := resp.StatusCode >= 200 && resp.StatusCode < 300
	tp.setHealthy(healthy)

	if healthy {
		logger.Debug("Health check successful",
			zap.String("target", tp.config.Name),
			zap.Int("status", resp.StatusCode),
			zap.ByteString("response", body),
		)
	} else {
		logger.Warn("Health check failed",
			zap.String("target", tp.config.Name),
			zap.Int("status", resp.StatusCode),
			zap.ByteString("response", body),
		)
	}
	mp.metrics.SetTargetUp(tp.config.Name, healthy)
}

// Reload перезагружает конфигурацию и обновляет targets/routing
func (mp *MultiProxy) Reload(cfg *config.Config) error {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	oldTargets := mp.targets
	mp.config.Store(cfg)

	newTargets := make(map[string]*TargetProxy, len(cfg.Targets))
	for _, targetCfg := range cfg.Targets {
		if old, ok := oldTargets[targetCfg.Name]; ok {
			old.config = &targetCfg
			newTargets[targetCfg.Name] = old
			continue
		}
		tp, err := mp.createTargetProxy(&targetCfg)
		if err != nil {
			return fmt.Errorf("failed to create proxy for target %s: %w", targetCfg.Name, err)
		}
		newTargets[targetCfg.Name] = tp
	}

	for name, old := range oldTargets {
		if _, ok := newTargets[name]; !ok {
			if old.healthCheck != nil {
				close(old.healthCheck.stopCh)
			}
		}
	}

	mp.targets = newTargets
	mp.rebuildRouteConfigs(cfg)
	mp.reloadGlobalLimiter(cfg)

	mp.logger.Info("Configuration reloaded",
		zap.Int("targets", len(newTargets)),
	)
	return nil
}

func (mp *MultiProxy) rebuildRouteConfigs(cfg *config.Config) {
	mp.routeConfigs = nil
	mp.routeByRule = make(map[*config.RoutingRule]*RouteConfig)

	for i := range cfg.Routing.Rules {
		rule := &cfg.Routing.Rules[i]
		rc := RouteConfig{Rule: rule, Target: cfg.GetTargetByName(rule.TargetName)}
		if rule.RateLimit != nil {
			rc.RateLimit = NewIPRateLimiter(rule.RateLimit.RequestsPerSecond, rule.RateLimit.Burst)
		}
		mp.routeConfigs = append(mp.routeConfigs, rc)
		mp.routeByRule[rule] = &mp.routeConfigs[len(mp.routeConfigs)-1]
	}
}

func (mp *MultiProxy) Stop(ctx context.Context) error {
	mp.logger.Info("Stopping multi-proxy server")

	for name, target := range mp.targets {
		if target.healthCheck != nil {
			close(target.healthCheck.stopCh)
		}
		mp.logger.Debug("Stopped target", zap.String("target", name))
	}

	if mp.httpServer != nil {
		if err := mp.httpServer.Shutdown(ctx); err != nil {
			mp.logger.Error("HTTP server shutdown error", zap.Error(err))
		}
	}

	if mp.httpsServer != nil {
		if err := mp.httpsServer.Shutdown(ctx); err != nil {
			mp.logger.Error("HTTPS server shutdown error", zap.Error(err))
		}
	}

	if err := mp.tracerProvider.Shutdown(ctx); err != nil {
		mp.logger.Error("Tracer provider shutdown error", zap.Error(err))
	}

	if mp.publisher != nil {
		mp.publisher.Close()
	}

	return nil
}

// Start запускает HTTP или HTTPS сервер (с автосертификатами Let's Encrypt)
func (mp *MultiProxy) Start() error {
	if mp.config.Load().TLS != nil && mp.config.Load().TLS.Enabled {
		return mp.startTLS()
	}

	addr := fmt.Sprintf(":%d", mp.config.Load().Server.Port)

	server := &http.Server{
		Addr:         addr,
		Handler:      mp,
		ReadTimeout:  mp.config.Load().Server.ReadTimeout,
		WriteTimeout: mp.config.Load().Server.WriteTimeout,
		IdleTimeout:  mp.config.Load().Server.IdleTimeout,
	}
	mp.httpServer = server

	mp.logger.Info("Starting HTTP proxy server",
		zap.Int("port", mp.config.Load().Server.Port),
		zap.Int("targets", len(mp.targets)),
		zap.Bool("dev_mode", mp.config.Load().Env == "dev"),
	)

	mp.logTargets()

	return server.ListenAndServe()
}

func (mp *MultiProxy) logTargets() {
	for name, target := range mp.targets {
		target.mu.RLock()
		healthy := target.healthy
		target.mu.RUnlock()
		mp.logger.Info("Registered target",
			zap.String("name", name),
			zap.String("url", target.config.URL),
			zap.Bool("healthy", healthy),
		)
	}
}

func (mp *MultiProxy) httpRedirectHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
			w.WriteHeader(http.StatusOK)
			return
		}
		cfg := mp.config.Load()
		if cfg.TLS == nil || !cfg.TLS.RedirectHTTP {
			mp.ServeHTTP(w, r)
			return
		}
		target := fmt.Sprintf("https://%s%s", r.Host, r.URL.RequestURI())
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}

// serveStatic пытается отдать SPA статику, возвращает true если запрос обработан
func (mp *MultiProxy) serveStatic(w http.ResponseWriter, r *http.Request) bool {
	if mp.config.Load().Static == nil {
		return false
	}

	// Проверяем skip_prefixes — если путь начинается с одного из них, пропускаем статику
	for _, skip := range mp.config.Load().Static.SkipPrefixes {
		if strings.HasPrefix(r.URL.Path, skip) {
			return false
		}
	}

	// Если есть routing rule с Host для этого запроса — пропускаем статику,
	// чтобы host-based роутинг работал (например chatcom.sarnas.ru → chatcom backend)
	_, rule := mp.config.Load().FindTargetForPath(r.URL.Path, r.Method, r.Host)
	if rule != nil && rule.Host != "" {
		return false
	}

	for _, app := range mp.config.Load().Static.Apps {
		if strings.HasPrefix(r.URL.Path, app.PathPrefix) {
			mp.serveSPA(w, r, &app)
			return true
		}
	}
	return false
}

func (mp *MultiProxy) serveSPA(w http.ResponseWriter, r *http.Request, app *config.StaticApp) {
	path := strings.TrimPrefix(r.URL.Path, app.PathPrefix)
	if path == "" || path[0] != '/' {
		path = "/" + path
	}

	path = mp.resolveStaticPath(path, app)

	maxAge := app.MaxAge
	if maxAge == 0 {
		maxAge = 3600
	}

	r.URL.Path = app.PathPrefix + strings.TrimPrefix(path, "/")
	fs := http.Dir(app.RootDir)
	handler := http.StripPrefix(app.PathPrefix, http.FileServer(fs))
	handler = cacheControlMiddleware(handler, maxAge)
	handler.ServeHTTP(w, r)
}

// resolveStaticPath определяет какой файл отдать для запрошенного пути.
// Поддерживает: index.html в директории, flat .html (about.html → /about),
// SPA fallback (любой несуществующий путь → index.html).
func (mp *MultiProxy) resolveStaticPath(rawPath string, app *config.StaticApp) string {
	root := app.RootDir

	// Корень — FileServer сам найдёт index.html
	if rawPath == "/" {
		return "/"
	}

	// Убираем слеш в конце для единообразия
	cleanPath := strings.TrimSuffix(rawPath, "/")

	// Проверяем: существует ли директория с index.html
	dirPath := filepath.Join(root, cleanPath)
	indexInDir := filepath.Join(root, cleanPath, app.IndexFile)
	if info, err := os.Stat(dirPath); err == nil && info.IsDir() {
		if _, err := os.Stat(indexInDir); err == nil {
			return rawPath // http.FileServer сам найдёт index.html
		}
		// Директория есть, но index.html нет — ищем flat .html
		flatHTML := dirPath + ".html"
		if _, err := os.Stat(flatHTML); err == nil {
			return cleanPath + ".html"
		}
	}

	// Проверяем: существует ли flat .html (about.html → /about)
	flatHTML := filepath.Join(root, cleanPath+".html")
	if _, err := os.Stat(flatHTML); err == nil {
		return cleanPath + ".html"
	}

	// Проверяем: существует ли сам файл как есть
	if info, err := os.Stat(filepath.Join(root, rawPath)); err == nil && !info.IsDir() {
		return rawPath
	}

	// Всё остальное — SPA fallback на корень (FileServer сам найдёт index.html)
	return "/"
}

func cacheControlMiddleware(next http.Handler, maxAge int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
		next.ServeHTTP(w, r)
	})
}

// initGlobalLimiter настраивает глобальный rate limiter из конфига
func cacheInvalidateMiddleware(mp *MultiProxy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/_cache/permissions/invalidate" {
				mp.permissionsManager.InvalidateCacheHandler(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (mp *MultiProxy) initGlobalLimiter(cfg *config.Config) {
	if cfg.Routing.GlobalLimit != nil && cfg.Routing.GlobalLimit.RequestsPerSecond > 0 {
		mp.globalLimiter = rate.NewLimiter(
			rate.Limit(cfg.Routing.GlobalLimit.RequestsPerSecond),
			cfg.Routing.GlobalLimit.Burst,
		)
		mp.logger.Info("Global rate limiter enabled",
			zap.Float64("rps", cfg.Routing.GlobalLimit.RequestsPerSecond),
			zap.Int("burst", cfg.Routing.GlobalLimit.Burst),
		)
	}
}

// reloadGlobalLimiter обновляет глобальный limiter при перезагрузке конфига
func (mp *MultiProxy) reloadGlobalLimiter(cfg *config.Config) {
	if cfg.Routing.GlobalLimit != nil && cfg.Routing.GlobalLimit.RequestsPerSecond > 0 {
		if mp.globalLimiter == nil {
			mp.globalLimiter = rate.NewLimiter(
				rate.Limit(cfg.Routing.GlobalLimit.RequestsPerSecond),
				cfg.Routing.GlobalLimit.Burst,
			)
		} else {
			mp.globalLimiter.SetLimit(rate.Limit(cfg.Routing.GlobalLimit.RequestsPerSecond))
			mp.globalLimiter.SetBurst(cfg.Routing.GlobalLimit.Burst)
		}
	} else {
		mp.globalLimiter = nil
	}
}

func toInt(v interface{}) (int, error) {
	switch val := v.(type) {
	case float64:
		return int(val), nil
	case int:
		return val, nil
	case string:
		var result int
		_, err := fmt.Sscanf(val, "%d", &result)
		return result, err
	default:
		return 0, fmt.Errorf("cannot convert %T to int", v)
	}
}
