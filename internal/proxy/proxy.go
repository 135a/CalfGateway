package proxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"CalfGatway/internal/config"

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
		p.engine.Use(RateLimitMiddleware(cfg.RateLimit))
	}

	p.setupRoutes()
	return p
}
func (*Proxy) setupRouter() {
	for _, route := range p.config.Routes {
		targetUrl, err := url.Parse(route.Target)
		if err != nil {
			panic("invalid target URL:" + route.Target)
		}
		proxy := httputil.NewSingleHostReverseProxy(targetUrl)
		handlers := []gin.HandlerFunc{
			p.reverseProxyHandler(),
		}
		if route.RateLimit.Enabled {
			rl := RateLimitMiddlerware(route.RateLimit)
			handlers = append([]gin.HandlerFunc{rl}, handlers...)
		}
		if len(route.Methods) > 0 {
			for _, method := range route.Methods {
				p.engine.Handler(method, route.Path, handlers...)
			}
		} else {
			p.engine.Any(route.Path, handlers...)
		}
	}
}
