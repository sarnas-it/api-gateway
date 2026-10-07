package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"golang.org/x/time/rate"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/identity"
	"github.com/basili4-1982/api-gateway/internal/jwtutil"
	"github.com/basili4-1982/api-gateway/internal/permissions"
	"github.com/golang-jwt/jwt/v5"
)

type RouteConfig struct {
	Rule      *config.RoutingRule
	RateLimit *IPRateLimiter

	// Targets — пул кандидатов одного route: все таргеты правил, совпадающих
	// по (host, path_prefix, methods). weights[i] — вес Targets[i],
	// totalWeight — их сумма (кандидаты с весом <= 0 в неё не входят).
	// counter — общий счётчик взвешенного round-robin; запросы идут
	// параллельно, поэтому только atomic.
	Targets     []*TargetProxy
	weights     []int
	totalWeight int
	counter     atomic.Uint64
}

// pickTarget выбирает следующий таргет пула взвешенным round-robin.
// Нездоровые кандидаты и кандидаты с весом <= 0 пропускаются.
// Если здоровых кандидатов с положительным весом нет — возвращает nil,
// и вызывающая сторона отдаёт 503.
func (rc *RouteConfig) pickTarget(cbEnabled bool) *TargetProxy {
	if rc == nil || rc.totalWeight <= 0 {
		return nil
	}

	// Быстрый путь для обычного роута с одним таргетом: без аллокации и
	// без инкремента счётчика.
	if len(rc.Targets) == 1 {
		tp := rc.Targets[0]
		if rc.weights[0] <= 0 || !tp.acquire(cbEnabled) {
			return nil
		}
		return tp
	}

	type candidate struct {
		target *TargetProxy
		weight int
	}
	// Half-open пробник расходуется только выбранным таргетом: кандидаты
	// проверяются read-only (eligible), а acquire вызывается на победителе.
	// Иначе проигравший кандидат «сжёг» бы пробник и остался навсегда
	// исключённым, пока его не переведёт другой путь.
	for attempt := 0; attempt <= len(rc.Targets); attempt++ {
		healthy := make([]candidate, 0, len(rc.Targets))
		sum := 0
		for i, tp := range rc.Targets {
			w := rc.weights[i]
			if w <= 0 || !tp.eligible(cbEnabled) {
				continue
			}
			healthy = append(healthy, candidate{target: tp, weight: w})
			sum += w
		}
		if sum <= 0 {
			return nil
		}

		n := rc.counter.Add(1) - 1
		pos := int(n % uint64(sum))
		chosen := healthy[len(healthy)-1].target
		for _, c := range healthy {
			if pos < c.weight {
				chosen = c.target
				break
			}
			pos -= c.weight
		}
		if chosen.acquire(cbEnabled) {
			return chosen
		}
		// Пробник забрал параллельный запрос — пересчитываем кандидатов.
	}
	return nil
}

var corsHeaders = map[string]string{
	"Access-Control-Allow-Origin":      "", // Будет заполняться динамически
	"Access-Control-Allow-Methods":     "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD, QUERY",
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
	config       *config.TargetConfig
	targetURL    *url.URL
	healthCheck  *HealthChecker
	mu           sync.RWMutex
	healthy      bool
	transport    *http.Transport
	timeout      time.Duration
	reverseProxy *httputil.ReverseProxy

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
	config             atomic.Pointer[config.Config]
	targets            map[string]*TargetProxy
	routeConfigs       []*RouteConfig
	routeByRule        map[*config.RoutingRule]*RouteConfig
	jwtValidator       *jwtutil.JWTValidator
	logger             *zap.Logger
	metrics            *Metrics
	mu                 sync.RWMutex
	httpServer         *http.Server
	httpsServer        *http.Server
	globalLimiter      *rate.Limiter
	handler            http.Handler
	tracerProvider     *TracerProvider
	permissionsManager *permissions.Manager
	identity           *identity.Manager
	publisher          *Publisher
}

// HealthChecker проверяет здоровье таргета
type HealthChecker struct {
	url      string
	period   time.Duration
	timeout  time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
}

