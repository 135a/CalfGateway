package proxy

import (
	"CalfGateway/internal/config"
	"CalfGateway/internal/degradation"
	"CalfGateway/internal/middleware"
	"CalfGateway/internal/monitor"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sony/gobreaker"
)

// 默认时间窗口：10s、10 个桶
var defaultWindow = monitor.TimeWindowConfig{Size: 10 * time.Second, BucketCount: 10}

type Proxy struct {
	provider *config.Provider
	monitor  *monitor.Monitor
	handler  atomic.Value // holds *gin.Engine
}

func NewProxy(provider *config.Provider) *Proxy {
	p := &Proxy{
		provider: provider,
	}

	p.rebuildEngine()

	go func() {
		for range provider.OnUpdate() {
			log.Println("Proxy rebuilding routes due to config update...")
			p.rebuildEngine()
			log.Println("Proxy routes rebuilt successfully")
		}
	}()

	return p
}

func (p *Proxy) rebuildEngine() {
	cfg := p.provider.Get()
	if cfg == nil {
		log.Println("Warning: rebuildEngine called but config is nil")
		return
	}

	// 监控实例可以复用，或者仅当不存在时创建
	if p.monitor == nil {
		qpsCfg := toWindowCfg(cfg.Degradation.QPSWindow, defaultWindow)
		mon := monitor.NewMonitor(qpsCfg)
		mon.Start()
		p.monitor = mon
	}

	engine := gin.Default()
	if cfg.Auth.Enabled {
		engine.Use(middleware.AuthMiddleware(&cfg.Auth))
	}
	if cfg.RateLimit.Enabled {
		engine.Use(middleware.GatewayRateLimitMiddleware(&cfg.RateLimit))
	}
	
	p.setupRoutes(engine, cfg)

	p.handler.Store(engine)
}

func (p *Proxy) setupRoutes(engine *gin.Engine, cfg *config.Config) {
	for _, route := range cfg.Routes {
		r := route
		targetUrl, err := url.Parse(r.Target)
		if err != nil {
			panic("invalid target URL:" + r.Target)
		}

		proxy := httputil.NewSingleHostReverseProxy(targetUrl)
		proxy.Transport = &http.Transport{
			MaxIdleConns:        cfg.Proxy.MaxIdleConns,
			MaxIdleConnsPerHost: cfg.Proxy.MaxIdleConnsPerHost,
			MaxConnsPerHost:     cfg.Proxy.MaxConnsPerHost,
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
		} else if cfg.Degradation.Enabled {
			degCfg = &cfg.Degradation
		}

		degStrategy := buildStrategy(degCfg)

		// 中间件链：指标采集 → 自动降级判定 → 限流 → 熔断 → 反向代理
		var handlers []gin.HandlerFunc

		handlers = append(handlers, middleware.MetricsMiddleware(p.monitor, r.Name))

		if degStrategy != nil && degCfg != nil && hasThreshold(degCfg) {
			p.monitor.InitErrorWindow(r.Name, toWindowCfg(degCfg.ErrorWindow, defaultWindow))
			judge := degradation.NewJudge(p.monitor, degradation.RouteThreshold{
				RouteName:          r.Name,
				CPUThreshold:       degCfg.CPUThreshold,
				QPSThreshold:       degCfg.QPSThreshold,
				ErrorRateThreshold: degCfg.ErrorRateThreshold,
			})
			handlers = append(handlers, middleware.DegradationMiddleware(degStrategy, judge))
		}

		if r.RateLimit.Enabled {
			handlers = append(handlers, middleware.RateLimitMiddleware(&r.RateLimit))
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
			handlers = append(handlers, middleware.BreakerMiddleware(cb))
		}

		handlers = append(handlers, p.reverseProxyHandler(proxy))

		if len(r.Methods) > 0 {
			for _, method := range r.Methods {
				engine.Handle(method, r.Path, handlers...)
			}
		} else {
			engine.Any(r.Path, handlers...)
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

func (p *Proxy) reverseProxyHandler(proxy *httputil.ReverseProxy) gin.HandlerFunc {
	return func(c *gin.Context) {
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	handler := p.handler.Load().(*gin.Engine)
	handler.ServeHTTP(w, req)
}

func (p *Proxy) Run(addr string) error {
	return http.ListenAndServe(addr, p)
}
