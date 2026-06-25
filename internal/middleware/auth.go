package middleware

import (
	"CalfGateway/internal/config"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func AuthMiddleware(ctg *config.AuthConfig, rdb *redis.Client) gin.HandlerFunc {
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
		
		userId, err := rdb.Get(c.Request.Context(), "session:"+tokenString).Result()
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			return
		}
		
		c.Set("user_id", userId)
		c.Request.Header.Set("X-User-ID", userId)
		
		c.Next()
	}
}