// Stop останавливает горутину health check; повторные вызовы безопасны.
func (hc *HealthChecker) Stop() {
	if hc == nil {
		return
	}
	hc.stopOnce.Do(func() { close(hc.stopCh) })
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

	var identityManager *identity.Manager
	if cfg.Identity.Enabled {
		identityManager, err = identity.NewManager(cfg.Identity, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to create identity manager: %w", err)
		}
	}

	mp := &MultiProxy{
		targets:      make(map[string]*TargetProxy),
		routeByRule:  make(map[*config.RoutingRule]*RouteConfig),
		jwtValidator: jwtValidator,
		identity:     identityManager,
		logger:       logger,
		metrics:      NewMetrics(cfg.MetricsEnabled),
	}
	mp.config.Store(cfg)
	mp.initGlobalLimiter(cfg)
	mp.tracerProvider, _ = NewTracerProvider("api-gateway", logger)

	for _, targetCfg := range cfg.Targets {
		targetProxy, err := mp.createTargetProxy(&targetCfg, cfg.HealthCheck)
		if err != nil {
			return nil, fmt.Errorf("failed to create proxy for target %s: %w", targetCfg.Name, err)
		}
		mp.targets[targetCfg.Name] = targetProxy
	}

	mp.rebuildRouteConfigs(cfg)

	// Строим middleware цепочку
	handler := mp.proxyHandler()
	handler = corsPreflightMiddleware(mp, mp.metrics)(handler)
	handler = spaStaticMiddleware(mp)(handler)
	handler = globalRateLimitMiddleware(mp.globalLimiter, mp.metrics)(handler)
	if cfg.MetricsEnabled {
		handler = metricsEndpointMiddleware(mp.metrics, cfg.MetricsAllowedIPs)(handler)
		handler = activeRequestMetricsMiddleware(mp.metrics)(handler)
	}
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
		wildcard := false
		exactMatch := false
		for _, o := range cfg.Headers.CORS.AllowedOrigins {
			if o == "*" {
				wildcard = true
			} else if o == origin {
				exactMatch = true
			}
		}
		if !wildcard && !exactMatch {
			return
		}
		// A reflected Origin must never be paired with
		// Allow-Credentials unless that exact origin is allow-listed:
		// otherwise any site could make credentialed requests and read
		// the response.
		if exactMatch {
			header.Set("Access-Control-Allow-Origin", origin)
		} else {
			header.Set("Access-Control-Allow-Origin", "*")
		}
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
		if exactMatch {
			header.Set("Access-Control-Allow-Credentials", corsHeaders["Access-Control-Allow-Credentials"])
		}
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

// createTargetProxy создает прокси для одного таргета. healthCheckEnabled
// передаётся явно, чтобы Reload мог строить новые прокси до подмены конфига.
func (mp *MultiProxy) createTargetProxy(targetCfg *config.TargetConfig, healthCheckEnabled bool) (*TargetProxy, error) {
	targetURL, err := url.Parse(targetCfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}

	// Пул keep-alive соединений к таргету. Если он меньше пиковой
	// конкурентности, транспорт постоянно закрывает и переоткрывает
	// соединения — CPU уходит в connect, RPS обваливается под нагрузкой.
	cfg := mp.config.Load()
	perHost := cfg.MaxIdleConnsPerHost
	if perHost <= 0 {
		perHost = 1000
	}
	// MaxIdleConns — суммарный лимит idle по всем таргетам; даём каждому
	// таргету полный per-host бюджет.
	totalIdle := perHost * len(cfg.Targets)
	if totalIdle < perHost {
		totalIdle = perHost
	}

	tp := &TargetProxy{
		config:           targetCfg,
		targetURL:        targetURL,
		healthy:          true,
		cbState:          stateClosed,
		failureThreshold: defaultFailureThreshold,
		cbTimeout:        defaultCBTimeout,
		timeout:          targetCfg.Timeout,
		transport: &http.Transport{
			MaxIdleConns:        totalIdle,
			MaxIdleConnsPerHost: perHost,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  false,
		},
	}
	tp.reverseProxy = mp.newReverseProxy(tp)

	if targetCfg.HealthCheck != "" && healthCheckEnabled {
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

// internalPathHeader передаёт remainingPath в Director без лишней
// аллокации context.WithValue — proxyRequest выставляет его перед вызовом
// ServeHTTP и удаляет сразу после (синхронно), поэтому наружу он никогда
// не уходит и не попадает в аудит/вебхуки.
const internalPathHeader = "X-Gateway-Internal-Path"

// proxyBufferPool переиспользует 32KB буферы для копирования тела ответа
// (httputil.ReverseProxy.copyBuffer). Без него ReverseProxy аллоцирует
// новый буфер на КАЖДЫЙ проксированный запрос — это самая крупная разовая
// аллокация на горячем пути, гораздо больше, чем структуры вроде
// responseWriter. Один пул на весь процесс: буферы одного размера, общий
// пул даёт больше переиспользования, чем пул на таргет.
type proxyBufferPool struct {
	pool sync.Pool
}

func newProxyBufferPool() *proxyBufferPool {
	return &proxyBufferPool{
		pool: sync.Pool{
			New: func() any {
				b := make([]byte, 32*1024)
				return &b
			},
		},
	}
}

func (p *proxyBufferPool) Get() []byte {
	return *(p.pool.Get().(*[]byte))
}

func (p *proxyBufferPool) Put(b []byte) {
	p.pool.Put(&b)
}

var sharedProxyBufferPool = newProxyBufferPool()

// newReverseProxy строит httputil.ReverseProxy один раз на таргет.
// Director читает путь из internalPathHeader, поэтому один и тот же
// экземпляр безопасно переиспользуется для всех запросов к этому таргету —
// без аллокации новых Director/ModifyResponse/ErrorHandler замыканий на
// каждый запрос.
func (mp *MultiProxy) newReverseProxy(target *TargetProxy) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport:  target.transport,
		BufferPool: sharedProxyBufferPool,
		// Немедленный flush: необходимо для SSE (text/event-stream), иначе
		// потоковые ответы буферизуются и клиент не получает события.
		FlushInterval: -1,
		Director: func(req *http.Request) {
			req.URL.Scheme = target.targetURL.Scheme
			req.URL.Host = target.targetURL.Host
			if path := req.Header.Get(internalPathHeader); path != "" {
				req.URL.Path = path
				req.Header.Del(internalPathHeader)
			}
			req.Host = target.targetURL.Host

			// Remove Accept-Encoding to prevent Go transport from auto-decompressing.
			// Otherwise Go strips Content-Encoding from response but keeps compressed body,
			// browser gets gzip bytes without Content-Encoding header -> SyntaxError -> white screen.
			req.Header.Del("Accept-Encoding")
		},
		ModifyResponse: func(resp *http.Response) error {
			if mp.config.Load().App.CircuitBreaker {
				// Получить любой HTTP-ответ от таргета, даже 5xx, значит транспорт
				// исправен — это уровень приложения на бэкенде, а не недоступность
				// таргета. Пробой цепи должны вызывать только ошибки транспорта,
				// которые попадают в ErrorHandler ниже.
				target.recordCall(nil)
			}
			mp.setCORSHeaders(resp.Header, resp.Request)
			mp.logger.Debug("Received response from target",
				zap.String("target", target.config.Name),
				zap.Int("status", resp.StatusCode),
			)
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			if mp.config.Load().App.CircuitBreaker {
				target.recordCall(err)
			}
			mp.setCORSHeaders(rw.Header(), req)
			mp.logger.Error("Proxy request failed",
				zap.Error(err),
				zap.String("target", target.config.Name),
			)
			http.Error(rw, err.Error(), http.StatusBadGateway)
		},
	}
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
		// A presented token is always cryptographically verified — regardless
		// of whether the matched route declares its own `auth:` block. Routes
		// without one previously inherited authRequired from the global
		// jwt.required flag but never actually validated the token, so any
		// non-empty Authorization header (or cml_access cookie) satisfied
		// auth on those routes without checking its signature.
		claims, err := mp.jwtValidator.ParseAndValidate(authHeader)
		if err != nil {
			// Наш JWT не прошёл — пробуем внешние identity-провайдеры на
			// защищённых маршрутах (наш JWT-путь не меняется).
			if mp.identity != nil && rule != nil && rule.Auth != nil && rule.Auth.Required {
				user, ierr := mp.identity.Resolve(r.Context(), authHeader)
				if ierr != nil {
					return fmt.Errorf("invalid token: %w", ierr)
				}
				if len(rule.Auth.Roles) > 0 && !hasAllRoles(user.Roles, rule.Auth.Roles) {
					return fmt.Errorf("missing required role: %s", rule.Auth.Roles[0])
				}
				if len(rule.Auth.RolesAll) > 0 && !hasAllRoles(user.Roles, rule.Auth.RolesAll) {
					return fmt.Errorf("missing required role: %s", rule.Auth.RolesAll[0])
				}
				mp.applyIdentityHeaders(r, user)
			} else if authRequired {
				return fmt.Errorf("invalid token: %w", err)
			} else {
				return nil
			}
		} else if cerr := mp.jwtValidator.ValidateClaims(claims); cerr != nil {
			if authRequired {
				return fmt.Errorf("invalid token claims: %w", cerr)
			}
			return nil
		} else {
			if rule != nil && rule.Auth != nil && (len(rule.Auth.Roles) > 0 || len(rule.Auth.RolesAll) > 0) {
				if err := mp.checkRoles(claims, rule.Auth.Roles, rule.Auth.RolesAll); err != nil {
					return err
				}
			}
			mp.applyClaimHeaders(r, claims)
		}
	}

	for header, value := range mp.config.Load().Headers.AddHeaders {
		r.Header.Set(header, value)
	}

	if stripToken {
		r.Header.Del("Authorization")
	}

	if clientIP := r.Header.Get("X-Forwarded-For"); clientIP == "" {
		r.Header.Set("X-Forwarded-For", r.RemoteAddr)
	}

	return nil
}

