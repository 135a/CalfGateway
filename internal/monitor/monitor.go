package monitor

import (
	"context"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
)

// DegradationRecorder 降级事件记录器接口（避免循环依赖）
type DegradationRecorder interface {
	Record(route, strategy, reason string)
	TotalCount() int64
}

// Monitor 网关监控器——采集原始指标，按需组装 DegradationMetrics
type Monitor struct {
	ctx          context.Context
	cancel       context.CancelFunc
	cpuPercent   float64
	qpsWindow    *QPSWindow              // QPS 时间窗口（全局一个）
	errorWindows map[string]*ErrorWindow // 错误率时间窗口（每路由一个）
	degRecorder  DegradationRecorder     // 降级事件记录器
}

// NewMonitor 创建 Monitor（QPS 窗口使用全局 qps_window 配置）
func NewMonitor(qpsCfg TimeWindowConfig) *Monitor {
	return &Monitor{
		qpsWindow:    NewQPSWindow(qpsCfg),
		errorWindows: make(map[string]*ErrorWindow),
	}
}

// Start 启动 CPU 采样协程
func (m *Monitor) Start() {
	m.ctx, m.cancel = context.WithCancel(context.Background())
	go m.cpuSampler()
}

// cpuSampler CPU 采样协程——每 5s 采集一次
func (m *Monitor) cpuSampler() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			percents, _ := cpu.Percent(0, true)
			maxCPU := 0.0
			for _, p := range percents {
				if p > maxCPU {
					maxCPU = p
				}
			}
			m.cpuPercent = maxCPU
		case <-m.ctx.Done():
			return
		}
	}
}
func (m *Monitor) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
}

// InitErrorWindow 初始化指定路由的错误率时间窗口
// 不同路由可以使用不同的窗口参数
// cfg 优先级：路由自己的 error_window > 全局默认 error_window
func (m *Monitor) InitErrorWindow(routeName string, cfg TimeWindowConfig) {
	m.errorWindows[routeName] = NewErrorWindow(cfg)
}

// Record 每次请求完成后调用——更新 QPS + 错误率统计
// errType 用于判断是否计入错误率（ErrBackend5xx / ErrNetwork 才计入）
func (m *Monitor) Record(routeName string, errType ErrType) {
	m.qpsWindow.Record()
	if w, ok := m.errorWindows[routeName]; ok {
		success := !(errType == ErrBackend5xx || errType == ErrNetwork)
		w.Record(success)
	}
}

// DegradationMetrics 降级指标集——包含降级判定所需的全部指标
type DegradationMetrics struct {
	CPUPercent float64 // 单核 CPU 最大利用率 (0-100)
	QPS        float64 // 全局 QPS
	ErrorRate  float64 // 本路由错误率 (0-1)
}

// GetMetrics 获取指定路由的降级指标集
func (m *Monitor) GetMetrics(routeName string) DegradationMetrics {
	errRate := 0.0
	if w, ok := m.errorWindows[routeName]; ok {
		errRate = w.ErrorRate()
	}
	return DegradationMetrics{
		CPUPercent: m.cpuPercent,
		QPS:        m.qpsWindow.QPS(),
		ErrorRate:  errRate,
	}
}

// SetDegRecorder 设置降级事件记录器
func (m *Monitor) SetDegRecorder(r DegradationRecorder) {
	m.degRecorder = r
}

// DegRecorder 获取降级事件记录器
func (m *Monitor) DegRecorder() DegradationRecorder {
	return m.degRecorder
}

// QPSWindow QPS 统计窗口——只统计请求数，不区分成功/失败
type QPSWindow struct {
	window *SlidingWindow
}

func NewQPSWindow(cfg TimeWindowConfig) *QPSWindow {
	return &QPSWindow{window: NewSlidingWindow(cfg)}
}

func (w *QPSWindow) Record() {
	w.window.IncRequests()
}

func (w *QPSWindow) QPS() float64 {
	return w.window.RequestsPerSecond()
}

