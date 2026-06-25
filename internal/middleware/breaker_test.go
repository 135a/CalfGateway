package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sony/gobreaker"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// runBreakerTest executes a request against a gin Engine using the BreakerMiddleware.
func runBreakerTest(cb *gobreaker.CircuitBreaker, method, path string, nextStatus int) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	engine := gin.New()
	engine.Use(BreakerMiddleware(cb))
	engine.Any("/*path", func(c *gin.Context) {
		if nextStatus > 0 {
			c.Status(nextStatus)
		}
	})
	req := httptest.NewRequest(method, path, nil)
	engine.ServeHTTP(w, req)
	return w
}

// newTestBreaker 创建用于测试的 CircuitBreaker。
func newTestBreaker(name string, errorThreshold int, failureRatio float64, maxRequests uint32, timeout time.Duration) *gobreaker.CircuitBreaker {
	return gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        name,
		MaxRequests: maxRequests,
		Interval:    10 * time.Second, // 窗口期内不清零
		Timeout:     timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			if counts.Requests == 0 {
				return false
			}
			ratio := float64(counts.TotalFailures) / float64(counts.Requests)
			return counts.Requests >= uint32(errorThreshold) && ratio >= failureRatio
		},
	})
}

// 常用测试配置
const (
	testMaxRequests  = 1
	testErrorCount   = 3
	testFailureRatio = 0.5
	shortTimeout     = 10 * time.Millisecond
)

// ---------- 熔断关闭态 ----------

func TestBreaker_Closed_AllowSuccess(t *testing.T) {
	cb := newTestBreaker("test", testErrorCount, testFailureRatio, testMaxRequests, shortTimeout)

	w := runBreakerTest(cb, "GET", "/api/hello", http.StatusOK)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("state should remain closed, got %s", cb.State())
	}
}

func TestBreaker_Closed_CountBackend5xx(t *testing.T) {
	cb := newTestBreaker("test", testErrorCount, testFailureRatio, testMaxRequests, shortTimeout)

	w := runBreakerTest(cb, "GET", "/api/error", http.StatusInternalServerError)

	// 后端 500 应透传
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}

	counts := cb.Counts()
	if counts.TotalFailures != 1 {
		t.Fatalf("expected 1 failure, got %d", counts.TotalFailures)
	}
}

func TestBreaker_Closed_CountClient4xxAsSuccess(t *testing.T) {
	cb := newTestBreaker("test", testErrorCount, testFailureRatio, testMaxRequests, shortTimeout)

	w := runBreakerTest(cb, "GET", "/api/notfound", http.StatusNotFound)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	counts := cb.Counts()
	if counts.TotalFailures != 0 {
		t.Fatalf("4xx should not count as failure, got %d failures", counts.TotalFailures)
	}
	if counts.TotalSuccesses != 1 {
		t.Fatalf("4xx should count as success, got %d successes", counts.TotalSuccesses)
	}
}

// ---------- 熔断关闭→打开 状态转换 ----------

func TestBreaker_ClosedToOpen_ByErrorCount(t *testing.T) {
	cb := newTestBreaker("test", testErrorCount, testFailureRatio, testMaxRequests, shortTimeout)

	for i := 0; i < testErrorCount; i++ {
		runBreakerTest(cb, "GET", "/api/error", http.StatusInternalServerError)
	}

	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("expected Open state after %d failures, got %s", testErrorCount, cb.State())
	}
}

func TestBreaker_Closed_NotTripByPartialFailure(t *testing.T) {
	cb := newTestBreaker("test", testErrorCount, testFailureRatio, testMaxRequests, shortTimeout)

	for i := 0; i < 3; i++ {
		runBreakerTest(cb, "GET", "/api/hello", http.StatusOK)
	}
	for i := 0; i < 2; i++ {
		runBreakerTest(cb, "GET", "/api/error", http.StatusInternalServerError)
	}

	if cb.State() == gobreaker.StateOpen {
		t.Fatalf("state should NOT be open (ratio=0.4 < 0.5), got Open")
	}
}

// ---------- 熔断打开态 ----------

func TestBreaker_Open_RejectRequest(t *testing.T) {
	cb := newTestBreaker("test", 1, 0, testMaxRequests, shortTimeout)

	// 第一次请求失败 → 熔断打开
	runBreakerTest(cb, "GET", "/api/error", http.StatusInternalServerError)

	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("expected Open state, got %s", cb.State())
	}

	// 打开态下请求被拒绝
	w2 := runBreakerTest(cb, "GET", "/api/hello", http.StatusOK)

	if w2.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 in Open state, got %d", w2.Code)
	}
}

func TestBreaker_Open_ErrorMessage(t *testing.T) {
	cb := newTestBreaker("test", 1, 0, testMaxRequests, shortTimeout)

	// 触发熔断
	runBreakerTest(cb, "GET", "/api/error", http.StatusInternalServerError)

	// 验证错误信息
	w2 := runBreakerTest(cb, "GET", "/api/hello", http.StatusOK)

	body := w2.Body.String()
	want := `{"error":"service temporary unavailable (circuit breaker open)"}`
	if body != want {
		t.Fatalf("unexpected body:\ngot:  %s\nwant: %s", body, want)
	}
}