func (mp *MultiProxy) applyClaimHeaders(r *http.Request, claims jwt.MapClaims) {
	extracted := jwtutil.ExtractClaims(claims, mp.config.Load().JWT.ClaimMappings)
	for claimName, headerName := range mp.config.Load().Headers.ClaimToHeader {
		if val, ok := extracted[claimName]; ok {
			r.Header.Set(headerName, fmt.Sprintf("%v", val))
		}
	}
	if userIDVal, ok := extracted["id"]; ok {
		if userID, err := toInt(userIDVal); err == nil {
			mp.setUserSignature(r, userID)
			mp.setUserPermissions(r, userID)
		}
	}
}

func (mp *MultiProxy) applyIdentityHeaders(r *http.Request, user *identity.User) {
	r.Header.Set("X-User-ID", strconv.Itoa(user.ID))
	if user.Email != "" {
		r.Header.Set("X-User-Email", user.Email)
	}
	if len(user.Roles) > 0 {
		r.Header.Set("X-User-Roles", strings.Join(user.Roles, ","))
	}
	mp.setUserSignature(r, user.ID)
	mp.setUserPermissions(r, user.ID)
}

func (mp *MultiProxy) setUserSignature(r *http.Request, userID int) {
	signHeader := mp.config.Load().Headers.SignHeader
	secret := mp.config.Load().Permissions.APIKey
	if signHeader == "" || secret == "" {
		return
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.Itoa(userID)))
	r.Header.Set(signHeader, hex.EncodeToString(mac.Sum(nil)))
}

