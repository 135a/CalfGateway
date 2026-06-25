package middleware

import (
	"CalfGateway/internal/config"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

func GatewayRateLimitMiddleware(cfg *config.RateLimitConfig) gin.HandlerFunc {
	limiter := rate.NewLimiter(rate.Limit(cfg.Global.Rate), cfg.Global.Burst)
	return func(c *gin.Context) {
		if !limiter.Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "gateway rate limit exceeded",
			})
			return
		}
		c.Next()
	}
}

func RateLimitMiddleware(cfg *config.RateLimitConfig) gin.HandlerFunc {
	globalLimiter := rate.NewLimiter(rate.Limit(cfg.Global.Rate), cfg.Global.Burst)
	var clientLimiters sync.Map

	return func(c *gin.Context) {
		if !globalLimiter.Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "route rate limit exceeded",
			})
			return
		}

		if cfg.PerClient.Rate > 0 {
			ip := c.ClientIP()
			limiterIface, _ := clientLimiters.LoadOrStore(ip, rate.NewLimiter(
				rate.Limit(cfg.PerClient.Rate), cfg.PerClient.Burst,
			))
			if !limiterIface.(*rate.Limiter).Allow() {
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
					"error": "per-client rate limit exceeded",
				})
				return
			}
		}
		c.Next()
	}
}
