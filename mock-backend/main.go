// Mock 后端：压测、鉴权透传、熔断测试。
//
// 用法：
//
//	go run .                    # 默认 :8080（service-v1）
//	go run . -port 8082         # :8082（service-v2）
//
// 熔断测试示例：
//
//	curl http://127.0.0.1:8082/control/mode?state=fail   # /api/hello 开始返回 500
//	curl http://127.0.0.1:8100/v2/api/hello              # 经网关连打 ≥5 次触发熔断
//	curl http://127.0.0.1:8082/control/mode?state=ok     # 恢复 200，等待半开探测
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"sync/atomic"
)

var failMode atomic.Bool

func main() {
	port := flag.String("port", "8080", "监听端口，如 8080 或 8082")
	flag.Parse()

	mux := http.NewServeMux()

	mux.HandleFunc("/api/hello", handleHello)
	mux.HandleFunc("/api/error", handleError)
	mux.HandleFunc("/control/mode", handleControlMode)
	mux.HandleFunc("/control/status", handleControlStatus)

	addr := ":" + *port
	log.Printf("mock backend listening on %s", addr)
	log.Printf("endpoints: GET /api/hello  GET /api/error  GET /control/mode?state=ok|fail  GET /control/status")
	log.Fatal(http.ListenAndServe(addr, mux))
}

func handleHello(w http.ResponseWriter, r *http.Request) {
	if failMode.Load() {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": "mock backend forced failure",
		})
		return
	}

	userID := r.Header.Get("X-User-ID")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"data":    "hello world",
		"user_id": userID,
	})
}

func handleError(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusInternalServerError, map[string]any{
		"ok":    false,
		"error": "mock internal server error",
	})
}

func handleControlMode(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	switch state {
	case "fail":
		failMode.Store(true)
		writeJSON(w, http.StatusOK, map[string]any{"mode": "fail", "message": "/api/hello will return 500"})
	case "ok", "":
		failMode.Store(false)
		writeJSON(w, http.StatusOK, map[string]any{"mode": "ok", "message": "/api/hello will return 200"})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "state must be ok or fail",
		})
	}
}

func handleControlStatus(w http.ResponseWriter, r *http.Request) {
	mode := "ok"
	if failMode.Load() {
		mode = "fail"
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": mode})
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
