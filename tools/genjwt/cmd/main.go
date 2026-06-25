package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"CalfGateway/internal/config"
	"CalfGateway/tools/genjwt"
)

func main() {
	configPath := flag.String("config", "config.yaml", "网关配置文件路径")
	secret := flag.String("secret", "my-gateway-jwt-secret-at-least-32-chars", "JWT 签名密钥；留空则从 config.yaml 的 auth.secret 读取")
	sub := flag.String("sub", "user-123", "JWT sub，会透传为 X-User-ID")
	ttl := flag.Duration("ttl", 2*time.Hour, "有效期，如 2h、30m")
	baseURL := flag.String("url", "http://127.0.0.1:8100/v1/api/hello", "测试用请求地址")
	flag.Parse()

	signSecret := *secret
	if signSecret == "" {
		cfg, err := config.LoadConfig(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "读取配置失败: %v\n", err)
			os.Exit(1)
		}
		signSecret = cfg.Auth.Secret
		fmt.Fprintf(os.Stderr, "已从 %s 读取 auth.secret（长度 %d）\n", *configPath, len(signSecret))
	}

	token, err := genjwt.Generate(signSecret, *sub, *ttl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "生成 token 失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("JWT token:")
	fmt.Println(token)
	fmt.Println()
	fmt.Println("curl 示例:")
	fmt.Printf("curl -H \"Authorization: Bearer %s\" %s\n", token, *baseURL)
}
