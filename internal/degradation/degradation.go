package degradation

import (
	"context"
	"net/http"
)

// Strategy 降级策略接口
// 目前内置两种实现：StaticResponseStrategy（静态响应）、CacheStrategy（缓存响应）
type Strategy interface {
	// Execute 执行降级策略，返回降级用的 HTTP 响应
	Execute(ctx context.Context, req *http.Request) (*http.Response, error)
	// Name 返回策略名称
	Name() string
}
