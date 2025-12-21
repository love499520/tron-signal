package auth

import (
	"crypto/rand"
	"encoding/hex"
)

// NewToken
// 生成一个随机 Token（32 bytes -> 64 hex chars）
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// HasToken
// 判断请求 token 是否在已配置 token 列表中
func HasToken(token string, tokens []string) bool {
	for _, t := range tokens {
		if t == token {
			return true
		}
	}
	return false
}
