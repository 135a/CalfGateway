package config

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync/atomic"

	"github.com/goccy/go-yaml"
	"github.com/redis/go-redis/v9"
)

const (
	ConfigRedisGlobalKey = "gateway:config:global"
	ConfigRedisRoutesKey = "gateway:config:routes"
)

type Provider struct {
	rdb        *redis.Client
	configVal  atomic.Value
	localPath  string
	onUpdateCh chan struct{}
}

func NewProvider(redisAddr, redisPass string, localPath string) *Provider {
	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPass,
	})
	return &Provider{
		rdb:        rdb,
		localPath:  localPath,
		onUpdateCh: make(chan struct{}, 1),
	}
}

func (p *Provider) Init(ctx context.Context) error {
	// 1. Try from Redis
	cfg, err := p.loadFromRedis(ctx)
	if err != nil {
		log.Printf("failed to load config from redis: %v, falling back to local file", err)
		// 2. Fallback to local
		cfg, err = LoadConfig(p.localPath)
		if err != nil {
			return fmt.Errorf("failed to load config from local file: %w", err)
		}
		// Write it to Redis in fine-grained hash format
		_ = p.PushToRedis(ctx, cfg)
	} else {
		// Update local backup
		p.backupLocal(cfg)
	}

	p.configVal.Store(cfg)
	return nil
}

func (p *Provider) loadFromRedis(ctx context.Context) (*Config, error) {
	var cfg Config

	// 1. Load Globals
	globals, err := p.rdb.HGetAll(ctx, ConfigRedisGlobalKey).Result()
	if err != nil || len(globals) == 0 {
		return nil, fmt.Errorf("global config not found in redis")
	}

	if val, ok := globals["server"]; ok { json.Unmarshal([]byte(val), &cfg.Server) }
	if val, ok := globals["auth"]; ok { json.Unmarshal([]byte(val), &cfg.Auth) }
	if val, ok := globals["redis"]; ok { json.Unmarshal([]byte(val), &cfg.Redis) }
	if val, ok := globals["rate_limit"]; ok { json.Unmarshal([]byte(val), &cfg.RateLimit) }
	if val, ok := globals["degradation"]; ok { json.Unmarshal([]byte(val), &cfg.Degradation) }
	if val, ok := globals["proxy"]; ok { json.Unmarshal([]byte(val), &cfg.Proxy) }

	// 2. Load Routes
	routesMap, err := p.rdb.HGetAll(ctx, ConfigRedisRoutesKey).Result()
	if err != nil {
		return nil, err
	}
	for _, routeJSON := range routesMap {
		var route RouteConfig
		if err := json.Unmarshal([]byte(routeJSON), &route); err == nil {
			cfg.Routes = append(cfg.Routes, route)
		}
	}

	return &cfg, nil
}

func (p *Provider) backupLocal(cfg *Config) {
	data, err := yaml.Marshal(cfg)
	if err == nil {
		_ = os.WriteFile(p.localPath, data, 0644)
	}
}

func (p *Provider) Get() *Config {
	val := p.configVal.Load()
	if val != nil {
		return val.(*Config)
	}
	return nil
}

func (p *Provider) Subscribe(ctx context.Context) {
	// 暂不实现 Push / PubSub 逻辑
	log.Println("PubSub for fine-grained config is disabled as requested.")
}

func (p *Provider) OnUpdate() <-chan struct{} {
	return p.onUpdateCh
}

func (p *Provider) PushToRedis(ctx context.Context, cfg *Config) error {
	// Push Globals
	globals := make(map[string]interface{})
	if b, err := json.Marshal(cfg.Server); err == nil { globals["server"] = b }
	if b, err := json.Marshal(cfg.Auth); err == nil { globals["auth"] = b }
	if b, err := json.Marshal(cfg.Redis); err == nil { globals["redis"] = b }
	if b, err := json.Marshal(cfg.RateLimit); err == nil { globals["rate_limit"] = b }
	if b, err := json.Marshal(cfg.Degradation); err == nil { globals["degradation"] = b }
	if b, err := json.Marshal(cfg.Proxy); err == nil { globals["proxy"] = b }
	
	if len(globals) > 0 {
		p.rdb.HSet(ctx, ConfigRedisGlobalKey, globals)
	}

	// Push Routes
	if len(cfg.Routes) > 0 {
		routes := make(map[string]interface{})
		for _, route := range cfg.Routes {
			if b, err := json.Marshal(route); err == nil {
				routes[route.Name] = b
			}
		}
		p.rdb.HSet(ctx, ConfigRedisRoutesKey, routes)
	}

	return nil
}
