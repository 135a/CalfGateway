package degradation

import (
	"log"
	"sync/atomic"
	"time"
)

// DegradationEvent 降级事件
type DegradationEvent struct {
	Time     time.Time
	Route    string
	Strategy string
	Reason   string
}

// Recorder 降级事件记录器
type Recorder struct {
	totalCount atomic.Int64
}

// NewRecorder 创建降级事件记录器
func NewRecorder() *Recorder {
	return &Recorder{}
}

// Record 记录一次降级事件
func (r *Recorder) Record(route, strategy, reason string) {
	r.totalCount.Add(1)
	log.Printf("[降级] route=%s strategy=%s reason=%s", route, strategy, reason)
}

// TotalCount 返回降级总次数
func (r *Recorder) TotalCount() int64 {
	return r.totalCount.Load()
}
