package proxy

import (
	"CalfGateway/internal/config"
	"CalfGateway/internal/degradation"
	"CalfGateway/internal/monitor"
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
	qpsCfg := toWindowCfg(cfg.Degradation.QPSWindow, defaultWindow)

	recorder := degradation.NewRecorder()
	mon := monitor.NewMonitor(qpsCfg)
	mon.SetDegRecorder(recorder)
	mon.Start()

	p := &Proxy{
		config:      cfg,
		engine:      gin.Default(),
		monitor:     mon,
		degRecorder: recorder,
	}
	if cfg.Auth.Enabled {
		p.engine.Use(AuthMiddleware(&cfg.Auth))
	}
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

func (p *Proxy) setupRoutes() {
	for _, route := range p.config.Routes {
		r := route
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

		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			if r.StripPrefix != "" {
				if path := req.URL.Path; len(path) >= len(r.StripPrefix) && path[:len(r.StripPrefix)] == r.StripPrefix {
					req.URL.Path = path[len(r.StripPrefix):]
					if req.URL.Path == "" {
						req.URL.Path = "/"
					}
				}
			}
			req.Header.Set("X-Real-IP", req.RemoteAddr)
		}

		var degCfg *config.DegradationConfig
		if r.Degradation != nil {
			degCfg = r.Degradation
		} else if p.config.Degradation.Enabled {
			degCfg = &p.config.Degradation
		}

		degStrategy := buildStrategy(degCfg)

		// 中间件链：指标采集 → 自动降级判定 → 限流 → 熔断 → 反向代理
		var handlers []gin.HandlerFunc

		handlers = append(handlers, p.metricsMiddleware(r.Name))

		if degStrategy != nil && degCfg != nil && hasThreshold(degCfg) {
			p.monitor.InitErrorWindow(r.Name, toWindowCfg(degCfg.ErrorWindow, defaultWindow))
			judge := degradation.NewJudge(p.monitor, degradation.RouteThreshold{
				RouteName:          r.Name,
				CPUThreshold:       degCfg.CPUThreshold,
				QPSThreshold:       degCfg.QPSThreshold,
				ErrorRateThreshold: degCfg.ErrorRateThreshold,
			})
			handlers = append(handlers, p.degradationMiddleware(degStrategy, judge))
		}

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
					failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
					return counts.Requests >= uint32(r.Breaker.ErrorThresholdCount) && failureRatio >= r.Breaker.ErrorThresholdPercentage
				},
			})
			handlers = append(handlers, BreakerMiddleware(cb))
		}

		handlers = append(handlers, p.reverseProxyHandler(proxy))

		if len(r.Methods) > 0 {
			for _, method := range r.Methods {
				p.engine.Handle(method, r.Path, handlers...)
			}
		} else {
			p.engine.Any(r.Path, handlers...)
		}
	}
}

func buildStrategy(degCfg *config.DegradationConfig) degradation.Strategy {
	if degCfg == nil || !degCfg.Enabled {
		return nil
	}
	if degCfg.Strategy != "static_response" && degCfg.Strategy != "" {
		return nil
	}
	return degradation.NewStaticResponseStrategy(
		degCfg.Static.StatusCode,
		degCfg.Static.Headers,
		degCfg.Static.Body,
	)
}

func hasThreshold(d *config.DegradationConfig) bool {
	return d.CPUThreshold > 0 || d.QPSThreshold > 0 || d.ErrorRateThreshold > 0
}

func toWindowCfg(c config.WindowConfig, def monitor.TimeWindowConfig) monitor.TimeWindowConfig {
	if c.Size <= 0 || c.BucketCount <= 0 {
		return def
	}
	return monitor.TimeWindowConfig{Size: c.Size, BucketCount: c.BucketCount}
}

func (p *Proxy) metricsMiddleware(routeName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		errType := monitor.ErrNone
		if degraded, ok := c.Get("degraded"); ok && degraded == true {
			errType = monitor.ErrDegraded
		} else if c.Writer.Status() >= http.StatusInternalServerError {
			errType = monitor.ErrBackend5xx
		}
		p.monitor.Record(routeName, errType)
	}
}

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
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "service degraded",
			})
			return
		}

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

func (p *Proxy) Run(addr string) error {
	return p.engine.Run(addr)
}

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
