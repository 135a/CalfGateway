package proxy

import (
	"CalfGateway/internal/config"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/sony/gobreaker"
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
	if cfg.Auth.Enabled {
		p.engine.Use(AuthMiddleware(&cfg.Auth))
	}
	// 网关全局限流 (针对所有进入网关的流量)
	if cfg.RateLimit.Enabled {
		p.engine.Use(GatewayRateLimitMiddleware(&cfg.RateLimit))
	}
	p.setupRoutes()
	return p
}

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
		// 1. 路由维度的全局限流
		if !globalLimiter.Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "route rate limit exceeded",
			})
			return
		}

		// 2. 路由维度的 Per-Client 限流
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

func (p *Proxy) setupRoutes() {
	for _, route := range p.config.Routes {
		r := route // 闭包捕获
		targetUrl, err := url.Parse(r.Target)
		if err != nil {
			panic("invalid target URL:" + r.Target)
		}

		proxy := httputil.NewSingleHostReverseProxy(targetUrl)

		// 自定义 Director 以支持路径重写和 Header 处理
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			// 实现 StripPrefix
			if r.StripPrefix != "" {
				if path := req.URL.Path; len(path) >= len(r.StripPrefix) && path[:len(r.StripPrefix)] == r.StripPrefix {
					req.URL.Path = path[len(r.StripPrefix):]
					if req.URL.Path == "" {
						req.URL.Path = "/"
					}
				}
			}
			// 透传必要的 Header
			req.Header.Set("X-Real-IP", req.RemoteAddr)
		}

		handlers := []gin.HandlerFunc{
			p.reverseProxyHandler(proxy),
		}

		// 仅当路由显式配置了限流时才挂载，避免与网关级全局限流重复
		if r.RateLimit.Enabled {
			rl := RateLimitMiddleware(&r.RateLimit)
			handlers = append([]gin.HandlerFunc{rl}, handlers...)
		}
		if r.Breaker.Enabled {
			cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{Name: r.Name,
				MaxRequests: uint32(r.Breaker.MaxRequests),
				Interval:    r.Breaker.Interval,
				Timeout:     r.Breaker.Timeout,
				ReadyToTrip: func(counts gobreaker.Counts) bool {
					// 自定义触发逻辑
					failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
					return counts.Requests >= uint32(r.Breaker.ErrorThresholdCount) && failureRatio >= r.Breaker.ErrorThresholdPercentage
				},
			})
			cbm := BreakerMiddleware(cb)
			handlers = append(handlers, cbm)
		}
		if len(r.Methods) > 0 {
			for _, method := range r.Methods {
				p.engine.Handle(method, r.Path, handlers...)
			}
		} else {
			p.engine.Any(r.Path, handlers...)
		}
	}
}

func (p *Proxy) reverseProxyHandler(proxy *httputil.ReverseProxy) gin.HandlerFunc {
	return func(c *gin.Context) {
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

func (p *Proxy) Run(addr string) error {
	return p.engine.Run(addr)
}
func BreakerMiddleware(cb *gobreaker.CircuitBreaker) gin.HandlerFunc {
	return func(c *gin.Context) {
		_, err := cb.Execute(
			func() (interface{}, error) {
				c.Next()
				if c.Writer.Status() >= http.StatusInternalServerError {
					return nil, fmt.Errorf("backend service error: %d", c.Writer.Status())
				}
				return nil, nil
			},
		)
		if err != nil {
			if err == gobreaker.ErrOpenState {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
					"error": "service temporary unavaiable (cirsuit breaker open)",
				})
			}
		}
	}
}
