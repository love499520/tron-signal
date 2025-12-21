package source

import (
	"context"
	"sync"
	"time"
)

// Dispatcher 并发调度器（先到先用）
type Dispatcher struct {
	mu       sync.RWMutex
	sources  []Fetcher
	timeout  time.Duration
	lastSeen map[string]string // sourceID -> height:hash
}

// NewDispatcher 创建调度器
func NewDispatcher(timeout time.Duration) *Dispatcher {
	return &Dispatcher{
		timeout:  timeout,
		lastSeen: make(map[string]string),
	}
}

// SetSources 设置数据源（顺序由用户 UI 控制）
func (d *Dispatcher) SetSources(srcs []Fetcher) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sources = srcs
}

// FetchAny 并发抓取，先返回有效新区块即采用
func (d *Dispatcher) FetchAny(ctx context.Context) (*Block, error) {
	d.mu.RLock()
	srcs := append([]Fetcher{}, d.sources...)
	d.mu.RUnlock()

	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	ch := make(chan Result, len(srcs))
	wg := sync.WaitGroup{}

	for _, s := range srcs {
		cfg := s.Config()
		if !cfg.Enabled {
			continue
		}
		wg.Add(1)
		go func(f Fetcher) {
			defer wg.Done()
			b, err := f.FetchLatest(ctx)
			ch <- Result{Block: b, Err: err, From: f.ID()}
		}(s)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	for res := range ch {
		if res.Err != nil || res.Block == nil {
			continue
		}
		key := res.Block.Height + ":" + res.Block.Hash
		if d.isNew(res.From, key) {
			d.mark(res.From, key)
			return res.Block, nil
		}
	}
	return nil, context.DeadlineExceeded
}

func (d *Dispatcher) isNew(from, key string) bool {
	last, ok := d.lastSeen[from]
	return !ok || last != key
}

func (d *Dispatcher) mark(from, key string) {
	d.lastSeen[from] = key
}
