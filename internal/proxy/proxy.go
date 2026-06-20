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
	degRecorder *degradation.Recorder
}

func NewProxy(cfg *config.Config) *Proxy {
	p := &Proxy{
		config:      cfg,
		engine:      gin.Default(),
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
		degStrategy := buildStrategy(degCfg)

		// 核心处理器：默认反向代理；缓存降级需要捕获响应体，使用特殊处理器
		var coreHandler gin.HandlerFunc
		if cs, ok := degStrategy.(*degradation.CacheStrategy); ok {
			coreHandler = p.cachedReverseProxyHandler(proxy, cs)
		} else {
			coreHandler = p.reverseProxyHandler(proxy)
		}

		// 组装中间件链，执行顺序：限流 → 熔断(含降级) → 核心处理器。
		// 注意：熔断中间件必须排在核心处理器之前，因为它通过 c.Next() 包裹后端调用。
		var handlers []gin.HandlerFunc
		if r.RateLimit.Enabled {
			handlers = append(handlers, RateLimitMiddleware(&r.RateLimit))
		}
		if r.Breaker.Enabled {
			cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
				Name:        r.Name,
				MaxRequests: uint32(r.Breaker.MaxRequests),
				Interval:    r.Breaker.Interval,
				Timeout:     r.Breaker.Timeout,
				ReadyToTrip: func(counts gobreaker.Counts) bool {
					// 自定义触发逻辑
					failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
					return counts.Requests >= uint32(r.Breaker.ErrorThresholdCount) && failureRatio >= r.Breaker.ErrorThresholdPercentage
				},
			})
			handlers = append(handlers, BreakerWithDegradationMiddleware(cb, degStrategy, p.degRecorder, r.Name))
		}
		handlers = append(handlers, coreHandler)

		if len(r.Methods) > 0 {
			for _, method := range r.Methods {
				p.engine.Handle(method, r.Path, handlers...)
			}
		} else {
			p.engine.Any(r.Path, handlers...)
		}
	}
}

// buildStrategy 根据降级配置构造对应的降级策略；未启用或类型未知时返回 nil
func buildStrategy(degCfg *config.DegradationConfig) degradation.Strategy {
	if degCfg == nil || !degCfg.Enabled {
		return nil
	}
	switch degCfg.Strategy {
	case "static_response":
		return degradation.NewStaticResponseStrategy(
			degCfg.Static.StatusCode,
			degCfg.Static.Headers,
			degCfg.Static.Body,
		)
	case "cache":
		return degradation.NewCacheStrategy(
			degCfg.Cache.TTL,
			degCfg.Cache.MaxEntries,
			degCfg.Cache.CacheableStatuses,
		)
	default:
		return nil
	}
}

func (p *Proxy) reverseProxyHandler(proxy *httputil.ReverseProxy) gin.HandlerFunc {
	return func(c *gin.Context) {
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

// cachedReverseProxyHandler 支持缓存响应的反向代理处理器
// 用于缓存降级策略，在正常响应时缓存 body，供熔断降级时回放
func (p *Proxy) cachedReverseProxyHandler(proxy *httputil.ReverseProxy, cs *degradation.CacheStrategy) gin.HandlerFunc {
	return func(c *gin.Context) {
		body := new(bytes.Buffer)
		w := &captureWriter{
			ResponseWriter: c.Writer,
			body:           body,
		}
		c.Writer = w

		proxy.ServeHTTP(w, c.Request)

		// 缓存非 5xx 的响应（具体是否可缓存由策略的 cacheable_statuses 决定）
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
// 当熔断器开启时，请求不会打到后端，此时优先执行降级策略；
// 无降级策略或降级失败时返回 503。
// 注意：仅在熔断开启（ErrOpenState）这一“响应尚未写出”的情况下才写降级响应，
// 后端自身返回 5xx 时响应已写出，不在此处重复写，避免 superfluous WriteHeader。
func BreakerWithDegradationMiddleware(cb *gobreaker.CircuitBreaker, strategy degradation.Strategy, recorder *degradation.Recorder, routeName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		_, err := cb.Execute(func() (interface{}, error) {
			c.Next()
			if c.Writer.Status() >= http.StatusInternalServerError {
				return nil, fmt.Errorf("backend service error: %d", c.Writer.Status())
			}
			return nil, nil
		})

		if err != gobreaker.ErrOpenState {
			// 熔断未开启：后端已被调用且响应已写出（包括 5xx），此处不再处理
			return
		}

		// 熔断开启：尝试执行降级策略
		if strategy != nil {
			if resp, degErr := strategy.Execute(c.Request.Context(), c.Request); degErr == nil {
				for k, vs := range resp.Header {
					for _, v := range vs {
						c.Header(k, v)
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

		// 无降级策略或降级失败，返回 503
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "service temporary unavailable (circuit breaker open)",
		})
	}
}
