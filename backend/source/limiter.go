package source

import (
	"sync"
	"time"
)

// Limiter
// - baseRate：基础频率（次/秒）
// - maxRate：上限频率（次/秒）
// 设计：
// - 正常情况下使用 baseRate 作为令牌补充速度
// - 当系统希望加速（比如用户把 baseRate 调高），仍受 maxRate 硬限制
// - Allow()：是否允许本次请求；不允许则应“跳过该源”，并由上层记录日志/降频策略
type Limiter struct {
	mu sync.Mutex

	baseRate int
	maxRate  int

	tokens      float64
	lastRefill  time.Time
}

// NewLimiter 创建限流器
func NewLimiter(baseRate, maxRate int) *Limiter {
	if baseRate <= 0 {
		baseRate = 1
	}
	if maxRate <= 0 {
		maxRate = baseRate
	}
	if maxRate < baseRate {
		maxRate = baseRate
	}
	return &Limiter{
		baseRate:   baseRate,
		maxRate:    maxRate,
		tokens:     float64(maxRate), // 允许短时间 burst
		lastRefill: time.Now(),
	}
}

// Update 动态更新阈值（UI 改动后热更新）
func (l *Limiter) Update(baseRate, maxRate int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if baseRate <= 0 {
		baseRate = 1
	}
	if maxRate <= 0 {
		maxRate = baseRate
	}
	if maxRate < baseRate {
		maxRate = baseRate
	}

	l.baseRate = baseRate
	l.maxRate = maxRate

	// tokens 上限也随 maxRate 更新
	if l.tokens > float64(l.maxRate) {
		l.tokens = float64(l.maxRate)
	}
}

// Allow 是否允许本次请求
func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(l.lastRefill).Seconds()
	if elapsed > 0 {
		// 按 baseRate 补充令牌
		l.tokens += elapsed * float64(l.baseRate)
		if l.tokens > float64(l.maxRate) {
			l.tokens = float64(l.maxRate)
		}
		l.lastRefill = now
	}

	if l.tokens >= 1 {
		l.tokens -= 1
		return true
	}
	return false
}

// Snapshot 返回当前阈值（用于状态页/UI）
func (l *Limiter) Snapshot() (baseRate int, maxRate int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.baseRate, l.maxRate
}
