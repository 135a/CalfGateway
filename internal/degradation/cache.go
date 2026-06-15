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

type cacheEntry struct {
	body      []byte
	expiresAt time.Time
}

// CacheStrategy 缓存降级策略
type CacheStrategy struct {
	mu         sync.RWMutex
	entries    map[string]*cacheEntry
	ttl        time.Duration
	maxEntries int
	statuses   map[int]bool
}

// NewCacheStrategy 创建缓存降级策略
//   - ttl: 缓存有效期
//   - maxEntries: 最大缓存条目数
//   - cacheableStatuses: 可缓存的状态码列表（空表示仅缓存 200）
func NewCacheStrategy(ttl time.Duration, maxEntries int, cacheableStatuses []int) *CacheStrategy {
	statuses := make(map[int]bool)
	if len(cacheableStatuses) == 0 {
		statuses[200] = true
	} else {
		for _, s := range cacheableStatuses {
			statuses[s] = true
		}
	}
	return &CacheStrategy{
		entries:    make(map[string]*cacheEntry),
		ttl:        ttl,
		maxEntries: maxEntries,
		statuses:   statuses,
	}
}

func (s *CacheStrategy) Name() string { return "cache" }

// Execute 从缓存中获取响应
func (s *CacheStrategy) Execute(ctx context.Context, req *http.Request) (*http.Response, error) {
	key := s.buildKey(req)

	s.mu.RLock()
	entry, ok := s.entries[key]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("cache miss: %s", key)
	}

	if time.Now().After(entry.expiresAt) {
		// 惰性删除过期条目
		s.mu.Lock()
		delete(s.entries, key)
		s.mu.Unlock()
		return nil, fmt.Errorf("cache expired: %s", key)
	}

	body := make([]byte, len(entry.body))
	copy(body, entry.body)

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}, nil
}

// Store 存储响应到缓存
func (s *CacheStrategy) Store(req *http.Request, statusCode int, body []byte) {
	if !s.statuses[statusCode] {
		return
	}

	key := s.buildKey(req)

	s.mu.Lock()
	defer s.mu.Unlock()

	// 淘汰过期条目
	if len(s.entries) >= s.maxEntries {
		s.evictExpired()
	}

	// 如果仍然超过上限，不缓存
	if len(s.entries) >= s.maxEntries {
		return
	}

	bodyCopy := make([]byte, len(body))
	copy(bodyCopy, body)

	s.entries[key] = &cacheEntry{
		body:      bodyCopy,
		expiresAt: time.Now().Add(s.ttl),
	}
}

// Invalidate 使指定请求的缓存失效
func (s *CacheStrategy) Invalidate(req *http.Request) {
	key := s.buildKey(req)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
}

// buildKey 从请求构建缓存键
func (s *CacheStrategy) buildKey(req *http.Request) string {
	return req.Method + ":" + req.URL.String()
}

// evictExpired 淘汰所有过期条目
func (s *CacheStrategy) evictExpired() {
	now := time.Now()
	for k, v := range s.entries {
		if now.After(v.expiresAt) {
			delete(s.entries, k)
		}
	}
}
