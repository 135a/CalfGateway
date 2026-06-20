package degradation

import (
	"context"
	"net/http"

	"CalfGateway/internal/monitor"
)

// Strategy 降级策略接口
type Strategy interface {
	// Execute 执行降级策略，返回 HTTP 响应
	Execute(ctx context.Context, req *http.Request) (*http.Response, error)
	// Name 返回策略名称
	Name() string
}

// Manager 降级管理器——管理所有注册的策略
type Manager struct {
	strategies map[string]Strategy
}

func NewManager() *Manager {
	return &Manager{
		strategies: make(map[string]Strategy),
	}
}

// Register 注册一个降级策略（以 Name() 为 key）
func (m *Manager) Register(s Strategy) {
	m.strategies[s.Name()] = s
}

func (m *Manager) GetStrategy(name string) (Strategy, bool) {
	s, ok := m.strategies[name]
	return s, ok
}

// RouteThreshold 单个路由的降级判定阈值
type RouteThreshold struct {
	RouteName          string  // 路由名称
	CPUThreshold       float64 // CPU 最大利用率阈值 (0-100)，0=不启用
	QPSThreshold       float64 // 全局 QPS 阈值，0=不启用
	ErrorRateThreshold float64 // 本路由错误率阈值 (0-1)，0=不启用
}

// DegradeReason 降级原因
type DegradeReason int

const (
	ReasonNone          DegradeReason = iota
	ReasonCPUOverload                 // CPU 超阈值
	ReasonHighErrorRate               // 错误率超阈值
	ReasonQPSOverload                 // QPS 超阈值
)

func (r DegradeReason) String() string {
	switch r {
	case ReasonCPUOverload:
		return "cpu_overload"
	case ReasonHighErrorRate:
		return "high_error_rate"
	case ReasonQPSOverload:
		return "qps_overload"
	default:
		return ""
	}
}

// Judge 降级判定器——每个路由一个实例
type Judge struct {
	monitor   *monitor.Monitor
	threshold RouteThreshold
}

func NewJudge(m *monitor.Monitor, threshold RouteThreshold) *Judge {
	return &Judge{monitor: m, threshold: threshold}
}

func (j *Judge) Threshold() RouteThreshold {
	return j.threshold
}

// ShouldDegrade 判定本路由是否应该降级
func (j *Judge) ShouldDegrade() (bool, DegradeReason) {
	metrics := j.monitor.GetMetrics(j.threshold.RouteName)

	if j.threshold.CPUThreshold > 0 && metrics.CPUPercent > j.threshold.CPUThreshold {
		return true, ReasonCPUOverload
	}
	if j.threshold.QPSThreshold > 0 && metrics.QPS > j.threshold.QPSThreshold {
		return true, ReasonQPSOverload
	}
	if j.threshold.ErrorRateThreshold > 0 && metrics.ErrorRate > j.threshold.ErrorRateThreshold {
		return true, ReasonHighErrorRate
	}

	return false, ReasonNone
}
