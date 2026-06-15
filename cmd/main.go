package main

import (
	"fmt"
	"log"

	"CalfGateway/internal/config"
	"CalfGateway/internal/proxy"
)

func main() {
	cfg, err := config.LoadConfig("../config.yaml")
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	p := proxy.NewProxy(cfg)

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	log.Printf("Starting proxy on %s", addr)
	if err := p.Run(addr); err != nil {
		log.Fatalf("failed to run proxy: %v", err)
	}
}
