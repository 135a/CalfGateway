package config

import (
	"os"
	"time"

	"github.com/goccy/go-yaml"
)

type Config struct {
	Server      ServerConfig      `yaml:"server"`
	Auth        AuthConfig        `yaml:"auth"`
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
	Secret      string   `yaml:"secret"`
	PublicPaths []string `yaml:"public_paths"`
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
	Strategy string                  `yaml:"strategy"`
	Cache    CacheDegradationConfig  `yaml:"cache"`
	Static   StaticDegradationConfig `yaml:"static_response"`

	// 自动降级指标
	CPUThreshold         float64       `yaml:"cpu_threshold"`
	QPSThreshold         float64       `yaml:"qps_threshold"`
	ErrorRateThreshold   float64       `yaml:"error_rate_threshold"`
	QPSWindowSize        time.Duration `yaml:"qps_window_size"`
	QPSWindowBucketCount int           `yaml:"qps_window_bucket_count"`
	ErrorWindowSize      time.Duration `yaml:"error_window_size"`
	ErrorWindowBucketCnt int           `yaml:"error_window_bucket_count"`
	MinSamples           int           `yaml:"min_samples"`
}

type CacheDegradationConfig struct {
	TTL               time.Duration `yaml:"ttl"`
	MaxEntries        int           `yaml:"max_entries"`
	CacheableStatuses []int         `yaml:"cacheable_statuses"`
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
