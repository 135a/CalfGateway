package config

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync/atomic"

	"github.com/goccy/go-yaml"
	"github.com/redis/go-redis/v9"
)

const (
	ConfigRedisKey     = "gateway:config"
	ConfigRedisChannel = "gateway:config:reload"
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
		// Write it to Redis so it's there
		_ = p.PushToRedis(ctx, cfg)
	} else {
		// Update local backup
		p.backupLocal(cfg)
	}

	p.configVal.Store(cfg)
	return nil
}

func (p *Provider) loadFromRedis(ctx context.Context) (*Config, error) {
	data, err := p.rdb.Get(ctx, ConfigRedisKey).Result()
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, err
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
	pubsub := p.rdb.Subscribe(ctx, ConfigRedisChannel)
	defer pubsub.Close()

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			log.Printf("received config update notification: %s", msg.Payload)
			cfg, err := p.loadFromRedis(ctx)
			if err != nil {
				log.Printf("failed to reload config from redis: %v", err)
				continue
			}
			p.configVal.Store(cfg)
			p.backupLocal(cfg)
			log.Println("config updated successfully in memory")

			// Notify listeners
			select {
			case p.onUpdateCh <- struct{}{}:
			default:
			}
		}
	}
}

func (p *Provider) OnUpdate() <-chan struct{} {
	return p.onUpdateCh
}

func (p *Provider) PushToRedis(ctx context.Context, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	err = p.rdb.Set(ctx, ConfigRedisKey, data, 0).Err()
	if err != nil {
		return err
	}
	// Publish update
	return p.rdb.Publish(ctx, ConfigRedisChannel, "updated").Err()
}
