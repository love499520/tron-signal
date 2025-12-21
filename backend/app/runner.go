package app

import (
	"context"
	"log"
	"sync"
	"time"

	"tron-signal/backend/source"
)

// Runner：轮询调度器 + Core 的运行器
type Runner struct {
	mu sync.RWMutex

	core       *Core
	dispatcher *source.Dispatcher

	// 可配置：失败后等待策略（人工 / N 分钟）
	autoRestart bool          // true=自动重试；false=人工（停止后不再跑）
	failWait    time.Duration // N 分钟

	// 全局 tick（基础节拍）
	baseTick time.Duration

	stopCh chan struct{}
}

func NewRunner(core *Core, dispatcher *source.Dispatcher) *Runner {
	return &Runner{
		core:       core,
		dispatcher: dispatcher,
		autoRestart: true,
		failWait:    2 * time.Minute,
		baseTick:    800 * time.Millisecond, // 追求速度：< 1s 更新；各源具体频率由 limiter 决定
		stopCh:      make(chan struct{}),
	}
}

func (r *Runner) UpdatePolicy(auto bool, waitMinutes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autoRestart = auto
	if waitMinutes <= 0 {
		waitMinutes = 1
	}
	r.failWait = time.Duration(waitMinutes) * time.Minute
}

func (r *Runner) UpdateBaseTick(ms int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ms < 200 {
		ms = 200
	}
	r.baseTick = time.Duration(ms) * time.Millisecond
}

func (r *Runner) Stop() {
	select {
	case <-r.stopCh:
		// already stopped
	default:
		close(r.stopCh)
	}
}

// Run：阻塞运行（建议 goroutine 启动）
func (r *Runner) Run() {
	log.Printf("POLL_RUNNER_START\n")
	for {
		select {
		case <-r.stopCh:
			log.Printf("POLL_RUNNER_STOP\n")
			return
		default:
		}

		r.mu.RLock()
		tick := r.baseTick
		auto := r.autoRestart
		wait := r.failWait
		r.mu.RUnlock()

		// 轮询一次
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		b, err := r.dispatcher.FetchAny(ctx)
		cancel()

		if err != nil {
			// 轮询异常 / 源不可用（清单 1003）
			log.Printf("POLL_ERROR: %v\n", err)

			if !auto {
				log.Printf("POLL_STOP_MANUAL_MODE\n")
				return
			}
			// 自动重试：等待 N 分钟
			log.Printf("POLL_WAIT_RETRY wait=%s\n", wait)
			if !sleepOrStop(wait, r.stopCh) {
				return
			}
			continue
		}

		// b != nil：进入核心链路
		r.core.OnBlock(b)

		// 基础节拍（不与各源 limiter 冲突）
		if !sleepOrStop(tick, r.stopCh) {
			return
		}
	}
}

func sleepOrStop(d time.Duration, stop <-chan struct{}) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}
