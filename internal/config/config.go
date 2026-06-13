package config

import (
	"os"
	"time"

	"github.com/goccy/go-yaml"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Auth      AuthConfig      `yaml:"auth"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
	Routes    []RouteConfig   `yaml:"routes"`
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
	Enabled bool    `yaml:"enabled"`
	Rate    float64 `yaml:"rate"`
	Burst   int     `yaml:"burst"`
}

type RouteConfig struct {
	Name        string          `yaml:"name"`
	Path        string          `yaml:"path"`
	StripPrefix string          `yaml:"strip_prefix"`
	Target      string          `yaml:"target"`
	Methods     []string        `yaml:"methods"`
	RateLimit   RateLimitConfig `yaml:"rate_limit"`
	Breaker     BreakerConfig   `yaml:"breaker"`
}

type BreakerConfig struct {
	Enabled bool `yaml:"enabled"`
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
