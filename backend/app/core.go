package app

import (
	"log"
	"strconv"
	"sync"
	"time"

	"tron-signal/backend/block"
	"tron-signal/backend/judge"
	"tron-signal/backend/machine"
	"tron-signal/backend/source"
)

// Broadcaster：只负责广播信号（WS），不参与任何判断
type Broadcaster interface {
	BroadcastSignal(payload any)
}

// Core：串联整个后端主流程
type Core struct {
	mu sync.RWMutex

	// 核心组件
	ring    *block.RingBuffer
	judge   *judge.Judge
	mgr     *machine.Manager
	ws      Broadcaster

	// 状态
	listening  bool
	lastHeight int64
	lastHash   string
	lastTime   time.Time

	// 统计
	reconnects int64

	// 重大事故计数
	majorIncidents int64
}

func NewCore(
	ring *block.RingBuffer,
	j *judge.Judge,
	mgr *machine.Manager,
	ws Broadcaster,
) *Core {
	return &Core{
		ring:      ring,
		judge:     j,
		mgr:       mgr,
		ws:        ws,
		listening: true,
	}
}

// OnBlock：轮询到新区块后的唯一入口（先到先用已在上层 dispatcher 完成）
func (c *Core) OnBlock(b *source.Block) {
	if b == nil || b.Hash == "" || b.Height == "" {
		return
	}

	// 1) 去重：height+hash
	if !c.ring.Push(b) {
		// 重复区块直接丢弃，不进入状态机
		return
	}

	// 2) 高度跳跃检测（重大事故）
	h := mustParseInt64(b.Height)
	c.mu.Lock()
	prevHeight := c.lastHeight
	c.lastHeight = h
	c.lastHash = b.Hash
	c.lastTime = b.Time
	c.mu.Unlock()

	if prevHeight > 0 && h > prevHeight+1 {
		c.majorIncidents++
		log.Printf("MAJOR_BLOCK_GAP prev=%d now=%d diff=%d\n", prevHeight, h, h-prevHeight)
	}

	// 3) 判定 ON / OFF
	st := c.judge.Decide(b.Hash)

	// 4) 状态机处理（多实例）
	sigs := c.mgr.ProcessBlock(h, st, time.Now())

	// 5) WS 广播信号（仅广播）
	for _, s := range sigs {
		if s == nil {
			continue
		}
		payload := map[string]any{
			"type":       s.Type,       // "TRIGGER" / "HIT"
			"machine_id": s.MachineID,
			"height":     s.Height,
			"time":       s.Time.Unix(), // 方便前端统一格式化（UI 要北京时间由前端处理）
		}
		if c.ws != nil {
			c.ws.BroadcastSignal(payload)
		}
	}
}

// StopAllMachinesAndReset：切换判定规则时调用（清单 205）
func (c *Core) StopAllMachinesAndReset() {
	// 这里不改变 machine.Config.Enabled
	// 只清空运行态，强制 waitingReverse=true
	c.mgr.ResetAllRuntime()
	log.Printf("RULE_SWITCH_RESET_ALL\n")
}

// Status：供 /api/status 与 SSE 使用
func (c *Core) Status() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return map[string]any{
		"Listening":       c.listening,
		"LastHeight":      c.lastHeight,
		"LastHash":        c.lastHash,
		"LastTimeUnix":    c.lastTime.Unix(),
		"Reconnects":      c.reconnects,
		"MajorIncidents":  c.majorIncidents,
		"JudgeRule":       string(c.judge.GetRule()),
		"JudgeRuleLabel":  judgeLabelCN(c.judge.GetRule()),
	}
}

// Blocks：供 UI 区块列表（最近 50）使用
func (c *Core) Blocks() []source.Block {
	return c.ring.List()
}

func mustParseInt64(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func judgeLabelCN(r judge.RuleType) string {
	switch r {
	case judge.Lucky:
		return "幸运"
	case judge.Big:
		return "大小"
	case judge.Odd:
		return "单双"
	default:
		return string(r)
	}
}
