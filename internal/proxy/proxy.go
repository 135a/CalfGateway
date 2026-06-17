package proxy

import (
	"CalfGateway/internal/config"
	"CalfGateway/internal/degradation"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sony/gobreaker"
	"golang.org/x/time/rate"
)

type Proxy struct {
	config      *config.Config
	engine      *gin.Engine
	degManager  *degradation.Manager
	degRecorder *degradation.Recorder
}

func NewProxy(cfg *config.Config) *Proxy {
	p := &Proxy{
		config:      cfg,
		engine:      gin.Default(),
		degManager:  degradation.NewManager(),
		degRecorder: degradation.NewRecorder(),
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
		proxy.Transport = &http.Transport{
			MaxIdleConns:        p.config.Proxy.MaxIdleConns,
			MaxIdleConnsPerHost: p.config.Proxy.MaxIdleConnsPerHost,
			MaxConnsPerHost:     p.config.Proxy.MaxConnsPerHost,
			IdleConnTimeout:     90 * time.Second,
		}

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

		// 解析降级配置：路由级覆盖全局
		var degCfg *config.DegradationConfig
		if r.Degradation != nil {
			degCfg = r.Degradation
		} else if p.config.Degradation.Enabled {
			degCfg = &p.config.Degradation
		}

		// 解析降级策略
		var degStrategy degradation.Strategy
		if degCfg != nil && degCfg.Enabled {
			switch degCfg.Strategy {
			case "static_response":
				degStrategy = degradation.NewStaticResponseStrategy(
					degCfg.Static.StatusCode,
					degCfg.Static.Headers,
					degCfg.Static.Body,
				)
			case "cache":
				degStrategy = degradation.NewCacheStrategy(
					degCfg.Cache.TTL,
					degCfg.Cache.MaxEntries,
					degCfg.Cache.CacheableStatuses,
				)
			}
			if degStrategy != nil {
				p.degManager.Register(degStrategy)
			}
		}

		handlers := []gin.HandlerFunc{
			p.reverseProxyHandler(proxy),
		}

		// 缓存降级策略需要捕获响应体，使用特殊处理器
		if cs, ok := degStrategy.(*degradation.CacheStrategy); ok {
			handlers = []gin.HandlerFunc{
				p.cachedReverseProxyHandler(proxy, cs),
			}
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
			cbm := BreakerWithDegradationMiddleware(cb, degStrategy, p.degRecorder, r.Name)
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

// cachedReverseProxyHandler 支持缓存响应的反向代理处理器
// 用于缓存降级策略，在正常响应时缓存 body
func (p *Proxy) cachedReverseProxyHandler(proxy *httputil.ReverseProxy, cs *degradation.CacheStrategy) gin.HandlerFunc {
	return func(c *gin.Context) {
		body := new(bytes.Buffer)
		w := &captureWriter{
			ResponseWriter: c.Writer,
			body:           body,
		}
		c.Writer = w

		proxy.ServeHTTP(w, c.Request)

		// 缓存可缓存的状态码响应
		if c.Writer.Status() < http.StatusInternalServerError {
			cs.Store(c.Request, c.Writer.Status(), body.Bytes())
		}
	}
}

// captureWriter 包装 gin.ResponseWriter 以捕获响应体
type captureWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *captureWriter) Write(data []byte) (int, error) {
	w.body.Write(data)
	return w.ResponseWriter.Write(data)
}

func (p *Proxy) Run(addr string) error {
	return p.engine.Run(addr)
}

// BreakerWithDegradationMiddleware 熔断中间件（支持降级）
// 当熔断器开启时，优先执行降级策略；降级也失败时返回 503
func BreakerWithDegradationMiddleware(cb *gobreaker.CircuitBreaker, strategy degradation.Strategy, recorder *degradation.Recorder, routeName string) gin.HandlerFunc {
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
			if err == gobreaker.ErrOpenState && strategy != nil {
				// 熔断开启 → 执行降级策略
				resp, degErr := strategy.Execute(c.Request.Context(), c.Request)
				if degErr == nil {
					for k, v := range resp.Header {
						for _, hv := range v {
							c.Header(k, hv)
						}
					}
					c.Status(resp.StatusCode)
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					c.Writer.Write(body)
					c.Abort()
					if recorder != nil {
						recorder.Record(routeName, strategy.Name(), "circuit_breaker_open")
					}
					return
				}
			}

			// 降级也失败或无降级策略时，返回 503
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "service temporary unavailable (circuit breaker open)",
			})
		}
	}
}

// BreakerMiddleware 熔断中间件（无降级）
// 保留以保持向后兼容
func BreakerMiddleware(cb *gobreaker.CircuitBreaker) gin.HandlerFunc {
	return BreakerWithDegradationMiddleware(cb, nil, nil, "")
}
