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
