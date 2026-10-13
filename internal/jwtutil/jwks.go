package jwtutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	// jwksCacheTTL — время жизни кэша JWK Set; после истечения набор
	// перезагружается при первой же валидации.
	jwksCacheTTL = 5 * time.Minute
	// jwksHTTPTimeout — таймаут запроса за JWKS.
	jwksHTTPTimeout = 5 * time.Second
	// jwksMaxBodySize — предел размера ответа JWKS.
	jwksMaxBodySize = 1 << 20
	// jwksUnknownKidCooldown — окно, в течение которого повторные проверки с
	// тем же неизвестным kid не инициируют новое принудительное обновление.
	jwksUnknownKidCooldown = 30 * time.Second
	// jwksNegativeCacheMax — предел размера negative-кэша неизвестных kid.
	jwksNegativeCacheMax = 1024
)

// jwk — отдельный ключ JWK (RFC 7517/7518). Поддерживаются RSA и EC.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// jwkSet — JWK Set: {"keys":[...]}.
type jwkSet struct {
	Keys []jwk `json:"keys"`
}

// jwksKeys — разобранный набор публичных ключей, индексированный по kid.
type jwksKeys struct {
	byKid map[string]interface{}
}

// selectKey выбирает ключ по kid из заголовка токена. Если kid пуст
// (некоторые провайдеры его не проставляют) и в наборе ровно один ключ —
// используется он (fallback: single-key set).
func (s *jwksKeys) selectKey(kid string) (interface{}, bool) {
	if kid == "" {
		if len(s.byKid) == 1 {
			for _, k := range s.byKid {
				return k, true
			}
		}
		k, ok := s.byKid[""]
		return k, ok
	}
	k, ok := s.byKid[kid]
	return k, ok
}

// jwksCache — потокобезопасный кэш JWK Set с TTL, дедупликацией сетевых
// запросов и negative-кэшем неизвестных kid.
type jwksCache struct {
	url    string
	client *http.Client
	logger *zap.Logger

	// fetchMu сериализует сетевые запросы за JWKS: одновременно выполняется не
	// более одного. Он НЕ удерживается при чтении кэша, поэтому валидные
	// запросы с актуальным набором не блокируются обновлением.
	fetchMu sync.Mutex

	mu        sync.RWMutex
	keys      *jwksKeys
	expiresAt time.Time
	gen       uint64               // растёт после каждой попытки загрузки
	lastErr   error                // результат последней неудачной попытки
	neg       map[string]time.Time // kid -> до какого времени не форсить refresh
}

func newJWKSCache(url string, logger *zap.Logger) *jwksCache {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &jwksCache{
		url:    url,
		client: &http.Client{Timeout: jwksHTTPTimeout},
		logger: logger,
		neg:    make(map[string]time.Time),
	}
}

// key возвращает публичный ключ для проверки подписи. При неизвестном kid
// выполняется не более одной принудительной попытки обновления (поддержка
// ротации ключей); повторные попытки для того же kid в пределах cooldown-окна
// подавляются. Сетевые запросы дедуплицируются между горутинами.
func (c *jwksCache) key(kid string) (interface{}, error) {
	keys, err := c.ensureLoaded(false)
	if err != nil {
		return nil, err
	}
	if k, ok := keys.selectKey(kid); ok {
		return k, nil
	}

	if c.unknownKidCoolingDown(kid) {
		return nil, fmt.Errorf("jwks: no key for kid %q", kid)
	}

	refreshed, refreshErr := c.ensureLoaded(true)
	if refreshErr == nil {
		if k, ok := refreshed.selectKey(kid); ok {
			return k, nil
		}
	}
	c.markUnknownKid(kid)
	return nil, fmt.Errorf("jwks: no key for kid %q", kid)
}

// ensureLoaded возвращает набор ключей из кэша, при необходимости загружая его.
// force=true инициирует загрузку даже при свежем кэше (ротация по kid).
//
// Дедупликация: параллельные вызовы разделяют один сетевой запрос через
// сериализацию fetchMu + счётчик поколений gen. При холодном кэше (ключей нет)
// выполняется ровно одна попытка загрузки, даже при ошибке — второй попытки в
// рамках одного вызова не делается.
func (c *jwksCache) ensureLoaded(force bool) (*jwksKeys, error) {
	c.mu.RLock()
	keys := c.keys
	fresh := keys != nil && time.Now().Before(c.expiresAt)
	gen := c.gen
	c.mu.RUnlock()

	if fresh && !force {
		return keys, nil
	}

	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	// Пока ждали fetchMu, другой вызов мог уже сходить за JWKS.
	c.mu.RLock()
	keys = c.keys
	fresh = keys != nil && time.Now().Before(c.expiresAt)
	curGen := c.gen
	lastErr := c.lastErr
	c.mu.RUnlock()

	if curGen != gen {
		// Кто-то завершил загрузку после нашей выборки — переиспользуем результат.
		if keys != nil {
			return keys, nil
		}
		if lastErr != nil {
			return nil, lastErr
		}
	}

	if fresh && !force {
		return keys, nil
	}

	newKeys, err := c.fetch()
	if err != nil {
		c.mu.Lock()
		c.gen++
		c.lastErr = err
		c.mu.Unlock()
		if keys != nil {
			c.logger.Warn("jwks: refresh failed, using cached keys",
				zap.String("url", c.url), zap.Error(err))
			return keys, nil
		}
		return nil, err
	}

	c.mu.Lock()
	c.keys = newKeys
	c.expiresAt = time.Now().Add(jwksCacheTTL)
	c.gen++
	c.lastErr = nil
	c.mu.Unlock()

	c.logger.Debug("jwks: keys loaded",
		zap.String("url", c.url), zap.Int("keys", len(newKeys.byKid)))
	return newKeys, nil
}

