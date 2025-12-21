package app

import (
	"log"
	"sync"
	"time"

	"tron-signal/backend/block"
	"tron-signal/backend/judge"
	"tron-signal/backend/machine"
	"tron-signal/backend/ws"
)

// Core：系统核心编排
// 职责：
// - 区块去重（RingBuffer）
// - ON/OFF 判定（Judge）
// - 多状态机驱动（Manager）
// - WS 广播信号
// - SSE/状态数据源
// - 重大事故日志（高度跳跃）
type Core struct {
	mu sync.RWMutex

	judge   *judge.Judge
	manager *machine.Manager
	ring    *block.RingBuffer
	hub     *ws.Hub

	// 状态（/api/status, SSE）
	lastHeight string
	lastHash   string
	lastTime   time.Time

	// 高度检测
	lastHeightNum int64 // 用于检测跳跃（解析失败则为 0）

	reconnects int64
}

// NewCore
func NewCore(j *judge.Judge, m *machine.Manager, r *block.RingBuffer, h *ws.Hub) *Core {
	return &Core{
		judge:   j,
		manager: m,
		ring:    r,
		hub:     h,
	}
}

// OnBlock：Runner 每拿到一个新区块就调用
func (c *Core) OnBlock(b *block.Block) {
	if b == nil {
		return
	}

	// 去重（1106）
	if ok := c.ring.AddIfNew(*b); !ok {
		return
	}

	// ===== 高度跳跃检测（1004 重大事故）=====
	hNum := parseHeight(b.Height)
	if c.lastHeightNum > 0 && hNum > 0 {
		if hNum > c.lastHeightNum+1 {
			log.Printf("MAJOR_BLOCK_GAP prev=%d curr=%d\n", c.lastHeightNum, hNum)
		}
	}
	c.lastHeightNum = hNum

	// 更新状态
	c.mu.Lock()
	c.lastHeight = b.Height
	c.lastHash = b.Hash
	c.lastTime = time.Unix(b.TimeUnix, 0)
	c.mu.Unlock()

	// 判定 ON / OFF
	state := c.judge.Decide(b.Hash)

	// 驱动状态机
	signals := c.manager.OnBlock(state, b.Height, b.Hash, b.TimeUnix)

	// 广播信号（WS，仅广播，不承载 UI 状态）
	for _, s := range signals {
		c.hub.Broadcast(s)
	}
}

// ====== 判定规则切换（API 调用）======

// SwitchJudgeRule：
// - 上层已完成二次确认
// - 这里必须：停止所有状态机 + 清空运行态 + 清空 ring
func (c *Core) SwitchJudgeRule(rule judge.RuleType) {
	log.Printf("JUDGE_SWITCH to=%s\n", rule)

	c.manager.StopAll()
	c.manager.ResetAll()
	c.ring.Reset()
	c.judge.SetRule(rule)

	c.mu.Lock()
	c.lastHeightNum = 0
	c.mu.Unlock()
}

// ====== SSE / Status ======

type Status struct {
	Listening bool   `json:"listening"`
	LastHeight string `json:"lastHeight"`
	LastHash   string `json:"lastHash"`
	LastTime   string `json:"lastTimeISO"` // 北京时间 ISO（前端再格式化）
	Reconnects int64  `json:"reconnects"`
}

// GetStatus：供 /api/status & SSE 使用
func (c *Core) GetStatus() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()

	t := c.lastTime
	if t.IsZero() {
		return Status{}
	}
	// 固定 UTC+8
	bt := t.In(time.FixedZone("UTC+8", 8*3600))
	return Status{
		Listening:  true,
		LastHeight: c.lastHeight,
		LastHash:   c.lastHash,
		LastTime:   bt.Format(time.RFC3339),
		Reconnects: c.reconnects,
	}
}

// IncReconnect：供 Runner/Dispatcher 在需要时调用
func (c *Core) IncReconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reconnects++
}

// ====== helpers ======

func parseHeight(h string) int64 {
	var n int64
	for i := 0; i < len(h); i++ {
		if h[i] < '0' || h[i] > '9' {
			return 0
		}
		n = n*10 + int64(h[i]-'0')
	}
	return n
}
