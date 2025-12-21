package judge

import (
	"strings"
	"sync"
	"unicode"
)

// RuleType：判定规则类型（全局唯一生效）
type RuleType string

const (
	Lucky RuleType = "lucky" // 幸运
	Big   RuleType = "big"   // 大小
	Odd   RuleType = "odd"   // 单双
)

// Result：区块判定结果
type Result string

const (
	ON  Result = "ON"
	OFF Result = "OFF"
)

// Judge：ON / OFF 判定引擎
// - 任一时刻只允许一种规则生效（204）
// - 切换规则由上层触发 StopAllMachines + Reset
type Judge struct {
	mu   sync.RWMutex
	rule RuleType
}

func NewJudge(rule RuleType) *Judge {
	if rule == "" {
		rule = Lucky
	}
	return &Judge{rule: rule}
}

func (j *Judge) GetRule() RuleType {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.rule
}

// SetRule：仅负责切换规则本身
// ⚠️ 注意：
// - 清单要求切换必须“停止所有状态机并清空计数器”
// - 该行为由 Core / API Handler 负责调用 machineManager.ResetAll()
// - Judge 只做纯判定（单一职责）
func (j *Judge) SetRule(rule RuleType) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.rule = rule
}

// Decide：根据当前生效规则，对区块 hash 判定 ON / OFF
func (j *Judge) Decide(hash string) Result {
	j.mu.RLock()
	rule := j.rule
	j.mu.RUnlock()

	switch rule {
	case Big:
		return decideBig(hash)
	case Odd:
		return decideOdd(hash)
	case Lucky:
		fallthrough
	default:
		return decideLucky(hash)
	}
}

//
// ===== 具体规则实现 =====
//

// 201 幸运规则：
// hash 最后两位
// - 字母 + 数字 / 数字 + 字母 → ON
// - 字母 + 字母 / 数字 + 数字 → OFF
func decideLucky(hash string) Result {
	h := strings.TrimSpace(hash)
	if len(h) < 2 {
		return OFF
	}
	a := rune(h[len(h)-2])
	b := rune(h[len(h)-1])

	isAAlpha := unicode.IsLetter(a)
	isADigit := unicode.IsDigit(a)
	isBAlpha := unicode.IsLetter(b)
	isBDigit := unicode.IsDigit(b)

	// 字母+数字 或 数字+字母
	if (isAAlpha && isBDigit) || (isADigit && isBAlpha) {
		return ON
	}
	return OFF
}

// 202 大小规则：
// hash 最后一个数字（忽略字母）
// 0–4 → ON
// 5–9 → OFF
func decideBig(hash string) Result {
	for i := len(hash) - 1; i >= 0; i-- {
		r := rune(hash[i])
		if unicode.IsDigit(r) {
			if r >= '0' && r <= '4' {
				return ON
			}
			return OFF
		}
	}
	// 找不到数字，默认 OFF
	return OFF
}

// 203 单双规则：
// hash 最后一个数字
// 偶数 → ON
// 奇数 → OFF
func decideOdd(hash string) Result {
	for i := len(hash) - 1; i >= 0; i-- {
		r := rune(hash[i])
		if unicode.IsDigit(r) {
			if (r-'0')%2 == 0 {
				return ON
			}
			return OFF
		}
	}
	return OFF
}
