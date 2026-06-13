package proxy

import (
	"CalfGateway/internal/config"
	"net/http"
	"net/http/httputil"
	"net/url"

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

	p.setupRoutes()
	return p
}
func RateLimitMiddleware(cfg *config.RateLimitConfig) gin.HandlerFunc {
	limiter := rate.NewLimiter(rate.Limit(cfg.Rate), cfg.Burst)

	return func(c *gin.Context) {
		if !limiter.Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			return
		}
		c.Next()
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
		// 路由自己的限流, 没有则 fallback 到全局默认
		rateCfg := &route.RateLimit
		if !route.RateLimit.Enabled {
			rateCfg = &p.config.RateLimit
		}
		if rateCfg.Enabled {
			rl := RateLimitMiddleware(rateCfg)
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