func (mp *MultiProxy) setUserPermissions(r *http.Request, userID int) {
	if mp.permissionsManager == nil {
		return
	}
	if err := mp.permissionsManager.SetHeader(r, userID); err != nil {
		mp.logger.Warn("failed to set permissions header",
			zap.Int("user_id", userID), zap.Error(err))
	}
}

func hasAllRoles(have, required []string) bool {
	set := make(map[string]bool, len(have))
	for _, r := range have {
		set[r] = true
	}
	for _, req := range required {
		if !set[req] {
			return false
		}
	}
	return true
}

// checkRoles проверяет роли из claims по схеме
// (any of anyOf) AND (all of allOf). Пустые оба списка — проверка не нужна.
// Отсутствующий, не строковый/не массивный claim или массив с нестроковыми
// элементами трактуется как ошибка (fail closed), чтобы защищённый роут
// нельзя было обойти подделкой формы claim.
func (mp *MultiProxy) checkRoles(claims jwt.MapClaims, anyOf, allOf []string) error {
	if len(anyOf) == 0 && len(allOf) == 0 {
		return nil
	}

	roleSet, err := extractRoles(claims)
	if err != nil {
		return err
	}

	if len(anyOf) > 0 {
		found := false
		for _, required := range anyOf {
			if roleSet[required] {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("missing any required role: %s", strings.Join(anyOf, ", "))
		}
	}

	for _, required := range allOf {
		if !roleSet[required] {
			return fmt.Errorf("missing required role: %s", required)
		}
	}

	return nil
}

// extractRoles разбирает claim "roles" в множество ролей. Claim может быть
// одной строкой или массивом строк; любая другая форма — ошибка.
func extractRoles(claims jwt.MapClaims) (map[string]bool, error) {
	raw, ok := claims["roles"]
	if !ok {
		return nil, fmt.Errorf("missing roles claim")
	}

	switch v := raw.(type) {
	case string:
		return map[string]bool{v: true}, nil
	case []string:
		set := make(map[string]bool, len(v))
		for _, role := range v {
			set[role] = true
		}
		return set, nil
	case []interface{}:
		set := make(map[string]bool, len(v))
		for _, item := range v {
			role, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("invalid roles claim: non-string element %T", item)
			}
			set[role] = true
		}
		return set, nil
	default:
		return nil, fmt.Errorf("invalid roles claim format: %T", raw)
	}
}

