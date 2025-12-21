package block

import (
	"sync"

	"tron-signal/backend/source"
)

// RingBuffer 用于区块去重 + 最近区块缓存
// - 固定长度（50）
// - 去重 key = height + hash
type RingBuffer struct {
	mu    sync.Mutex
	size  int
	items []source.Block
	index map[string]struct{}
}

// NewRingBuffer 创建 RingBuffer
func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{
		size:  size,
		items: make([]source.Block, 0, size),
		index: make(map[string]struct{}),
	}
}

// Exists 判断区块是否已存在
func (r *RingBuffer) Exists(b *source.Block) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := makeKey(b)
	_, ok := r.index[key]
	return ok
}

// Push 插入新区块
// 返回 true = 新区块
// 返回 false = 重复区块
func (r *RingBuffer) Push(b *source.Block) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := makeKey(b)
	if _, ok := r.index[key]; ok {
		return false
	}

	// 超出长度，移除最旧
	if len(r.items) >= r.size {
		old := r.items[0]
		oldKey := makeKey(&old)
		delete(r.index, oldKey)
		r.items = r.items[1:]
	}

	r.items = append(r.items, *b)
	r.index[key] = struct{}{}
	return true
}

// List 返回当前区块列表（按新→旧）
func (r *RingBuffer) List() []source.Block {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]source.Block, len(r.items))
	copy(out, r.items)

	// 倒序返回（最新在前）
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func makeKey(b *source.Block) string {
	return b.Height + ":" + b.Hash
}
