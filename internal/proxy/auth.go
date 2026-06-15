package proxy

import (
	"CalfGateway/internal/config"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func AuthMiddleware(ctg *config.AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, path := range ctg.PublicPaths {
			if strings.HasPrefix(c.Request.URL.Path, path) {
				c.Next()
				return
			}
		}
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is required"})
			return
		}
		parts := strings.SplitN(authHeader, " ", 2)
		if !(len(parts) == 2 && parts[0] == "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header format must be Bearer {token}"})
			return
		}
		tokenString := parts[1]
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			return []byte(ctg.Secret), nil
		})
		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			return
		}
		// 5. 将解析出的 Claims 存入上下文
		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			c.Set("user_claims", claims)
			// 建议：将关键信息通过 Header 透传给后端
			if userId, ok := claims["sub"].(string); ok {
				c.Request.Header.Set("X-User-ID", userId)
			}
		}
		c.Next()

	}
}
