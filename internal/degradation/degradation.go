package degradation

import (
	"context"
	"fmt"
	"net/http"
)

// Strategy 降级策略接口
type Strategy interface {
	// Execute 执行降级策略，返回 HTTP 响应
	Execute(ctx context.Context, req *http.Request) (*http.Response, error)
	// Name 返回策略名称
	Name() string
}

// Response 降级响应值对象
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// ToHTTPResponse 将降级响应转换为 *http.Response
func (r *Response) ToHTTPResponse() *http.Response {
	return &http.Response{
		StatusCode: r.StatusCode,
		Header:     r.Header,
		Body:       NewBytesReadCloser(r.Body),
	}
}

// Manager 降级管理器
type Manager struct {
	strategies map[string]Strategy
}

// NewManager 创建降级管理器
func NewManager() *Manager {
	return &Manager{
		strategies: make(map[string]Strategy),
	}
}

// Register 注册降级策略
func (m *Manager) Register(strategy Strategy) {
	m.strategies[strategy.Name()] = strategy
}

// Get 获取指定名称的降级策略
func (m *Manager) Get(name string) (Strategy, error) {
	s, ok := m.strategies[name]
	if !ok {
		return nil, fmt.Errorf("degradation strategy not found: %s", name)
	}
	return s, nil
}
