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

// jwksCache — потокобезопасный кэш JWK Set с TTL и принудительным обновлением.
type jwksCache struct {
	url    string
	client *http.Client
	logger *zap.Logger

	mu        sync.RWMutex
	keys      *jwksKeys
	expiresAt time.Time
}

func newJWKSCache(url string, logger *zap.Logger) *jwksCache {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &jwksCache{
		url:    url,
		client: &http.Client{Timeout: jwksHTTPTimeout},
		logger: logger,
	}
}

// key возвращает публичный ключ для проверки подписи. При неизвестном kid
// выполняется одно принудительное обновление набора (поддержка ротации ключей).
func (c *jwksCache) key(kid string) (interface{}, error) {
	keys, err := c.get(false)
	if err == nil {
		if k, ok := keys.selectKey(kid); ok {
			return k, nil
		}
	}

	refreshed, refreshErr := c.get(true)
	if refreshErr != nil {
		if err != nil {
			return nil, err
		}
		return nil, refreshErr
	}
	if k, ok := refreshed.selectKey(kid); ok {
		return k, nil
	}
	return nil, fmt.Errorf("jwks: no key for kid %q", kid)
}

func (c *jwksCache) get(force bool) (*jwksKeys, error) {
	c.mu.RLock()
	if !force && c.keys != nil && time.Now().Before(c.expiresAt) {
		keys := c.keys
		c.mu.RUnlock()
		return keys, nil
	}
	c.mu.RUnlock()
	return c.refresh(force)
}

func (c *jwksCache) refresh(force bool) (*jwksKeys, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !force && c.keys != nil && time.Now().Before(c.expiresAt) {
		return c.keys, nil
	}

	keys, err := c.fetch()
	if err != nil {
		if c.keys != nil {
			c.logger.Warn("jwks: refresh failed, using cached keys",
				zap.String("url", c.url), zap.Error(err))
			return c.keys, nil
		}
		return nil, err
	}

	c.keys = keys
	c.expiresAt = time.Now().Add(jwksCacheTTL)
	c.logger.Debug("jwks: keys loaded",
		zap.String("url", c.url), zap.Int("keys", len(keys.byKid)))
	return keys, nil
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
