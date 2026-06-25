package degradation

import (
	"io"
	"net/http"
	"testing"
	"time"

	"CalfGateway/internal/monitor"
)

// ---------- Judge 降级判定 ----------
// 没有降级的情况
func TestJudge_AllThresholdsZero_NoDegradation(t *testing.T) {
	m := newTestMonitor()
	judge := NewJudge(m, RouteThreshold{
		RouteName: "test",
	})

	degraded, reason := judge.ShouldDegrade()
	if degraded {
		t.Fatalf("expected no degradation, got reason=%v", reason)
	}
}

// qps超过限制
func TestJudge_QPSExceedsThreshold(t *testing.T) {
	m := newTestMonitor()
	// 记录 6 次请求 → QPS ≈ 6/1s = 6
	for i := 0; i < 6; i++ {
		m.Record("test", monitor.ErrNone)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:    "test",
		QPSThreshold: 5,
	})

	degraded, reason := judge.ShouldDegrade()
	if !degraded {
		t.Fatal("expected degradation due to QPS overload")
	}
	if reason != ReasonQPSOverload {
		t.Fatalf("expected QPSOverload, got %v", reason)
	}
}

// qps没有超过限制
func TestJudge_QPSBelowThreshold(t *testing.T) {
	m := newTestMonitor()
	for i := 0; i < 3; i++ {
		m.Record("test", monitor.ErrNone)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:    "test",
		QPSThreshold: 5,
	})

	degraded, _ := judge.ShouldDegrade()
	if degraded {
		t.Fatal("expected no degradation when QPS is below threshold")
	}
}

// 错误率超过阈值
func TestJudge_ErrorRateExceedsThreshold(t *testing.T) {
	m := newTestMonitor()
	// 4 成功 + 6 失败 → 错误率 60%
	for i := 0; i < 4; i++ {
		m.Record("test", monitor.ErrNone)
	}
	for i := 0; i < 6; i++ {
		m.Record("test", monitor.ErrBackend5xx)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "test",
		ErrorRateThreshold: 0.5, // 阈值 50%
	})

	degraded, reason := judge.ShouldDegrade()
	if !degraded {
		t.Fatal("expected degradation due to high error rate")
	}
	if reason != ReasonHighErrorRate {
		t.Fatalf("expected HighErrorRate, got %v", reason)
	}
}

// 错误率没有超过阈值
func TestJudge_ErrorRateBelowThreshold(t *testing.T) {
	m := newTestMonitor()
	// 8 成功 + 2 失败 → 错误率 20%
	for i := 0; i < 8; i++ {
		m.Record("test", monitor.ErrNone)
	}
	for i := 0; i < 2; i++ {
		m.Record("test", monitor.ErrBackend5xx)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "test",
		ErrorRateThreshold: 0.5,
	})

	degraded, _ := judge.ShouldDegrade()
	if degraded {
		t.Fatal("expected no degradation when error rate is below threshold")
	}
}

// ErrDegraded / ErrRateLimited 不应计入错误率
func TestJudge_DegradedNotCountedAsError(t *testing.T) {
	m := newTestMonitor()
	for i := 0; i < 10; i++ {
		m.Record("test", monitor.ErrDegraded)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "test",
		ErrorRateThreshold: 0.1,
	})

	degraded, _ := judge.ShouldDegrade()
	if degraded {
		t.Fatal("errDegraded should not count toward error rate")
	}
}

func TestJudge_RateLimitedNotCountedAsError(t *testing.T) {
	m := newTestMonitor()
	for i := 0; i < 10; i++ {
		m.Record("test", monitor.ErrRateLimited)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "test",
		ErrorRateThreshold: 0.1,
	})

	degraded, _ := judge.ShouldDegrade()
	if degraded {
		t.Fatal("errRateLimited should not count toward error rate")
	}
}

