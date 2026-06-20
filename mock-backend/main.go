// Mock 极速后端，用于压测网关时避免真实业务接口成为瓶颈。
//
// 用法：
//
//	go run .                    # 默认 :8080（对应 service-v1）
//	go run . -port 8082         # 对应 service-v2
package main

import (
	"flag"
	"log"
	"net/http"
)

func main() {
	port := flag.String("port", "8080", "监听端口，如 8080 或 8082")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	})

	addr := ":" + *port
	log.Printf("mock backend listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
