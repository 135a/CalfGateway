package proxy

import (
	"CalfGateway/internal/config"
	"CalfGateway/internal/degradation"
	"CalfGateway/internal/monitor"
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

// 默认时间窗口：10s、10 个桶
var defaultWindow = monitor.TimeWindowConfig{Size: 10 * time.Second, BucketCount: 10}

type Proxy struct {
	config      *config.Config
	engine      *gin.Engine
	monitor     *monitor.Monitor
	degRecorder *degradation.Recorder
}

func NewProxy(cfg *config.Config) *Proxy {
	// 全局 QPS 窗口
	qpsCfg := toWindowCfg(cfg.Degradation.QPSWindow, defaultWindow)

	recorder := degradation.NewRecorder()
	mon := monitor.NewMonitor(qpsCfg)
	mon.SetDegRecorder(recorder)
	mon.Start() // 启动 CPU 采样协程

	p := &Proxy{
		config:      cfg,
		engine:      gin.Default(),
		monitor:     mon,
		degRecorder: recorder,
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

		// 组装中间件链，执行顺序：
		//   指标采集 → 自动降级判定 → 限流 → 熔断(兜底降级) → 核心处理器
		var handlers []gin.HandlerFunc

		// 1. 指标采集（最外层）：统计 QPS / 错误率，供自动降级判定使用
		handlers = append(handlers, p.metricsMiddleware(r.Name))

		// 2. 自动降级判定：CPU / QPS / 错误率 超阈值时主动降级
		if degStrategy != nil && degCfg != nil && hasThreshold(degCfg) {
			// 为该路由初始化错误率窗口
			p.monitor.InitErrorWindow(r.Name, toWindowCfg(degCfg.ErrorWindow, defaultWindow))
			judge := degradation.NewJudge(p.monitor, degradation.RouteThreshold{
				RouteName:          r.Name,
				CPUThreshold:       degCfg.CPUThreshold,
				QPSThreshold:       degCfg.QPSThreshold,
				ErrorRateThreshold: degCfg.ErrorRateThreshold,
			})
			handlers = append(handlers, p.degradationMiddleware(degStrategy, judge))
		}

		// 3. 限流
		if r.RateLimit.Enabled {
			handlers = append(handlers, RateLimitMiddleware(&r.RateLimit))
		}

		// 4. 熔断（含兜底降级）。注意：必须排在核心处理器之前，因为它通过 c.Next() 包裹后端调用。
		if r.Breaker.Enabled {
			cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
				Name:        r.Name,
				MaxRequests: uint32(r.Breaker.MaxRequests),
				Interval:    r.Breaker.Interval,
				Timeout:     r.Breaker.Timeout,
				ReadyToTrip: func(counts gobreaker.Counts) bool {
					failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
					return counts.Requests >= uint32(r.Breaker.ErrorThresholdCount) && failureRatio >= r.Breaker.ErrorThresholdPercentage
				},
			})
			handlers = append(handlers, BreakerWithDegradationMiddleware(cb, degStrategy, p.degRecorder, r.Name))
		}

		// 5. 核心处理器
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

// hasThreshold 判断降级配置中是否设置了任意一个自动降级阈值
func hasThreshold(d *config.DegradationConfig) bool {
	return d.CPUThreshold > 0 || d.QPSThreshold > 0 || d.ErrorRateThreshold > 0
}

// toWindowCfg 把 config.WindowConfig 转成 monitor.TimeWindowConfig，非法值回退到默认
func toWindowCfg(c config.WindowConfig, def monitor.TimeWindowConfig) monitor.TimeWindowConfig {
	if c.Size <= 0 || c.BucketCount <= 0 {
		return def
	}
	return monitor.TimeWindowConfig{Size: c.Size, BucketCount: c.BucketCount}
}

// metricsMiddleware 指标采集中间件——请求结束后更新 QPS / 错误率窗口
func (p *Proxy) metricsMiddleware(routeName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		errType := monitor.ErrNone
		if degraded, ok := c.Get("degraded"); ok && degraded == true {
			// 网关主动降级 / 熔断降级 → 不计入错误率
			errType = monitor.ErrDegraded
		} else if c.Writer.Status() >= http.StatusInternalServerError {
			errType = monitor.ErrBackend5xx
		}
		p.monitor.Record(routeName, errType)
	}
}

// degradationMiddleware 自动降级判定中间件——指标超阈值时主动执行降级策略
func (p *Proxy) degradationMiddleware(strategy degradation.Strategy, judge *degradation.Judge) gin.HandlerFunc {
	return func(c *gin.Context) {
		degrade, reason := judge.ShouldDegrade()
		if !degrade {
			c.Next()
			return
		}

		c.Set("degraded", true)
		resp, err := strategy.Execute(c.Request.Context(), c.Request)
		if err != nil {
			// 降级执行失败（如缓存未命中）→ 503
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "service degraded",
			})
			return
		}

		// 注意：响应头必须在写入响应体之前设置，否则不会生效
		c.Header("X-Degradation-Reason", reason.String())
		writeResponse(c, resp)
		c.Abort()
		p.degRecorder.Record(judge.Threshold().RouteName, strategy.Name(), reason.String())
	}
}

func (p *Proxy) reverseProxyHandler(proxy *httputil.ReverseProxy) gin.HandlerFunc {
	return func(c *gin.Context) {
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

// cachedReverseProxyHandler 支持缓存响应的反向代理处理器
// 用于缓存降级策略，在正常响应时缓存 body，供降级时回放
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

// writeResponse 把降级策略返回的 *http.Response 写回客户端
func writeResponse(c *gin.Context, resp *http.Response) {
	for k, vs := range resp.Header {
		for _, v := range vs {
			c.Header(k, v)
		}
	}
	c.Status(resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	c.Writer.Write(body)
}

// BreakerWithDegradationMiddleware 熔断中间件（含兜底降级）
// 当熔断器开启时请求不会打到后端，此时优先执行降级策略；无策略或降级失败返回 503。
// 仅在熔断开启（ErrOpenState，响应尚未写出）时写降级响应，避免重复写。
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
				c.Set("degraded", true)
				writeResponse(c, resp)
				c.Abort()
				if recorder != nil {
					recorder.Record(routeName, strategy.Name(), "circuit_breaker_open")
				}
				return
			}
		}

		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "service temporary unavailable (circuit breaker open)",
		})
	}
}
