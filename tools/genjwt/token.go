package genjwt

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Generate 使用 HS256 签发 JWT，算法与网关 AuthMiddleware 一致。
func Generate(secret, sub string, ttl time.Duration) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": sub,
		"exp": time.Now().Add(ttl).Unix(),
	})
	return token.SignedString([]byte(secret))
}

// Parse 校验 token 是否可被指定 secret 正确解析。
func Parse(tokenString, secret string) (*jwt.Token, error) {
	return jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	})
}