// ErrClient4xx 不应计入错误率
func TestJudge_Client4xxNotCountedAsError(t *testing.T) {
	m := newTestMonitor()
	// 全部是 4xx 错误
	for i := 0; i < 10; i++ {
		m.Record("test", monitor.ErrClient4xx)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "test",
		ErrorRateThreshold: 0.1,
	})

	degraded, _ := judge.ShouldDegrade()
	if degraded {
		t.Fatal("ErrClient4xx should not count toward error rate")
	}
}

// ErrNetwork 应计入错误率
func TestJudge_NetworkCountedAsError(t *testing.T) {
	m := newTestMonitor()
	for i := 0; i < 3; i++ {
		m.Record("test", monitor.ErrNone)
	}
	for i := 0; i < 7; i++ {
		m.Record("test", monitor.ErrNetwork)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "test",
		ErrorRateThreshold: 0.5,
	})

	degraded, reason := judge.ShouldDegrade()
	if !degraded {
		t.Fatal("expected degradation when ErrNetwork is counted as error")
	}
	if reason != ReasonHighErrorRate {
		t.Fatalf("expected HighErrorRate, got %v", reason)
	}
}

// Todo
// 未初始化 ErrorWindow 的路由不应 panic，错误率应视为 0
func TestJudge_NoErrorWindow_NoPanic(t *testing.T) {
	m := monitor.NewMonitor(monitor.TimeWindowConfig{
		Size:        time.Second,
		BucketCount: 10,
	})
	// 故意不初始化 "unknown" 路由的 ErrorWindow
	m.Record("unknown", monitor.ErrBackend5xx)

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "unknown",
		ErrorRateThreshold: 0.1,
	})

	degraded, reason := judge.ShouldDegrade()
	if degraded {
		t.Fatalf("expected no degradation without error window, got reason=%v", reason)
	}
} // Todo
// 空路由名不应 panic
func TestJudge_EmptyRouteName(t *testing.T) {
	m := newTestMonitor()
	m.Record("", monitor.ErrBackend5xx)

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "",
		QPSThreshold:       1000,
		ErrorRateThreshold: 0.5,
	})

	degraded, reason := judge.ShouldDegrade()
	if degraded {
		t.Fatalf("expected no degradation for empty route, got reason=%v", reason)
	}
}

func TestJudge_CPUExceedsThreshold(t *testing.T) {
	m := newTestMonitor()
	m.SetCPUPct(90)

	judge := NewJudge(m, RouteThreshold{
		RouteName:    "test",
		CPUThreshold: 80,
	})

	degraded, reason := judge.ShouldDegrade()
	if !degraded {
		t.Fatal("expected degradation due to CPU overload")
	}
	if reason != ReasonCPUOverload {
		t.Fatalf("expected CPUOverload, got %v", reason)
	}
}

func TestJudge_CPUBelowThreshold(t *testing.T) {
	m := newTestMonitor()
	m.SetCPUPct(50)

	judge := NewJudge(m, RouteThreshold{
		RouteName:    "test",
		CPUThreshold: 80,
	})

	degraded, _ := judge.ShouldDegrade()
	if degraded {
		t.Fatal("expected no degradation when CPU is below threshold")
	}
}

func TestJudge_CPUPriorityOverQPS(t *testing.T) {
	m := newTestMonitor()
	m.SetCPUPct(90) // CPU 超阈值
	for i := 0; i < 10; i++ {
		m.Record("test", monitor.ErrNone) // QPS 也超阈值
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:    "test",
		CPUThreshold: 80,
		QPSThreshold: 5,
	})

	degraded, reason := judge.ShouldDegrade()
	if !degraded {
		t.Fatal("expected degradation")
	}
	// CPU 检查在 QPS 之前，应优先返回 CPUOverload
	if reason != ReasonCPUOverload {
		t.Fatalf("expected CPUOverload (checked first), got %v", reason)
	}
}