// ErrorWindow 错误率统计窗口——同时统计请求数和错误数
type ErrorWindow struct {
	window *SlidingWindow
}

func NewErrorWindow(cfg TimeWindowConfig) *ErrorWindow {
	return &ErrorWindow{window: NewSlidingWindow(cfg)}
}

// Record 记录一次请求，success=false 时同时记入错误数
func (w *ErrorWindow) Record(success bool) {
	w.window.IncRequests()
	if !success {
		w.window.IncErrors()
	}
}

func (w *ErrorWindow) ErrorRate() float64 {
	return w.window.ErrorRate()
}

// ErrType 错误类型——区分哪些计入错误率
type ErrType int

const (
	ErrNone        ErrType = iota // 后端正常 (2xx/3xx/4xx)
	ErrBackend5xx                 // 后端 5xx → 计入错误率
	ErrNetwork                    // 网络错误 → 计入错误率
	ErrClient4xx                  // 客户端 4xx（不进后端）→ 不计入
	ErrDegraded                   // 网关主动降级 → 不计入
	ErrRateLimited                // 网关限流 429 → 不计入
)

// TimeWindowConfig 时间窗口配置
type TimeWindowConfig struct {
	Size        time.Duration `yaml:"size"`         // 窗口总大小，如 10s
	BucketCount int           `yaml:"bucket_count"` // 桶数量，决定精度，如 10
}

// bucket 滑动窗口中的单个桶
type bucket struct {
	requests int // 总请求数
	errors   int // 错误数（仅后端错误）
}

// SlidingWindow 滑动窗口（通用底层实现）
type SlidingWindow struct {
	buckets  []*bucket
	size     int           // bucket 数量
	interval time.Duration // 每个 bucket 时长 = Size / BucketCount
	cursor   int
	lastTick time.Time
	mu       sync.Mutex
}

func NewSlidingWindow(cfg TimeWindowConfig) *SlidingWindow {
	bucketInterval := cfg.Size / time.Duration(cfg.BucketCount)
	buckets := make([]*bucket, cfg.BucketCount)
	for i := range buckets {
		buckets[i] = &bucket{}
	}
	return &SlidingWindow{
		buckets:  buckets,
		size:     cfg.BucketCount,
		interval: bucketInterval,
		lastTick: time.Now(),
	}
}

func (sw *SlidingWindow) getCurrentBucket() *bucket {
	now := time.Now()
	elapsed := now.Sub(sw.lastTick)

	if elapsed < sw.interval {
		return sw.buckets[sw.cursor]
	}

	steps := int(elapsed / sw.interval)
	if steps > sw.size {
		steps = sw.size
	}

	for i := 0; i < steps; i++ {
		sw.cursor = (sw.cursor + 1) % sw.size
		sw.buckets[sw.cursor] = &bucket{}
	}

	sw.lastTick = sw.lastTick.Add(time.Duration(steps) * sw.interval)
	return sw.buckets[sw.cursor]
}

func (sw *SlidingWindow) IncRequests() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.getCurrentBucket().requests++
}

func (sw *SlidingWindow) IncErrors() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.getCurrentBucket().errors++
}

func (sw *SlidingWindow) RequestsPerSecond() float64 {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	totalRequests := 0
	for _, b := range sw.buckets {
		totalRequests += b.requests
	}

	windowDuration := sw.size * int(sw.interval/time.Second)
	return float64(totalRequests) / float64(windowDuration)
}

func (sw *SlidingWindow) ErrorRate() float64 {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	totalRequests := 0
	totalErrors := 0
	for _, b := range sw.buckets {
		totalRequests += b.requests
		totalErrors += b.errors
	}

	if totalRequests == 0 {
		return 0.0
	}
	return float64(totalErrors) / float64(totalRequests)
}

func (sw *SlidingWindow) TotalRequests() int {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	total := 0
	for _, b := range sw.buckets {
		total += b.requests
	}
	return total
}
