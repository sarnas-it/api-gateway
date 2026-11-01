package jwtutil

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

type jwksTestKey struct {
	kid string
	key *rsa.PrivateKey
}

func newRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func marshalJWKS(t *testing.T, entries ...jwksTestKey) []byte {
	t.Helper()
	var set jwkSet
	for _, e := range entries {
		set.Keys = append(set.Keys, jwk{
			Kty: "RSA",
			Kid: e.kid,
			Alg: "RS256",
			N:   base64.RawURLEncoding.EncodeToString(e.key.N.Bytes()),
			E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(e.key.E)).Bytes()),
		})
	}
	b, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// jwksTestServer отдаёт JWKS; тело можно подменить на лету (ротация ключей).
func jwksTestServer(t *testing.T) (*httptest.Server, *atomic.Int64, func([]byte)) {
	t.Helper()
	var (
		requests atomic.Int64
		mu       sync.Mutex
		body     []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		mu.Lock()
		b := body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	setBody := func(b []byte) {
		mu.Lock()
		body = b
		mu.Unlock()
	}
	return srv, &requests, setBody
}

func signRS256WithKid(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestJWKSValidator_ValidToken(t *testing.T) {
	key := newRSAKey(t)
	srv, requests, setBody := jwksTestServer(t)
	defer srv.Close()
	setBody(marshalJWKS(t, jwksTestKey{kid: "k1", key: key}))

	v, err := NewJWKSValidator(srv.URL, "RS256", true, false, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	token := signRS256WithKid(t, key, "k1", jwt.MapClaims{
		"sub": "123",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	claims, err := v.ParseAndValidate("Bearer " + token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims["sub"] != "123" {
		t.Fatalf("expected sub=123, got %v", claims["sub"])
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected one JWKS fetch, got %d", got)
	}
}

func TestJWKSValidator_NoKidSingleKeyFallback(t *testing.T) {
	key := newRSAKey(t)
	srv, _, setBody := jwksTestServer(t)
	defer srv.Close()
	setBody(marshalJWKS(t, jwksTestKey{kid: "k1", key: key}))

	v, err := NewJWKSValidator(srv.URL, "RS256", true, false, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	token := signRS256WithKid(t, key, "", jwt.MapClaims{
		"sub": "1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.ParseAndValidate(token); err != nil {
		t.Fatalf("expected single-key fallback to validate, got %v", err)
	}
}

func TestJWKSValidator_WrongKidRefreshesThenFails(t *testing.T) {
	key := newRSAKey(t)
	srv, requests, setBody := jwksTestServer(t)
	defer srv.Close()
	setBody(marshalJWKS(t, jwksTestKey{kid: "good", key: key}))

	v, err := NewJWKSValidator(srv.URL, "RS256", true, false, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	token := signRS256WithKid(t, key, "bad", jwt.MapClaims{
		"sub": "123",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.ParseAndValidate(token); err == nil {
		t.Fatal("expected error for unknown kid")
	}
	if got := requests.Load(); got < 2 {
		t.Fatalf("expected forced refresh on unknown kid (>=2 fetches), got %d", got)
	}
}

func TestJWKSValidator_ExpiredToken(t *testing.T) {
	key := newRSAKey(t)
	srv, _, setBody := jwksTestServer(t)
	defer srv.Close()
	setBody(marshalJWKS(t, jwksTestKey{kid: "k1", key: key}))

	v, err := NewJWKSValidator(srv.URL, "RS256", true, false, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	token := signRS256WithKid(t, key, "k1", jwt.MapClaims{
		"sub": "123",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	if _, err := v.ParseAndValidate(token); err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestJWKSValidator_ExpiredTokenAllowedWhenValidationOff(t *testing.T) {
	key := newRSAKey(t)
	srv, _, setBody := jwksTestServer(t)
	defer srv.Close()
	setBody(marshalJWKS(t, jwksTestKey{kid: "k1", key: key}))

	v, err := NewJWKSValidator(srv.URL, "RS256", false, false, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	token := signRS256WithKid(t, key, "k1", jwt.MapClaims{
		"sub": "123",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	if _, err := v.ParseAndValidate(token); err != nil {
		t.Fatalf("expected no exp validation, got %v", err)
	}
}

func TestJWKSValidator_KidRotation(t *testing.T) {
	oldKey := newRSAKey(t)
	newKey := newRSAKey(t)
	srv, requests, setBody := jwksTestServer(t)
	defer srv.Close()
	setBody(marshalJWKS(t, jwksTestKey{kid: "old", key: oldKey}))

	v, err := NewJWKSValidator(srv.URL, "RS256", true, false, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	// Прогреваем кэш старым ключом.
	oldToken := signRS256WithKid(t, oldKey, "old", jwt.MapClaims{
		"sub": "1", "exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.ParseAndValidate(oldToken); err != nil {
		t.Fatalf("old key must validate before rotation: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected one fetch after warmup, got %d", got)
	}

	// Провайдер ротирует ключ: старый kid исчезает, появляется новый.
	setBody(marshalJWKS(t, jwksTestKey{kid: "new", key: newKey}))

	newToken := signRS256WithKid(t, newKey, "new", jwt.MapClaims{
		"sub": "2", "exp": time.Now().Add(time.Hour).Unix(),
	})
	claims, err := v.ParseAndValidate(newToken)
	if err != nil {
		t.Fatalf("rotated key must validate via forced refresh: %v", err)
	}
	if claims["sub"] != "2" {
		t.Fatalf("expected sub=2, got %v", claims["sub"])
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("expected a forced refresh after rotation (2 fetches), got %d", got)
	}
}

func TestJWKSValidator_AlgMismatchRejected(t *testing.T) {
	key := newRSAKey(t)
	srv, _, setBody := jwksTestServer(t)
	defer srv.Close()
	setBody(marshalJWKS(t, jwksTestKey{kid: "k1", key: key}))

	v, err := NewJWKSValidator(srv.URL, "RS256", true, false, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	hsToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "123",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	s, err := hsToken.SignedString([]byte("attacker-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.ParseAndValidate(s); err == nil {
		t.Fatal("expected error for HS256 token against RS256 jwks validator")
	}
}

func TestNewJWKSValidator_RejectsHMAC(t *testing.T) {
	if _, err := NewJWKSValidator("http://jwks", "HS256", true, false, "", zap.NewNop()); err == nil {
		t.Fatal("expected error for HS256 with jwks_url")
	}
}

func TestJWKSValidator_ConcurrentUnknownKidSingleFetch(t *testing.T) {
	key := newRSAKey(t)
	srv, requests, setBody := jwksTestServer(t)
	defer srv.Close()
	setBody(marshalJWKS(t, jwksTestKey{kid: "good", key: key}))

	v, err := NewJWKSValidator(srv.URL, "RS256", true, false, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	// Прогреваем кэш валидным токеном.
	prime := signRS256WithKid(t, key, "good", jwt.MapClaims{
		"sub": "1", "exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.ParseAndValidate(prime); err != nil {
		t.Fatalf("warmup token must validate: %v", err)
	}
	baseline := requests.Load()

	// 10 параллельных проверок с неизвестным kid не должны устроить шторм:
	// ровно одно принудительное обновление на всех.
	badToken := signRS256WithKid(t, key, "bad", jwt.MapClaims{
		"sub": "2", "exp": time.Now().Add(time.Hour).Unix(),
	})

	const goroutines = 10
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = v.ParseAndValidate(badToken)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Fatalf("goroutine %d: expected error for unknown kid", i)
		}
	}
	if got := requests.Load() - baseline; got != 1 {
		t.Fatalf("expected exactly one forced JWKS fetch for %d concurrent unknown-kid checks, got %d", goroutines, got)
	}
}

func TestJWKSValidator_UnknownKidNegativeCache(t *testing.T) {
	key := newRSAKey(t)
	srv, requests, setBody := jwksTestServer(t)
	defer srv.Close()
	setBody(marshalJWKS(t, jwksTestKey{kid: "good", key: key}))

	v, err := NewJWKSValidator(srv.URL, "RS256", true, false, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	prime := signRS256WithKid(t, key, "good", jwt.MapClaims{
		"sub": "1", "exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.ParseAndValidate(prime); err != nil {
		t.Fatalf("warmup token must validate: %v", err)
	}
	afterWarmup := requests.Load()

	badToken := signRS256WithKid(t, key, "bad", jwt.MapClaims{
		"sub": "2", "exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.ParseAndValidate(badToken); err == nil {
		t.Fatal("expected error for unknown kid")
	}
	afterFirst := requests.Load()
	if afterFirst-afterWarmup != 1 {
		t.Fatalf("expected one forced fetch on first unknown kid, got %d", afterFirst-afterWarmup)
	}

	// В пределах cooldown повторная проверка того же kid не фетчит снова.
	if _, err := v.ParseAndValidate(badToken); err == nil {
		t.Fatal("expected error for unknown kid on retry")
	}
	if got := requests.Load(); got != afterFirst {
		t.Fatalf("expected no additional JWKS fetch for repeated unknown kid, got %d extra", got-afterFirst)
	}
}

func TestJWKSValidator_ColdStartDownEndpointSingleAttempt(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	key := newRSAKey(t)
	v, err := NewJWKSValidator(srv.URL, "RS256", true, false, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	token := signRS256WithKid(t, key, "k1", jwt.MapClaims{
		"sub": "1", "exp": time.Now().Add(time.Hour).Unix(),
	})

	start := time.Now()
	if _, err := v.ParseAndValidate(token); err == nil {
		t.Fatal("expected error for down JWKS endpoint")
	}
	if elapsed := time.Since(start); elapsed > jwksHTTPTimeout {
		t.Fatalf("cold-start failure took %v (expected a single fast attempt, budget %v)", elapsed, jwksHTTPTimeout)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected exactly one JWKS attempt on cold start, got %d", got)
	}
}

func TestJWKSValidator_ConcurrentColdStartDownEndpointSingleFetch(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	key := newRSAKey(t)
	v, err := NewJWKSValidator(srv.URL, "RS256", true, false, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	token := signRS256WithKid(t, key, "k1", jwt.MapClaims{
		"sub": "1", "exp": time.Now().Add(time.Hour).Unix(),
	})

	const goroutines = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := v.ParseAndValidate(token); err == nil {
				t.Error("expected error for down JWKS endpoint")
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := requests.Load(); got != 1 {
		t.Fatalf("expected concurrent cold-start failures to share one JWKS attempt, got %d", got)
	}
}

func TestJWKSValidator_DownEndpointNegativeCache(t *testing.T) {
	// Последовательные запросы при недоступном IdP не должны ходить в сеть
	// на каждый вызов: иначе падение IdP превращается в очередь запросов,
	// каждый из которых ждёт jwksHTTPTimeout.
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	key := newRSAKey(t)
	v, err := NewJWKSValidator(srv.URL, "RS256", true, false, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	token := signRS256WithKid(t, key, "k1", jwt.MapClaims{
		"sub": "1", "exp": time.Now().Add(time.Hour).Unix(),
	})

	for i := 0; i < 5; i++ {
		if _, err := v.ParseAndValidate(token); err == nil {
			t.Fatal("expected error for down JWKS endpoint")
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected one JWKS attempt inside the negative-cache window, got %d", got)
	}
}