// QPS 阈值 > 错误率阈值，且 QPS 先超 → 应返回 QPS 原因
func TestJudge_QPSPriorityOverErrorRate(t *testing.T) {
	m := newTestMonitor()
	// 记录大量请求使 QPS 超标，同时制造错误
	for i := 0; i < 10; i++ {
		m.Record("test", monitor.ErrBackend5xx)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "test",
		QPSThreshold:       5,   // QPS ≈ 10, 超标
		ErrorRateThreshold: 0.9, // 错误率 ≈ 100%, 也超标
	})

	degraded, reason := judge.ShouldDegrade()
	if !degraded {
		t.Fatal("expected degradation")
	}
	// CPU 未开启, QPS 检查在前, 应优先返回 QPSOverload
	if reason != ReasonQPSOverload {
		t.Fatalf("expected QPSOverload (checked first), got %v", reason)
	}
}

// ---------- StaticResponseStrategy ----------

func TestStaticResponseStrategy_Execute(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	strategy := NewStaticResponseStrategy(
		http.StatusServiceUnavailable,
		headers,
		`{"error":"degraded"}`,
	)

	resp, err := strategy.Execute(nil, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", resp.Header.Get("Content-Type"), "application/json")
	}

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != `{"error":"degraded"}` {
		t.Fatalf("Body = %q, want %q", string(body), `{"error":"degraded"}`)
	}
}

func TestStaticResponseStrategy_Name(t *testing.T) {
	s := NewStaticResponseStrategy(http.StatusOK, nil, "")
	if s.Name() != "static_response" {
		t.Fatalf("Name() = %q, want %q", s.Name(), "static_response")
	}
}

// ---------- RouteThreshold / DegradeReason ----------

func TestJudge_ThresholdGetter(t *testing.T) {
	m := newTestMonitor()
	threshold := RouteThreshold{
		RouteName:          "test",
		CPUThreshold:       80,
		QPSThreshold:       100,
		ErrorRateThreshold: 0.3,
	}
	judge := NewJudge(m, threshold)

	got := judge.Threshold()
	if got != threshold {
		t.Fatalf("Threshold() = %v, want %v", got, threshold)
	}
}

func TestDegradeReason_String(t *testing.T) {
	tests := []struct {
		reason DegradeReason
		want   string
	}{
		{ReasonNone, ""},
		{ReasonCPUOverload, "cpu_overload"},
		{ReasonHighErrorRate, "high_error_rate"},
		{ReasonQPSOverload, "qps_overload"},
	}

	for _, tt := range tests {
		if got := tt.reason.String(); got != tt.want {
			t.Errorf("DegradeReason(%d).String() = %q, want %q", tt.reason, got, tt.want)
		}
	}
}

// ---------- 边界值测试 ----------

// CPU 恰好等于阈值（> 而非 >=，不应降级）
func TestJudge_CPUEqualsThreshold(t *testing.T) {
	m := newTestMonitor()
	m.SetCPUPct(80)

	judge := NewJudge(m, RouteThreshold{
		RouteName:    "test",
		CPUThreshold: 80,
	})

	degraded, _ := judge.ShouldDegrade()
	if degraded {
		t.Fatal("expected no degradation when CPU equals threshold (uses > not >=)")
	}
}

// CPU 100% 应降级
func TestJudge_CPUAtMax(t *testing.T) {
	m := newTestMonitor()
	m.SetCPUPct(100)

	judge := NewJudge(m, RouteThreshold{
		RouteName:    "test",
		CPUThreshold: 80,
	})

	degraded, reason := judge.ShouldDegrade()
	if !degraded {
		t.Fatal("expected degradation when CPU is 100%")
	}
	if reason != ReasonCPUOverload {
		t.Fatalf("expected CPUOverload, got %v", reason)
	}
}

// QPS 恰好等于阈值（不应降级）
func TestJudge_QPSEqualsThreshold(t *testing.T) {
	m := newTestMonitor()
	for i := 0; i < 5; i++ {
		m.Record("test", monitor.ErrNone)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:    "test",
		QPSThreshold: 5,
	})

	degraded, _ := judge.ShouldDegrade()
	if degraded {
		t.Fatal("expected no degradation when QPS equals threshold (uses > not >=)")
	}
}

