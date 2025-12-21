package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// MemoryStore：极简 session store（管理登录态）
// - Cookie: sid
// - TTL 过期清理（懒清理 + 后台清理）
type MemoryStore struct {
	mu sync.RWMutex
	m  map[string]time.Time
}

func NewMemoryStore() *MemoryStore {
	s := &MemoryStore{m: map[string]time.Time{}}
	go s.gcLoop()
	return s
}

func (s *MemoryStore) NewSession() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *MemoryStore) Set(sessionID string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[sessionID] = time.Now().Add(ttl)
}

func (s *MemoryStore) Has(sessionID string) bool {
	s.mu.RLock()
	exp, ok := s.m[sessionID]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		s.mu.Lock()
		delete(s.m, sessionID)
		s.mu.Unlock()
		return false
	}
	return true
}

func (s *MemoryStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, sessionID)
}

func (s *MemoryStore) gcLoop() {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		s.mu.Lock()
		for k, exp := range s.m {
			if now.After(exp) {
				delete(s.m, k)
			}
		}
		s.mu.Unlock()
	}
}
