package middleware

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/sony/gobreaker"
)

// BreakerMiddleware 熔断中间件：熔断打开时返回 503，不执行降级策略
func BreakerMiddleware(cb *gobreaker.CircuitBreaker) gin.HandlerFunc {
	return func(c *gin.Context) {
		_, err := cb.Execute(func() (interface{}, error) {
			c.Next()
			if c.Writer.Status() >= http.StatusInternalServerError {
				return nil, fmt.Errorf("backend service error: %d", c.Writer.Status())
			}
			return nil, nil
		})

		if err == gobreaker.ErrOpenState {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "service temporary unavailable (circuit breaker open)",
			})
		}
	}
}
