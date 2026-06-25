package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"CalfGateway/internal/config"
	"CalfGateway/internal/proxy"
)

func main() {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "127.0.0.1:6379"
	}
	redisPass := os.Getenv("REDIS_PASS")

	// 初始化动态配置 Provider
	provider := config.NewProvider(redisAddr, redisPass, "../config.yaml")
	
	// Init 会尝试从 Redis 读取，如果失败则从本地读取，并同步到 Redis 和本地备份
	if err := provider.Init(context.Background()); err != nil {
		log.Fatalf("failed to init config provider: %v", err)
	}

	// 启动后台协程，订阅 Redis 的配置变更消息
	go provider.Subscribe(context.Background())

	// 代理网关会将 Provider 作为数据源，支持动态重建路由表
	p := proxy.NewProxy(provider)

	cfg := provider.Get()
	if cfg == nil {
		log.Fatalf("config is nil after init")
	}

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	log.Printf("Starting proxy on %s, connected to redis at %s", addr, redisAddr)
	if err := p.Run(addr); err != nil {
		log.Fatalf("failed to run proxy: %v", err)
	}
}
