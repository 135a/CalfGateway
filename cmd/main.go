package main

import (
	"net/http/httputil"
	"net/url"
	"os"

	"github.com/gin-gonic/gin"
)

func main() {
	// 后端目标地址，可从环境变量读取
	target := os.Getenv("PROXY_TARGET")
	if target == "" {
		target = "http://localhost:8081"
	}

	targetURL, _ := url.Parse(target)
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	r := gin.Default()
	r.Any("/api/**", func(c *gin.Context) {
		proxy.ServeHTTP(c.Writer, c.Request)
	})
	r.Run()
}
