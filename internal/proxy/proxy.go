package proxy

import (
	"CalfGateway/internal/config"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

type Proxy struct {
	config *config.Config
	engine *gin.Engine
}

func NewProxy(cfg *config.Config) *Proxy {
	p := &Proxy{
		config: cfg,
		engine: gin.Default(),
	}

	// Add global rate limit middleware if enabled at top level
	if cfg.RateLimit.Enabled {
		p.engine.Use(RateLimitMiddleware(&cfg.RateLimit))
	}

	p.setupRoutes()
	return p
}
func RateLimitMiddleware(ctg *config.RateLimitConfig) gin.HandlerFunc {
	var globalLimiter *rate.Limiter
	if ctg.Global.Rate > 0 {
		globalLimiter = rate.NewLimiter(rate.Limit(ctg.Global.Rate), ctg.Global.Burst)
	}
	var (
		clientLimiters sync.Map
		perClient      *rate.Limiter
	)
	if ctg.PerClient.Rate > 0 {
		perClient = rate.NewLimiter(rate.Limit(ctg.PerClient.Rate), ctg.PerClient.Burst)
	}

	return func(c *gin.Context) {
		if globalLimiter != nil && !globalLimiter.Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "rate limit exceeded",
			})
		}
		return
		if perClient != nil {
			ip := c.ClientIP()
			limiterIface, _ := clientLimiters.LoadOrStore(ip, rate.NewLimiter(
				rate.Limit(ctg.PerClient.Rate), ctg.PerClient.Burst,
			))
			clientLimiter := limiterIface.(*rate.Limiter)
			if !clientLimiter.Allow() {
				c.AbortWithStatusJSON(http.StatusTooManyRequests,
					gin.H{
						"error": "per-client rate limit exceeded",
					})
				return
			}
		}

	}
}
func (p *Proxy) setupRoutes() {
	for _, route := range p.config.Routes {
		targetUrl, err := url.Parse(route.Target)
		if err != nil {
			panic("invalid target URL:" + route.Target)
		}
		proxy := httputil.NewSingleHostReverseProxy(targetUrl)
		handlers := []gin.HandlerFunc{
			p.reverseProxyHandler(proxy),
		}
		if route.RateLimit.Enabled {
			rl := RateLimitMiddleware(&route.RateLimit)
			handlers = append([]gin.HandlerFunc{rl}, handlers...)
		}
		if len(route.Methods) > 0 {
			for _, method := range route.Methods {
				p.engine.Handle(method, route.Path, handlers...)
			}
		} else {
			p.engine.Any(route.Path, handlers...)
		}
	}
}
func (p *Proxy) reverseProxyHandler(proxy *httputil.ReverseProxy) gin.HandlerFunc {
	return func(c *gin.Context) {
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}
