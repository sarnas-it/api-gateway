package jwtutil

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrNoToken          = errors.New("no token provided")
	ErrInvalidToken     = errors.New("invalid token")
	ErrInvalidAlgorithm = errors.New("invalid signing algorithm")
)

// JWTValidator валидатор JWT токенов
type JWTValidator struct {
	secretKey   []byte
	publicKey   interface{}
	algorithm   string
	validateExp bool
	validateIss bool
	expectedIss string
	validateAud bool
	expectedAud string
}

// NewJWTValidator создает новый валидатор JWT
func NewJWTValidator(secretKey, algorithm string,
	validateExp, validateIss bool, expectedIss string,
	validateAud bool, expectedAud string,
	publicKeyFile string,
) (*JWTValidator, error) {

	v := &JWTValidator{
		algorithm:   algorithm,
		validateExp: validateExp,
		validateIss: validateIss,
		expectedIss: expectedIss,
		validateAud: validateAud,
		expectedAud: expectedAud,
	}

	prefix := strings.ToUpper(algorithm[:2])

	switch {
	case prefix == "HS" || algorithm == "HS256" || algorithm == "HS384" || algorithm == "HS512":
		if secretKey == "" {
			return nil, fmt.Errorf("secret key required for HMAC algorithm")
		}
		v.secretKey = []byte(secretKey)

	case prefix == "RS" || algorithm == "RS256" || algorithm == "RS384" || algorithm == "RS512":
		key, err := loadPublicKey(publicKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load RSA public key: %w", err)
		}
		rsaKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("key file is not an RSA public key")
		}
		v.publicKey = rsaKey

	case prefix == "ES" || algorithm == "ES256" || algorithm == "ES384" || algorithm == "ES512":
		key, err := loadPublicKey(publicKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load ECDSA public key: %w", err)
		}
		ecKey, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("key file is not an ECDSA public key")
		}
		v.publicKey = ecKey

	case algorithm == "EdDSA" || algorithm == "ED25519":
		key, err := loadPublicKey(publicKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load Ed25519 public key: %w", err)
		}
		edKey, ok := key.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("key file is not an Ed25519 public key")
		}
		v.publicKey = edKey

	default:
		return nil, fmt.Errorf("unsupported algorithm: %s", algorithm)
	}

	return v, nil
}

// ParseAndValidate парсит и валидирует JWT токен
func (v *JWTValidator) ParseAndValidate(tokenString string) (jwt.MapClaims, error) {
	if tokenString == "" {
		return nil, ErrNoToken
	}

	tokenString = strings.TrimPrefix(tokenString, "Bearer ")

	token, err := jwt.Parse(tokenString, v.keyFunc, v.withValidOptions()...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	if !token.Valid {
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, ErrInvalidToken
	}

	return claims, nil
}

// withValidOptions возвращает опции валидации
func (v *JWTValidator) withValidOptions() []jwt.ParserOption {
	var opts []jwt.ParserOption

	if !v.validateExp {
		opts = append(opts, jwt.WithoutClaimsValidation())
	}

	opts = append(opts, jwt.WithValidMethods([]string{v.algorithm}))

	return opts
}

// keyFunc возвращает ключ для проверки подписи
func (v *JWTValidator) keyFunc(token *jwt.Token) (interface{}, error) {
	switch {
	case v.secretKey != nil:
		return v.secretKey, nil
	case v.publicKey != nil:
		return v.publicKey, nil
	}
	return nil, fmt.Errorf("no key available")
}

// ValidateClaims выполняет дополнительную валидацию claims
func (v *JWTValidator) ValidateClaims(claims jwt.MapClaims) error {
	// Проверка issuer
	if v.validateIss {
		iss, ok := claims["iss"].(string)
		if !ok {
			return fmt.Errorf("iss claim missing")
		}
		if iss != v.expectedIss {
			return fmt.Errorf("invalid issuer: expected %s, got %s", v.expectedIss, iss)
		}
	}

	// Проверка audience
	if v.validateAud {
		audMatch := false
		if audStr, ok := claims["aud"].(string); ok {
			audMatch = audStr == v.expectedAud
		} else if audSlice, ok := claims["aud"].([]interface{}); ok {
			for _, a := range audSlice {
				if s, ok := a.(string); ok && s == v.expectedAud {
					audMatch = true
					break
				}
			}
		}
		if !audMatch {
			return fmt.Errorf("invalid audience")
		}
	}

	return nil
}

// CheckRoles проверяет роли из claims по схеме (any of anyOf) AND (all of allOf).
// Пустые оба списка — проверка не нужна. Отсутствующий/нестроковый/не-массивный
// claim или массив с нестроковыми элементами — ошибка (fail closed).
func CheckRoles(claims jwt.MapClaims, anyOf, allOf []string) error {
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

// ExtractClaims извлекает значения из claims
func ExtractClaims(claims jwt.MapClaims, mappings []string) map[string]interface{} {
	result := make(map[string]interface{})

	for _, key := range mappings {
		if val, ok := claims[key]; ok {
			result[key] = val
		}
	}

	return result
}

// loadPublicKey загружает публичный ключ из PEM файла
func loadPublicKey(path string) (interface{}, error) {
	pemData, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}

	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("no PEM data found in %s", path)
	}

	switch block.Type {
	case "PUBLIC KEY":
		return x509.ParsePKIXPublicKey(block.Bytes)
	case "RSA PUBLIC KEY":
		return x509.ParsePKCS1PublicKey(block.Bytes)
	case "CERTIFICATE":
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}
		return cert.PublicKey, nil
	default:
		return nil, fmt.Errorf("unsupported PEM block type: %s", block.Type)
	}
}