// proxyRequest выполняет проксирование запроса
// proxyRequest проксирует запрос через httputil.ReverseProxy. ReverseProxy
// calls the RoundTripper directly (not an http.Client), so it never follows
// a target's redirect responses itself — they're relayed to the real client
// verbatim, exactly as a reverse proxy must. It also streams the response
// body instead of buffering it, so long-lived responses (e.g. chatcom's SSE
// comment stream) aren't held until the upstream closes the connection.
func (mp *MultiProxy) proxyRequest(w http.ResponseWriter, r *http.Request, target *TargetProxy, remainingPath string) {
	if remainingPath == "" {
		remainingPath = "/"
	}

	if r.Body != nil {
		if maxSize := mp.config.Load().Server.EffectiveMaxRequestBodySize(); maxSize > 0 {
			r.Body = io.NopCloser(io.LimitReader(r.Body, maxSize))
		}
	}

	if mp.config.Load().Logging.AccessLog {
		mp.logger.Info("Sending request to target",
			zap.String("target", target.config.Name),
			zap.String("host", target.targetURL.Host),
			zap.String("path", remainingPath),
		)
	}

	r.Header.Set(internalPathHeader, remainingPath)

	if target.timeout > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), target.timeout)
		defer cancel()
		r = r.WithContext(ctx)
	}

	target.reverseProxy.ServeHTTP(w, r)
	r.Header.Del(internalPathHeader)
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
	if !mp.config.Load().Logging.AccessLog {
		return
	}
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

// eligible сообщает, можно ли выбрать таргет, НЕ расходуя half-open пробник.
// Истёкшая открытая цепь лениво переводится в half-open со взведённым
// пробником, чтобы последующий acquire мог его забрать.
func (tp *TargetProxy) eligible(cbEnabled bool) bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.eligibleLocked(cbEnabled)
}

func (tp *TargetProxy) eligibleLocked(cbEnabled bool) bool {
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
		return tp.halfOpenProbe.Load()
	}
	return true
}

// acquire подтверждает выбор таргета и расходует half-open пробник. Возвращает
// false, если таргет больше не доступен (например, пробник уже забрал
// параллельный запрос) — тогда вызывающая сторона выбирает заново.
func (tp *TargetProxy) acquire(cbEnabled bool) bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()

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
		if time.Since(tp.cbLastFailure) <= tp.cbTimeout {
			return false
		}
		tp.cbState = stateHalfOpen
		tp.halfOpenProbe.Store(true)
		return tp.halfOpenProbe.CompareAndSwap(true, false)
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

	// Health check уже подтвердил, что таргет отвечает — не ждать
	// оставшийся cbTimeout, а сразу дать пробному запросу шанс закрыть цепь.
	if healthy && tp.cbState == stateOpen {
		tp.cbState = stateHalfOpen
		tp.halfOpenProbe.Store(true)
	}
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

