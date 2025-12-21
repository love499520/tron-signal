package auth

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const cookieName = "tron_signal_session"

type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time
	ttl      time.Duration
}

func NewMemoryStore(ttl time.Duration) *MemoryStore {
	return &MemoryStore{
		sessions: map[string]time.Time{},
		ttl:      ttl,
	}
}

func (s *MemoryStore) IsLoggedIn(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || c == nil || c.Value == "" {
		return false
	}
	token := c.Value

	s.mu.Lock()
	defer s.mu.Unlock()

	exp, ok := s.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, token)
		return false
	}
	return true
}

func (s *MemoryStore) Login(w http.ResponseWriter) {
	token := randHex(32)

	s.mu.Lock()
	s.sessions[token] = time.Now().Add(s.ttl)
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *MemoryStore) Logout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(cookieName)
	if err == nil && c != nil && c.Value != "" {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
