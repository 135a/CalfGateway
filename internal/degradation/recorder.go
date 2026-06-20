package degradation

import (
	"log"
	"sync/atomic"
)

// Recorder 降级事件记录器
type Recorder struct {
	totalCount atomic.Int64
}

func NewRecorder() *Recorder {
	return &Recorder{}
}

// Record 记录一次降级事件
func (r *Recorder) Record(route, strategy, reason string) {
	r.totalCount.Add(1)
	log.Printf("[降级] route=%s strategy=%s reason=%s", route, strategy, reason)
}

func (r *Recorder) TotalCount() int64 {
	return r.totalCount.Load()
}