// 错误率恰好等于阈值（不应降级）
func TestJudge_ErrorRateEqualsThreshold(t *testing.T) {
	m := newTestMonitor()
	for i := 0; i < 5; i++ {
		m.Record("test", monitor.ErrNone)
	}
	for i := 0; i < 5; i++ {
		m.Record("test", monitor.ErrBackend5xx)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "test",
		ErrorRateThreshold: 0.5,
	})

	degraded, _ := judge.ShouldDegrade()
	if degraded {
		t.Fatal("expected no degradation when error rate equals threshold (uses > not >=)")
	}
}

// 错误率 100% 应降级
func TestJudge_ErrorRateAtMax(t *testing.T) {
	m := newTestMonitor()
	for i := 0; i < 10; i++ {
		m.Record("test", monitor.ErrBackend5xx)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "test",
		ErrorRateThreshold: 0.99,
	})

	degraded, reason := judge.ShouldDegrade()
	if !degraded {
		t.Fatal("expected degradation when error rate is 100%")
	}
	if reason != ReasonHighErrorRate {
		t.Fatalf("expected HighErrorRate, got %v", reason)
	}
}

// ---------- 并发安全测试 ----------

func TestJudge_ConcurrentAccess(t *testing.T) {
	m := newTestMonitor()
	for i := 0; i < 20; i++ {
		m.Record("test", monitor.ErrNone)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:    "test",
		CPUThreshold: 80,
		QPSThreshold: 10,
	})

	// 同时设置 CPU 并反复调用 ShouldDegrade
	m.SetCPUPct(50)

	done := make(chan struct{})
	const goroutines = 20
	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				judge.ShouldDegrade()
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
	// 如果没 panic 就算通过
}

// ---------- 多路由测试 ----------

func TestJudge_MultipleRoutes_Independent(t *testing.T) {
	m := newTestMonitor()
	m.InitErrorWindow("route-a", monitor.TimeWindowConfig{Size: time.Second, BucketCount: 10})
	m.InitErrorWindow("route-b", monitor.TimeWindowConfig{Size: time.Second, BucketCount: 10})

	// route-a: 大量错误, route-b: 全部成功
	for i := 0; i < 8; i++ {
		m.Record("route-a", monitor.ErrBackend5xx)
	}
	for i := 0; i < 2; i++ {
		m.Record("route-a", monitor.ErrNone)
	}
	for i := 0; i < 10; i++ {
		m.Record("route-b", monitor.ErrNone)
	}

	judgeA := NewJudge(m, RouteThreshold{
		RouteName:          "route-a",
		ErrorRateThreshold: 0.5, // 80% > 50% → 降级
	})
	judgeB := NewJudge(m, RouteThreshold{
		RouteName:          "route-b",
		ErrorRateThreshold: 0.5, // 0% < 50% → 不降级
	})

	degA, reasonA := judgeA.ShouldDegrade()
	if !degA {
		t.Fatal("expected route-a to degrade (error rate exceeds threshold)")
	}
	if reasonA != ReasonHighErrorRate {
		t.Fatalf("expected HighErrorRate for route-a, got %v", reasonA)
	}

	degB, _ := judgeB.ShouldDegrade()
	if degB {
		t.Fatal("expected route-b not to degrade (error rate is zero)")
	}
}

// ---------- 无指标数据测试 ----------

// 尚未 Record 任何数据，但有阈值，不应 panic 且不应降级
func TestJudge_NoMetrics_NoDegradation(t *testing.T) {
	m := newTestMonitor()

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "test",
		QPSThreshold:       100,
		ErrorRateThreshold: 0.5,
	})

	degraded, reason := judge.ShouldDegrade()
	if degraded {
		t.Fatalf("expected no degradation with no metrics, got reason=%v", reason)
	}
}

// ---------- 三个阈值全部启用 ----------