// unknownKidCoolingDown сообщает, что для kid недавно уже была неудачная
// принудительная попытка и новую делать не нужно.
func (c *jwksCache) unknownKidCoolingDown(kid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.neg[kid]
	if !ok {
		return false
	}
	if time.Now().Before(until) {
		return true
	}
	delete(c.neg, kid)
	return false
}

func (c *jwksCache) markUnknownKid(kid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.neg) >= jwksNegativeCacheMax {
		c.neg = make(map[string]time.Time)
	}
	c.neg[kid] = time.Now().Add(jwksUnknownKidCooldown)
}

func (c *jwksCache) fetch() (*jwksKeys, error) {
	req, err := http.NewRequest(http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("jwks: build request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwks: fetch %s: %w", c.url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: %s returned %d", c.url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, jwksMaxBodySize))
	if err != nil {
		return nil, fmt.Errorf("jwks: read body: %w", err)
	}

	var set jwkSet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("jwks: decode: %w", err)
	}
	return parseJWKSet(set)
}

func parseJWKSet(set jwkSet) (*jwksKeys, error) {
	keys := &jwksKeys{byKid: make(map[string]interface{}, len(set.Keys))}
	for _, k := range set.Keys {
		if k.Use == "enc" {
			continue
		}
		pub, err := parseJWK(k)
		if err != nil {
			return nil, err
		}
		keys.byKid[k.Kid] = pub
	}
	if len(keys.byKid) == 0 {
		return nil, fmt.Errorf("jwks: no usable keys")
	}
	return keys, nil
}

func parseJWK(k jwk) (interface{}, error) {
	switch k.Kty {
	case "RSA":
		n, err := decodeJWKInt(k.N)
		if err != nil {
			return nil, fmt.Errorf("jwks: rsa modulus: %w", err)
		}
		e, err := decodeJWKInt(k.E)
		if err != nil {
			return nil, fmt.Errorf("jwks: rsa exponent: %w", err)
		}
		if !e.IsInt64() || e.Int64() <= 0 {
			return nil, fmt.Errorf("jwks: invalid rsa exponent")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		curve, err := jwksCurve(k.Crv)
		if err != nil {
			return nil, err
		}
		x, err := decodeJWKInt(k.X)
		if err != nil {
			return nil, fmt.Errorf("jwks: ec x: %w", err)
		}
		y, err := decodeJWKInt(k.Y)
		if err != nil {
			return nil, fmt.Errorf("jwks: ec y: %w", err)
		}
		return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
	default:
		return nil, fmt.Errorf("jwks: unsupported kty %q", k.Kty)
	}
}

func decodeJWKInt(s string) (*big.Int, error) {
	if s == "" {
		return nil, fmt.Errorf("empty value")
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.URLEncoding.DecodeString(s)
		if err != nil {
			return nil, err
		}
	}
	return new(big.Int).SetBytes(b), nil
}

func jwksCurve(crv string) (elliptic.Curve, error) {
	switch crv {
	case "P-256":
		return elliptic.P256(), nil
	case "P-384":
		return elliptic.P384(), nil
	case "P-521":
		return elliptic.P521(), nil
	default:
		return nil, fmt.Errorf("jwks: unsupported curve %q", crv)
	}
}

// NewJWKSValidator создаёт валидатор, проверяющий подпись по публичным ключам
// из удалённого JWK Set. Поддерживаются только асимметричные алгоритмы RS*/ES*.
func NewJWKSValidator(jwksURL, algorithm string,
	validateExp, validateIss bool, expectedIss string,
	logger *zap.Logger,
) (*JWTValidator, error) {
	if jwksURL == "" {
		return nil, fmt.Errorf("jwks url is required")
	}
	if len(algorithm) < 2 {
		return nil, fmt.Errorf("invalid algorithm: %s", algorithm)
	}

	switch prefix := strings.ToUpper(algorithm[:2]); prefix {
	case "RS", "ES":
	default:
		return nil, fmt.Errorf("algorithm %s is not supported with jwks_url", algorithm)
	}

	return &JWTValidator{
		algorithm:   algorithm,
		validateExp: validateExp,
		validateIss: validateIss,
		expectedIss: expectedIss,
		jwks:        newJWKSCache(jwksURL, logger),
	}, nil
}
