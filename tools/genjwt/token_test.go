package genjwt

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestGenerate_ValidToken(t *testing.T) {
	secret := ""
	sub := "user-123"

	s, err := Generate(secret, sub, 2*time.Hour)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if s == "" {
		t.Fatal("Generate() returned empty token")
	}

	token, err := Parse(s, secret)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !token.Valid {
		t.Fatal("token should be valid")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("claims should be MapClaims")
	}
	if claims["sub"] != sub {
		t.Fatalf("sub = %v, want %v", claims["sub"], sub)
	}
}

func TestGenerate_WrongSecretRejected(t *testing.T) {
	s, err := Generate("", "user-123", 2*time.Hour)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	token, err := Parse(s, "wrong-secret")
	if err == nil && token.Valid {
		t.Fatal("token signed with empty secret should not validate with wrong-secret")
	}
}

func TestGenerate_ExpiredTokenRejected(t *testing.T) {
	secret := ""
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "user-123",
		"exp": time.Now().Add(-1 * time.Hour).Unix(),
	})
	s, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}

	parsed, err := Parse(s, secret)
	if err == nil && parsed.Valid {
		t.Fatal("expired token should not be valid")
	}
}