// 三个阈值都设置但都没超
func TestJudge_AllThreeThresholds_NoneExceeded(t *testing.T) {
	m := newTestMonitor()
	m.SetCPUPct(30)
	for i := 0; i < 3; i++ {
		m.Record("test", monitor.ErrNone)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "test",
		CPUThreshold:       80,
		QPSThreshold:       10,
		ErrorRateThreshold: 0.5,
	})

	degraded, reason := judge.ShouldDegrade()
	if degraded {
		t.Fatalf("expected no degradation when all thresholds are below limit, got reason=%v", reason)
	}
}

// 三个阈值全部超标，应按 CPU > QPS > 错误率 优先级返回
func TestJudge_AllThreeThresholds_AllExceeded(t *testing.T) {
	m := newTestMonitor()
	m.SetCPUPct(90)
	for i := 0; i < 10; i++ {
		m.Record("test", monitor.ErrBackend5xx)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "test",
		CPUThreshold:       80,
		QPSThreshold:       5,
		ErrorRateThreshold: 0.5,
	})

	degraded, reason := judge.ShouldDegrade()
	if !degraded {
		t.Fatal("expected degradation")
	}
	if reason != ReasonCPUOverload {
		t.Fatalf("expected CPUOverload (highest priority), got %v", reason)
	}
}

// 只有错误率阈值启用（QPS 和 CPU 都设为 0）
func TestJudge_OnlyErrorRateEnabled(t *testing.T) {
	m := newTestMonitor()
	for i := 0; i < 6; i++ {
		m.Record("test", monitor.ErrBackend5xx)
	}
	for i := 0; i < 4; i++ {
		m.Record("test", monitor.ErrNone)
	}

	judge := NewJudge(m, RouteThreshold{
		RouteName:          "test",
		CPUThreshold:       0,   // 禁用
		QPSThreshold:       0,   // 禁用
		ErrorRateThreshold: 0.5, // 60% > 50%
	})

	degraded, reason := judge.ShouldDegrade()
	if !degraded {
		t.Fatal("expected degradation when only error rate is enabled and exceeded")
	}
	if reason != ReasonHighErrorRate {
		t.Fatalf("expected HighErrorRate, got %v", reason)
	}
}

// ---------- StaticResponseStrategy 边界测试 ----------

func TestStaticResponseStrategy_EmptyHeadersAndBody(t *testing.T) {
	strategy := NewStaticResponseStrategy(http.StatusOK, nil, "")

	resp, err := strategy.Execute(nil, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "" {
		t.Fatalf("Body = %q, want empty", string(body))
	}
}

func TestStaticResponseStrategy_MultipleHeaders(t *testing.T) {
	headers := map[string]string{
		"Content-Type":  "application/json",
		"X-Custom-Head": "custom-value",
		"Retry-After":   "120",
	}
	strategy := NewStaticResponseStrategy(
		http.StatusServiceUnavailable,
		headers,
		`{"error":"degraded"}`,
	)

	resp, err := strategy.Execute(nil, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", resp.Header.Get("Content-Type"), "application/json")
	}
	if resp.Header.Get("X-Custom-Head") != "custom-value" {
		t.Fatalf("X-Custom-Head = %q, want %q", resp.Header.Get("X-Custom-Head"), "custom-value")
	}
	if resp.Header.Get("Retry-After") != "120" {
		t.Fatalf("Retry-After = %q, want %q", resp.Header.Get("Retry-After"), "120")
	}
}

// ---------- 编译期接口检查 ----------

func TestStaticResponseStrategy_ImplementsStrategy(t *testing.T) {
	var s Strategy = NewStaticResponseStrategy(http.StatusOK, nil, "")
	_ = s
}

// ---------- helper ----------

func newTestMonitor() *monitor.Monitor {
	m := monitor.NewMonitor(monitor.TimeWindowConfig{
		Size:        time.Second,
		BucketCount: 10,
	})
	m.InitErrorWindow("test", monitor.TimeWindowConfig{
		Size:        time.Second,
		BucketCount: 10,
	})
	return m
}
