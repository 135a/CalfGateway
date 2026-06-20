package degradation

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// CacheEntry 缓存条目
type CacheEntry struct {
	StatusCode int
	Body       []byte
	ExpiresAt  time.Time
}

// CacheStrategy 缓存降级策略
type CacheStrategy struct {
	mu                sync.RWMutex
	cache             map[string]*CacheEntry
	ttl               time.Duration
	maxEntries        int
	cacheableStatuses []int
}

func NewCacheStrategy(ttl time.Duration, maxEntries int, cacheableStatuses []int) *CacheStrategy {
	return &CacheStrategy{
		cache:             make(map[string]*CacheEntry),
		ttl:               ttl,
		maxEntries:        maxEntries,
		cacheableStatuses: cacheableStatuses,
	}
}

func (s *CacheStrategy) Name() string { return "cache" }

// Execute 尝试从缓存中获取响应
func (s *CacheStrategy) Execute(ctx context.Context, req *http.Request) (*http.Response, error) {
	key := req.URL.String()

	s.mu.RLock()
	entry, ok := s.cache[key]
	s.mu.RUnlock()

	if ok && time.Now().Before(entry.ExpiresAt) {
		return &http.Response{
			StatusCode: entry.StatusCode,
			Body:       io.NopCloser(bytes.NewReader(entry.Body)),
		}, nil
	}

	return nil, fmt.Errorf("cache miss: %s", key)
}

// Store 存储缓存（由正向代理流程调用）
func (s *CacheStrategy) Store(req *http.Request, statusCode int, body []byte) {
	// 检查是否可缓存
	if !s.isCacheable(statusCode) {
		return
	}

	key := req.URL.String()

	s.mu.Lock()
	defer s.mu.Unlock()

	// 淘汰过期条目
	if len(s.cache) >= s.maxEntries {
		for k, v := range s.cache {
			if time.Now().After(v.ExpiresAt) {
				delete(s.cache, k)
			}
		}
	}

	bodyCopy := make([]byte, len(body))
	copy(bodyCopy, body)

	s.cache[key] = &CacheEntry{
		StatusCode: statusCode,
		Body:       bodyCopy,
		ExpiresAt:  time.Now().Add(s.ttl),
	}
}

func (s *CacheStrategy) isCacheable(statusCode int) bool {
	for _, code := range s.cacheableStatuses {
		if statusCode == code {
			return true
		}
	}
	return false
}

func (s *CacheStrategy) Invalidate(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, key)
}
