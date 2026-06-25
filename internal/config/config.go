package config

import (
	"os"
	"time"

	"github.com/goccy/go-yaml"
)

type Config struct {
	Server      ServerConfig      `yaml:"server"`
	Auth        AuthConfig        `yaml:"auth"`
	Redis       RedisConfig       `yaml:"redis"`
	RateLimit   RateLimitConfig   `yaml:"rate_limit"`
	Degradation DegradationConfig `yaml:"degradation"`
	Routes      []RouteConfig     `yaml:"routes"`
	Proxy       ProxyConfig       `yaml:"proxy"`
}
type ProxyConfig struct {
	MaxIdleConns        int `yaml:"max_idle_conns"`
	MaxIdleConnsPerHost int `yaml:"max_idle_conns_per_host"`
	MaxConnsPerHost     int `yaml:"max_conns_per_host"`
}
type LimitConfig struct {
	Rate  float64 `yaml:"rate"`
	Burst int     `yaml:"burst"`
}

type ServerConfig struct {
	Port         int           `yaml:"port"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	GracePeriod  time.Duration `yaml:"grace_period"`
}

type AuthConfig struct {
	Enabled     bool     `yaml:"enabled"`
	PublicPaths []string `yaml:"public_paths"`
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	PoolSize int    `yaml:"pool_size"`
}

type RateLimitConfig struct {
	Enabled   bool        `yaml:"enabled"`
	Global    LimitConfig `yaml:"global"`
	PerClient LimitConfig `yaml:"per_client"`
}

type RouteConfig struct {
	Name        string             `yaml:"name"`
	Path        string             `yaml:"path"`
	StripPrefix string             `yaml:"strip_prefix"`
	Target      string             `yaml:"target"`
	Methods     []string           `yaml:"methods"`
	RateLimit   RateLimitConfig    `yaml:"rate_limit"`
	Breaker     BreakerConfig      `yaml:"breaker"`
	Degradation *DegradationConfig `yaml:"degradation,omitempty"`
}

type DegradationConfig struct {
	Enabled  bool                    `yaml:"enabled"`
	Strategy string                  `yaml:"strategy"` // static_response
	Static   StaticDegradationConfig `yaml:"static_response"`

	// 自动降级阈值（0 = 不启用该项判定）
	CPUThreshold       float64 `yaml:"cpu_threshold"`
	QPSThreshold       float64 `yaml:"qps_threshold"`
	ErrorRateThreshold float64 `yaml:"error_rate_threshold"`

	QPSWindow   WindowConfig `yaml:"qps_window"`
	ErrorWindow WindowConfig `yaml:"error_window"`
}

// WindowConfig 滑动窗口配置
type WindowConfig struct {
	Size        time.Duration `yaml:"size"`
	BucketCount int           `yaml:"bucket_count"`
}

type StaticDegradationConfig struct {
	StatusCode int               `yaml:"status_code"`
	Headers    map[string]string `yaml:"headers"`
	Body       string            `yaml:"body"`
}
type BreakerConfig struct {
	Enabled                  bool          `yaml:"enabled"`
	ErrorThresholdCount      int           `yaml:"error_threshold_count"`
	ErrorThresholdPercentage float64       `yaml:"error_threshold_percentage"`
	Interval                 time.Duration `yaml:"interval"`
	Timeout                  time.Duration `yaml:"timeout"`
	MaxRequests              int           `yaml:"max_requests"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
