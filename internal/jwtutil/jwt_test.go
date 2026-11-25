package jwtutil

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func newTestValidator(t *testing.T) *JWTValidator {
	t.Helper()
	v, err := NewJWTValidator("test-secret", "HS256", true, false, "", false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func signToken(claims jwt.MapClaims, secret string) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, _ := token.SignedString([]byte(secret))
	return s
}

func TestParseAndValidate_ValidToken(t *testing.T) {
	v := newTestValidator(t)
	token := signToken(jwt.MapClaims{
		"sub":   "123",
		"email": "test@test.com",
		"roles": []interface{}{"user"},
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	}, "test-secret")

	claims, err := v.ParseAndValidate("Bearer " + token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims["sub"] != "123" {
		t.Errorf("expected sub=123, got %v", claims["sub"])
	}
}

func TestParseAndValidate_ExpiredToken(t *testing.T) {
	v := newTestValidator(t)
	token := signToken(jwt.MapClaims{
		"sub": "123",
		"exp": float64(time.Now().Add(-1 * time.Hour).Unix()),
	}, "test-secret")

	_, err := v.ParseAndValidate("Bearer " + token)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

// Токен без exp при включённой проверке срока невалиден: бессрочный токен
// не должен проходить на защищённом маршруте.
func TestParseAndValidate_MissingExp(t *testing.T) {
	v := newTestValidator(t)
	token := signToken(jwt.MapClaims{
		"sub": "123",
	}, "test-secret")

	_, err := v.ParseAndValidate("Bearer " + token)
	if err == nil {
		t.Fatal("expected error for token without exp")
	}
}

func TestParseAndValidate_WrongSecret(t *testing.T) {
	v := newTestValidator(t)
	token := signToken(jwt.MapClaims{
		"sub": "123",
		"exp": float64(time.Now().Add(1 * time.Hour).Unix()),
	}, "wrong-secret")

	_, err := v.ParseAndValidate("Bearer " + token)
	if err == nil {
		t.Fatal("expected error for wrong secret")
	}
}

func TestParseAndValidate_NoToken(t *testing.T) {
	v := newTestValidator(t)
	_, err := v.ParseAndValidate("")
	if err != ErrNoToken {
		t.Fatalf("expected ErrNoToken, got %v", err)
	}
}

func TestExtractClaims(t *testing.T) {
	claims := jwt.MapClaims{
		"id":    "42",
		"email": "user@test.com",
	}
	result := ExtractClaims(claims, []string{"id", "email", "missing"})
	if result["id"] != "42" {
		t.Errorf("expected id=42, got %v", result["id"])
	}
	if result["email"] != "user@test.com" {
		t.Errorf("expected email, got %v", result["email"])
	}
	if _, ok := result["missing"]; ok {
		t.Error("expected missing to not be present")
	}
}

func TestValidateClaims_Issuer(t *testing.T) {
	v, err := NewJWTValidator("secret", "HS256", false, true, "myapp", false, "", "")
	if err != nil {
		t.Fatal(err)
	}

	token := signToken(jwt.MapClaims{
		"sub": "123",
		"iss": "myapp",
	}, "secret")

	claims, err := v.ParseAndValidate("Bearer " + token)
	if err != nil {
		t.Fatal(err)
	}

	if err := v.ValidateClaims(claims); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestValidateClaims_WrongIssuer(t *testing.T) {
	v, err := NewJWTValidator("secret", "HS256", false, true, "myapp", false, "", "")
	if err != nil {
		t.Fatal(err)
	}

	token := signToken(jwt.MapClaims{
		"sub": "123",
		"iss": "other",
	}, "secret")

	claims, err := v.ParseAndValidate("Bearer " + token)
	if err != nil {
		t.Fatal(err)
	}

	if err := v.ValidateClaims(claims); err == nil {
		t.Fatal("expected error for wrong issuer")
	}
}

func TestWithoutExpValidation(t *testing.T) {
	v, err := NewJWTValidator("secret", "HS256", false, false, "", false, "", "")
	if err != nil {
		t.Fatal(err)
	}

	token := signToken(jwt.MapClaims{
		"sub": "123",
		"exp": float64(time.Now().Add(-1 * time.Hour).Unix()),
	}, "secret")

	_, err = v.ParseAndValidate("Bearer " + token)
	if err != nil {
		t.Fatalf("expected no exp validation, got %v", err)
	}
}
