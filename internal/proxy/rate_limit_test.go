package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"CalfGateway/internal/config"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newCtx(method, path string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, nil)
	return c, w
}

// ---------- GatewayRateLimitMiddleware (第1层：网关全局) ----------

func TestGatewayRateLimit_UnderLimit_Allow(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global: config.LimitConfig{Rate: 1000, Burst: 100},
	}
	handler := GatewayRateLimitMiddleware(cfg)

	c, w := newCtx("GET", "/")
	handler(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
}

func TestGatewayRateLimit_OverLimit_Deny(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global: config.LimitConfig{Rate: 0.001, Burst: 1},
	}
	handler := GatewayRateLimitMiddleware(cfg)

	// 消耗 burst
	c, w := newCtx("GET", "/")
	handler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("first request should be allowed, got %d", w.Code)
	}

	// 再次请求应被限流
	c2, w2 := newCtx("GET", "/")
	handler(c2)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 TooManyRequests, got %d", w2.Code)
	}
}

func TestGatewayRateLimit_DenyMessage(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global: config.LimitConfig{Rate: 0.001, Burst: 1},
	}
	handler := GatewayRateLimitMiddleware(cfg)

	// 消耗 burst
	c, _ := newCtx("GET", "/")
	handler(c)

	// 超限请求
	c2, w2 := newCtx("GET", "/")
	handler(c2)

	body := w2.Body.String()
	if body != `{"error":"gateway rate limit exceeded"}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestGatewayRateLimit_MultipleInstancesIndependent(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global: config.LimitConfig{Rate: 0.001, Burst: 1},
	}
	handler1 := GatewayRateLimitMiddleware(cfg)
	handler2 := GatewayRateLimitMiddleware(cfg)

	// 用掉 handler1 的 burst
	c1, _ := newCtx("GET", "/")
	handler1(c1)

	// handler2 是新实例，应有自己的 burst
	c2, w2 := newCtx("GET", "/")
	handler2(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("independent instance should allow, got %d", w2.Code)
	}
}

// ---------- RateLimitMiddleware (第2层: 路由全局 + 第3层: 每客户端) ----------

func TestRateLimit_Layer2_UnderLimit_Allow(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global:    config.LimitConfig{Rate: 1000, Burst: 100},
		PerClient: config.LimitConfig{Rate: 1000, Burst: 100},
	}
	handler := RateLimitMiddleware(cfg)

	c, w := newCtx("GET", "/")
	handler(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
}

func TestRateLimit_Layer2_OverLimit_Deny(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global:    config.LimitConfig{Rate: 0.001, Burst: 1},
		PerClient: config.LimitConfig{Rate: 1000, Burst: 100},
	}
	handler := RateLimitMiddleware(cfg)

	// 消耗第2层 burst
	c, w := newCtx("GET", "/")
	handler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("first request should be allowed, got %d", w.Code)
	}

	// 第2层已满 → 429
	c2, w2 := newCtx("GET", "/")
	handler(c2)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w2.Code)
	}
}

func TestRateLimit_Layer2_DenyMessage(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global:    config.LimitConfig{Rate: 0.001, Burst: 1},
		PerClient: config.LimitConfig{Rate: 1000, Burst: 100},
	}
	handler := RateLimitMiddleware(cfg)

	// 消耗 burst
	c, _ := newCtx("GET", "/")
	handler(c)

	// 超限
	c2, w2 := newCtx("GET", "/")
	handler(c2)

	if w2.Body.String() != `{"error":"route rate limit exceeded"}` {
		t.Fatalf("unexpected body: %s", w2.Body.String())
	}
}

func TestRateLimit_Layer3_SameIP_OverLimit_Deny(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global:    config.LimitConfig{Rate: 1000, Burst: 100},
		PerClient: config.LimitConfig{Rate: 0.001, Burst: 1},
	}
	handler := RateLimitMiddleware(cfg)

	// 同一 IP 第一次请求
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	handler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("first request should be allowed, got %d", w.Code)
	}

	// 同一 IP 再次请求 → 429
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.RemoteAddr = "192.168.1.1:12345"
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = req2
	handler(c2)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for same IP, got %d", w2.Code)
	}
}

func TestRateLimit_Layer3_DifferentIPs_Independent(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global:    config.LimitConfig{Rate: 1000, Burst: 100},
		PerClient: config.LimitConfig{Rate: 0.001, Burst: 1},
	}
	handler := RateLimitMiddleware(cfg)

	// IP-A 消耗掉 burst
	cA, wA := newCtx("GET", "/")
	cA.Request.RemoteAddr = "10.0.0.1:12345"
	handler(cA)
	if wA.Code != http.StatusOK {
		t.Fatalf("IP-A first request should be allowed, got %d", wA.Code)
	}

	// IP-A 再次请求 → 429
	cA2, wA2 := newCtx("GET", "/")
	cA2.Request.RemoteAddr = "10.0.0.1:12345"
	handler(cA2)
	if wA2.Code != http.StatusTooManyRequests {
		t.Fatalf("IP-A second request should be 429, got %d", wA2.Code)
	}

	// IP-B 不受影响
	cB, wB := newCtx("GET", "/")
	cB.Request.RemoteAddr = "10.0.0.2:54321"
	handler(cB)
	if wB.Code != http.StatusOK {
		t.Fatalf("IP-B first request should be allowed, got %d", wB.Code)
	}
}

func TestRateLimit_Layer3_DenyMessage(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global:    config.LimitConfig{Rate: 1000, Burst: 100},
		PerClient: config.LimitConfig{Rate: 0.001, Burst: 1},
	}
	handler := RateLimitMiddleware(cfg)

	// 消耗同一 IP 的 burst
	c, _ := newCtx("GET", "/")
	handler(c)

	// 超限
	c2, w2 := newCtx("GET", "/")
	handler(c2)

	if w2.Body.String() != `{"error":"per-client rate limit exceeded"}` {
		t.Fatalf("unexpected body: %s", w2.Body.String())
	}
}

func TestRateLimit_PerClientDisabled_SkipLayer3(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global:    config.LimitConfig{Rate: 1000, Burst: 100},
		PerClient: config.LimitConfig{Rate: 0, Burst: 0},
	}
	handler := RateLimitMiddleware(cfg)

	for i := 0; i < 10; i++ {
		c, w := newCtx("GET", "/")
		handler(c)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d should be allowed when per-client disabled, got %d", i, w.Code)
		}
	}
}

func TestRateLimit_Layer2PriorityOverLayer3(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global:    config.LimitConfig{Rate: 0.001, Burst: 1},
		PerClient: config.LimitConfig{Rate: 0.001, Burst: 1},
	}
	handler := RateLimitMiddleware(cfg)

	// 消耗两个层的 burst
	c, _ := newCtx("GET", "/")
	handler(c)

	// 第2层先满，应返回路由全局限流错误
	c2, w2 := newCtx("GET", "/")
	handler(c2)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w2.Code)
	}
	if w2.Body.String() != `{"error":"route rate limit exceeded"}` {
		t.Fatalf("layer 2 should take priority, body: %s", w2.Body.String())
	}
}

func TestRateLimit_MultipleInstancesIndependent(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global:    config.LimitConfig{Rate: 0.001, Burst: 1},
		PerClient: config.LimitConfig{Rate: 0.001, Burst: 1},
	}
	handler1 := RateLimitMiddleware(cfg)
	handler2 := RateLimitMiddleware(cfg)

	// 用掉 handler1 的 burst
	c1, _ := newCtx("GET", "/")
	handler1(c1)

	// handler2 是新实例，应该有自己的 burst
	c2, w2 := newCtx("GET", "/")
	handler2(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("independent instance should allow, got %d", w2.Code)
	}
}

func TestRateLimit_NextCalledOnAllow(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global:    config.LimitConfig{Rate: 1000, Burst: 100},
		PerClient: config.LimitConfig{Rate: 1000, Burst: 100},
	}
	handler := RateLimitMiddleware(cfg)

	called := false
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	handler(c)
	if !c.IsAborted() {
		called = true
	}

	if !called {
		t.Fatal("next handler should be called when request is allowed")
	}
}

func TestRateLimit_NextNotCalledOnDeny(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Global:    config.LimitConfig{Rate: 0.001, Burst: 1},
		PerClient: config.LimitConfig{Rate: 1000, Burst: 100},
	}
	handler := RateLimitMiddleware(cfg)

	// 消耗 burst
	c, _ := newCtx("GET", "/")
	handler(c)

	// 第二次请求应被限流
	called := false
	c2, w2 := newCtx("GET", "/")
	handler(c2)

	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w2.Code)
	}
	_ = called
}