// Reload перезагружает конфигурацию и обновляет targets/routing.
//
// Изменения применяются атомарно: все новые прокси строятся заранее, и только
// если каждый создан успешно, подменяются config/targets/routeConfigs и
// останавливаются старые healthcheck'и. При любой ошибке прежнее состояние
// (конфиг, таргеты, правила, healthcheck'и) остаётся нетронутым.
func (mp *MultiProxy) Reload(cfg *config.Config) error {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	oldTargets := mp.targets
	newTargets := make(map[string]*TargetProxy, len(cfg.Targets))
	for i := range cfg.Targets {
		targetCfg := cfg.Targets[i]
		if old, ok := oldTargets[targetCfg.Name]; ok && !targetChanged(old.config, &targetCfg) {
			old.config = &targetCfg
			newTargets[targetCfg.Name] = old
			continue
		}
		tp, err := mp.createTargetProxy(&targetCfg, cfg.HealthCheck)
		if err != nil {
			// Откат: останавливаем healthcheck'и уже пересозданных прокси,
			// чтобы не течь горутинами, но не трогаем старые.
			for name, created := range newTargets {
				if old, ok := oldTargets[name]; ok && old == created {
					continue
				}
				created.healthCheck.Stop()
			}
			return fmt.Errorf("failed to create proxy for target %s: %w", targetCfg.Name, err)
		}
		newTargets[targetCfg.Name] = tp
	}

	// Все новые прокси готовы — можно безопасно остановить вытесненные.
	for name, old := range oldTargets {
		if next, ok := newTargets[name]; !ok || next != old {
			old.healthCheck.Stop()
		}
	}

	mp.config.Store(cfg)
	mp.targets = newTargets
	mp.rebuildRouteConfigs(cfg)
	mp.reloadGlobalLimiter(cfg)

	mp.logger.Info("Configuration reloaded",
		zap.Int("targets", len(newTargets)),
	)
	return nil
}

// targetChanged сообщает, изменились ли поля, влияющие на построенный прокси.
func targetChanged(old, next *config.TargetConfig) bool {
	if old == nil {
		return true
	}
	return old.URL != next.URL ||
		old.Timeout != next.Timeout ||
		old.HealthCheck != next.HealthCheck ||
		old.EffectiveWeight() != next.EffectiveWeight()
}

// rebuildRouteConfigs пересобирает route-пулы из правил и уже созданных
// прокси (mp.targets). Правила, совпадающие по (host, path_prefix, methods),
// попадают в один RouteConfig: так несколько статических таргетов одного
// роута балансируются взвешенно.
func (mp *MultiProxy) rebuildRouteConfigs(cfg *config.Config) {
	routeConfigs := make([]*RouteConfig, 0, len(cfg.Routing.Rules))
	routeByRule := make(map[*config.RoutingRule]*RouteConfig, len(cfg.Routing.Rules))
	groups := make(map[string]*RouteConfig)

	for i := range cfg.Routing.Rules {
		rule := &cfg.Routing.Rules[i]
		key := config.RuleRouteKey(*rule)
		rc := groups[key]
		if rc == nil {
			rc = &RouteConfig{Rule: rule}
			if rule.RateLimit != nil {
				rc.RateLimit = NewIPRateLimiter(rule.RateLimit.RequestsPerSecond, rule.RateLimit.Burst)
			}
			groups[key] = rc
			routeConfigs = append(routeConfigs, rc)
		}

		if tp := mp.targets[rule.TargetName]; tp != nil {
			weight := tp.config.EffectiveWeight()
			if weight < 0 {
				weight = 0
			}
			rc.Targets = append(rc.Targets, tp)
			rc.weights = append(rc.weights, weight)
			rc.totalWeight += weight
		}
		routeByRule[rule] = rc
	}

	mp.routeConfigs = routeConfigs
	mp.routeByRule = routeByRule
}

func (mp *MultiProxy) Stop(ctx context.Context) error {
	mp.logger.Info("Stopping multi-proxy server")

	for name, target := range mp.targets {
		target.healthCheck.Stop()
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

	// http.Dir отклонит ".." при непосредственной отдаче файла, но эта
	// функция сама делает os.Stat по непроверенному пути — без этой проверки
	// её ответ (SPA fallback vs "нашли flat .html" vs "нашли сам файл")
	// работает как оракул существования файлов за пределами root.
	if strings.Contains(rawPath, "..") {
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
