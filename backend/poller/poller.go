package poller

import (
	"context"
	"log"
	"sync"
	"time"

	"tron-signal/backend/source"
)

// Poller 负责周期性拉取新区块
// - 仅 HTTP 轮询
// - 多源并发，先到先用
// - 支持启动 / 停止
type Poller struct {
	manager *source.Manager

	interval time.Duration

	mu      sync.Mutex
	running bool

	ctx    context.Context
	cancel context.CancelFunc

	onBlock func(b *source.Block)
}

// NewPoller 创建轮询器
func NewPoller(mgr *source.Manager, interval time.Duration) *Poller {
	return &Poller{
		manager: mgr,
		interval: interval,
	}
}

// SetInterval 动态修改轮询间隔（秒级）
func (p *Poller) SetInterval(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interval = d
}

// OnBlock 注册新区块回调
func (p *Poller) OnBlock(fn func(b *source.Block)) {
	p.onBlock = fn
}

// Start 启动轮询
func (p *Poller) Start() {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.mu.Unlock()

	log.Println("POLLING_START")

	go p.loop()
}

// Stop 停止轮询
func (p *Poller) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return
	}
	p.running = false
	if p.cancel != nil {
		p.cancel()
	}
	log.Println("POLLING_STOP")
}

func (p *Poller) loop() {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.tick()
		}
	}
}

func (p *Poller) tick() {
	ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
	defer cancel()

	block, err := p.manager.FetchFirstAvailable(ctx)
	if err != nil {
		log.Printf("POLL_ERROR: %v\n", err)
		return
	}

	if p.onBlock != nil {
		p.onBlock(block)
	}
}
