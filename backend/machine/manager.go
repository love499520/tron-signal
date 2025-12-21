package machine

import (
	"sync"
	"time"

	"tron-signal/backend/judge"
)

// ====== 配置结构（持久化到 config.json）======

type Config struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`

	// 触发规则
	TriggerState judge.Result `json:"trigger_state"` // ON / OFF
	Threshold    int          `json:"threshold"`     // 滑块

	// HIT 规则（附加）
	HitEnabled bool         `json:"hit_enabled"`
	HitExpect  judge.Result `json:"hit_expect"` // ON / OFF
	HitOffset  int          `json:"hit_offset"` // X（滑块）
}

// ====== 运行态（重启强制清零；切换判定规则也强制清零）======

type Runtime struct {
	// 计数器（展示用，404）
	Counter int `json:"counter"`

	// waitingReverse 机制（1107）
	WaitingReverse bool `json:"waiting_reverse"`

	// HIT 运行态：触发后开始倒数，到 t+X 时只判断一次（102/103/104）
	HitPending   bool `json:"hit_pending"`
	HitCountdown int  `json:"hit_countdown"`

	// 记录触发区块（用于信号字段）
	LastTriggerHeight string `json:"last_trigger_height"`
	LastTriggerHash   string `json:"last_trigger_hash"`
	LastTriggerTime   int64  `json:"last_trigger_time_unix"`
}

// ====== 信号（广播 payload 由上层 Core 组装；字段你已确认“没问题”）======

type SignalType string

const (
	SignalTrigger SignalType = "TRIGGER"
	SignalHit     SignalType = "HIT"
)

type Signal struct {
	Type      SignalType   `json:"type"`
	MachineID string       `json:"machineId"`
	State     judge.Result `json:"state"` // 触发/命中时的状态

	Height string `json:"height"`
	Hash   string `json:"hash"`
	Time   int64  `json:"time"` // unix seconds

	// HIT 关联
	BaseHeight string `json:"baseHeight,omitempty"`
	BaseHash   string `json:"baseHash,omitempty"`
	Offset     int    `json:"offset,omitempty"` // X
}

// ====== Manager：多状态机 ======

type Manager struct {
	mu sync.RWMutex

	cfgs map[string]Config
	rts  map[string]*Runtime

	// 保持 UI 顺序
	order []string
}

func NewManager(cfgList []Config) *Manager {
	m := &Manager{
		cfgs:  map[string]Config{},
		rts:   map[string]*Runtime{},
		order: []string{},
	}
	for _, c := range cfgList {
		m.Upsert(c)
	}
	// 强制清零运行态（你“重启/异常重启都不恢复历史状态”的约束）
	m.ResetAll()
	return m
}

func (m *Manager) List() []Config {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Config, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.cfgs[id])
	}
	return out
}

func (m *Manager) RuntimeSnapshot() map[string]Runtime {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := map[string]Runtime{}
	for id, rt := range m.rts {
		if rt == nil {
			continue
		}
		out[id] = *rt
	}
	return out
}

func (m *Manager) Upsert(c Config) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 默认值修正
	if c.TriggerState != judge.ON && c.TriggerState != judge.OFF {
		c.TriggerState = judge.ON
	}
	if c.Threshold < 1 {
		c.Threshold = 1
	}
	if c.HitExpect != judge.ON && c.HitExpect != judge.OFF {
		c.HitExpect = judge.ON
	}
	if c.HitOffset < 1 {
		c.HitOffset = 1
	}

	if _, ok := m.cfgs[c.ID]; !ok {
		m.order = append(m.order, c.ID)
	}
	m.cfgs[c.ID] = c
	if _, ok := m.rts[c.ID]; !ok {
		m.rts[c.ID] = &Runtime{}
	}
}

func (m *Manager) Delete(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.cfgs, id)
	delete(m.rts, id)

	out := make([]string, 0, len(m.order))
	for _, x := range m.order {
		if x != id {
			out = append(out, x)
		}
	}
	m.order = out
}

// StopAll：把机器都置为 disabled（UI 总开关等价于 machine.enabled）
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, c := range m.cfgs {
		c.Enabled = false
		m.cfgs[id] = c
	}
}

// ResetAll：清空所有运行态（205：切换判定规则必须清空）
func (m *Manager) ResetAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id := range m.rts {
		m.rts[id] = &Runtime{
			Counter:        0,
			WaitingReverse: true,
			HitPending:     false,
			HitCountdown:   0,
		}
	}
}

// ResetOne：清空单机运行态（比如禁用/启用时你想清零）
func (m *Manager) ResetOne(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.rts[id] = &Runtime{
		Counter:        0,
		WaitingReverse: true,
		HitPending:     false,
		HitCountdown:   0,
	}
}

// OnBlock：每到一个新区块（已去重）
// 输入：当前区块判定 state + 区块信息
// 输出：信号列表（触发/命中），由上层广播（WS）
// 约束：
// - HIT 只在“基础触发成功”后启动一次倒数（102）
// - HIT 关闭时不执行任何判断（103）
// - HIT 不影响触发逻辑（104）
// - waitingReverse：触发后必须先见反向状态才恢复计数（1107）
func (m *Manager) OnBlock(state judge.Result, height, hash string, unixSec int64) []Signal {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []Signal

	for _, id := range m.order {
		cfg := m.cfgs[id]
		rt := m.rts[id]
		if rt == nil {
			rt = &Runtime{}
			m.rts[id] = rt
		}

		// 机器总开关
		if !cfg.Enabled {
			// 即便禁用，也允许 HIT pending 自然消亡？你的清单没要求；这里选择：禁用即不再处理任何运行态
			continue
		}

		// ===== HIT 倒数：与触发计数解耦（104）=====
		if cfg.HitEnabled && rt.HitPending {
			rt.HitCountdown--
			if rt.HitCountdown <= 0 {
				// 到达 t+X：只判断一次
				if state == cfg.HitExpect {
					out = append(out, Signal{
						Type:      SignalHit,
						MachineID: id,
						State:     state,
						Height:    height,
						Hash:      hash,
						Time:      unixSec,
						BaseHeight: rt.LastTriggerHeight,
						BaseHash:   rt.LastTriggerHash,
						Offset:     cfg.HitOffset,
					})
				}
				rt.HitPending = false
				rt.HitCountdown = 0
			}
		}

		// ===== waitingReverse：触发后必须先见反向状态 =====
		if rt.WaitingReverse {
			if state == reverse(cfg.TriggerState) {
				rt.WaitingReverse = false
				rt.Counter = 0
			}
			// 等待阶段不计数
			continue
		}

		// ===== 计数阶段 =====
		if state == cfg.TriggerState {
			rt.Counter++
		} else {
			rt.Counter = 0
		}

		if rt.Counter >= cfg.Threshold {
			// 触发
			out = append(out, Signal{
				Type:      SignalTrigger,
				MachineID: id,
				State:     state,
				Height:    height,
				Hash:      hash,
				Time:      unixSec,
			})

			// 记录 base
			rt.LastTriggerHeight = height
			rt.LastTriggerHash = hash
			rt.LastTriggerTime = unixSec

			// 触发后：计数器清零 + 进入 waitingReverse
			rt.Counter = 0
			rt.WaitingReverse = true

			// 触发后：如果 HIT 开启，则启动倒数（101/102/103）
			if cfg.HitEnabled {
				rt.HitPending = true
				if cfg.HitOffset < 1 {
					cfg.HitOffset = 1
				}
				rt.HitCountdown = cfg.HitOffset
			} else {
				rt.HitPending = false
				rt.HitCountdown = 0
			}
		}
	}

	return out
}

func reverse(s judge.Result) judge.Result {
	if s == judge.ON {
		return judge.OFF
	}
	return judge.ON
}

// 便于前端展示：把 unix seconds 转成 time.Time
func UnixToTime(sec int64) time.Time {
	return time.Unix(sec, 0)
}