func TestBreaker_Open_ContentType(t *testing.T) {
	cb := newTestBreaker("test", 1, 0, testMaxRequests, shortTimeout)

	// 触发熔断
	runBreakerTest(cb, "GET", "/api/error", http.StatusInternalServerError)

	w2 := runBreakerTest(cb, "GET", "/api/hello", http.StatusOK)

	ct := w2.Header().Get("Content-Type")
	if ct != "application/json; charset=utf-8" {
		t.Fatalf("expected application/json content-type, got %s", ct)
	}
}

// ---------- 熔断打开→半开 状态转换 ----------

func TestBreaker_OpenToHalfOpen_AfterTimeout(t *testing.T) {
	cb := newTestBreaker("test", 1, 0, testMaxRequests, 5*time.Millisecond)

	// 触发熔断
	runBreakerTest(cb, "GET", "/api/error", http.StatusInternalServerError)

	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("expected Open after failure, got %s", cb.State())
	}

	// 等待超时进入半开
	time.Sleep(10 * time.Millisecond)

	// 半开态下请求应被允许（maxRequests=1）
	w2 := runBreakerTest(cb, "GET", "/api/hello", http.StatusOK)

	if w2.Code == http.StatusServiceUnavailable {
		t.Fatalf("request should be allowed in Half-Open, but got 503")
	}
}

func TestBreaker_HalfOpen_SuccessToClosed(t *testing.T) {
	cb := newTestBreaker("test", 1, 0, testMaxRequests, 5*time.Millisecond)

	// 触发熔断
	runBreakerTest(cb, "GET", "/api/error", http.StatusInternalServerError)

	// 等待半开
	time.Sleep(10 * time.Millisecond)

	// 半开态下请求成功 → 关闭
	w2 := runBreakerTest(cb, "GET", "/api/hello", http.StatusOK)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 in Half-Open, got %d", w2.Code)
	}
	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("expected Closed after success in Half-Open, got %s", cb.State())
	}
}

func TestBreaker_HalfOpen_FailureToOpen(t *testing.T) {
	cb := newTestBreaker("test", 1, 0, testMaxRequests, 5*time.Millisecond)

	// 触发熔断
	runBreakerTest(cb, "GET", "/api/error", http.StatusInternalServerError)

	// 等待半开
	time.Sleep(10 * time.Millisecond)

	// 半开态下请求失败 → 重新打开
	w2 := runBreakerTest(cb, "GET", "/api/error", http.StatusInternalServerError)

	if w2.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 from backend, got %d", w2.Code)
	}
	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("expected Open after failure in Half-Open, got %s", cb.State())
	}
}

// ---------- 半开态 maxRequests 限制 ----------

func TestBreaker_HalfOpen_MaxRequestsLimit(t *testing.T) {
	// maxRequests=2，只允许 2 个探测请求通过
	cb := newTestBreaker("test", 1, 0, 2, 5*time.Millisecond)

	// 触发熔断
	runBreakerTest(cb, "GET", "/api/error", http.StatusInternalServerError)

	// 等待半开
	time.Sleep(10 * time.Millisecond)

	// 第 1 个探测请求
	w2 := runBreakerTest(cb, "GET", "/api/hello", http.StatusOK)
	if w2.Code != http.StatusOK {
		t.Fatalf("1st probe should be allowed, got %d", w2.Code)
	}

	// 第 2 个探测请求
	w3 := runBreakerTest(cb, "GET", "/api/hello", http.StatusOK)
	if w3.Code != http.StatusOK {
		t.Fatalf("2nd probe should be allowed, got %d", w3.Code)
	}

	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("expected Closed after 2 successes in Half-Open, got %s", cb.State())
	}
}

// ---------- 中间件多次独立调用 ----------

func TestBreaker_MultipleInstances_Independent(t *testing.T) {
	cb1 := newTestBreaker("breaker-a", testErrorCount, testFailureRatio, testMaxRequests, shortTimeout)
	cb2 := newTestBreaker("breaker-b", testErrorCount, testFailureRatio, testMaxRequests, shortTimeout)

	// breaker-a 触发熔断
	for i := 0; i < testErrorCount; i++ {
		runBreakerTest(cb1, "GET", "/api/error", http.StatusInternalServerError)
	}
	if cb1.State() != gobreaker.StateOpen {
		t.Fatalf("breaker-a should be Open, got %s", cb1.State())
	}

	// breaker-b 不受影响
	if cb2.State() != gobreaker.StateClosed {
		t.Fatalf("breaker-b should remain Closed, got %s", cb2.State())
	}
	w := runBreakerTest(cb2, "GET", "/api/hello", http.StatusOK)
	if w.Code != http.StatusOK {
		t.Fatalf("breaker-b should allow requests, got %d", w.Code)
	}
}

// ---------- gobreaker.ErrTooManyRequests 边界 ----------

func TestBreaker_HalfOpen_TooManyRequests(t *testing.T) {
	cb := newTestBreaker("test", 1, 0, 1, 5*time.Millisecond)

	// 触发熔断
	runBreakerTest(cb, "GET", "/api/error", http.StatusInternalServerError)

	// 等待半开
	time.Sleep(10 * time.Millisecond)

	// 第 1 个探测请求
	w2 := runBreakerTest(cb, "GET", "/api/hello", http.StatusOK)
	if w2.Code != http.StatusOK {
		t.Fatalf("probe should be allowed in Half-Open, got %d", w2.Code)
	}

	// 此时 CB 已转为 Closed
	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("expected Closed after Half-Open success, got %s", cb.State())
	}
}
