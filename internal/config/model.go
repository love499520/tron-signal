package config

// Config
// 所有配置均可持久化，启动加载，修改即落盘
type Config struct {
	// 判定规则：lucky / bigsmall / oddseven
	JudgeRule string `json:"judge_rule"`

	// 数据源配置
	Sources []SourceConfig `json:"sources"`

	// 状态机配置
	Machines []MachineConfig `json:"machines"`

	// Token 列表
	Tokens []TokenConfig `json:"tokens"`

	// IP 白名单
	IPWhitelist []string `json:"ip_whitelist"`
}

// SourceConfig
// HTTP 数据源配置（Ankr RPC / Ankr REST / TronGrid 等）
type SourceConfig struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`

	// 请求地址
	Endpoint string `json:"endpoint"`

	// 可选 Header（如 API Key）
	Headers map[string]string `json:"headers,omitempty"`

	// 轮询频率配置
	BaseRate int `json:"base_rate"` // 基础阈值（每秒）
	MaxRate  int `json:"max_rate"`  // 上限阈值（每秒）
}

// MachineConfig
// 单个状态机配置
type MachineConfig struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`

	// 触发规则
	TriggerState string `json:"trigger_state"` // ON / OFF
	Threshold    int    `json:"threshold"`

	// HIT 规则
	HitEnabled bool   `json:"hit_enabled"`
	HitExpect  string `json:"hit_expect"` // ON / OFF
	HitOffset  int    `json:"hit_offset"` // T+X
}

// TokenConfig
// 外网访问 Token
type TokenConfig struct {
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
}
