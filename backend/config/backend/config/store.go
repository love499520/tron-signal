package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Config struct {
	AdminUser     string   `json:"adminUser"`
	AdminPassHash string   `json:"adminPassHash"`

	Tokens      []string `json:"tokens"`
	IPWhitelist []string `json:"ipWhitelist"`

	JudgeRule string `json:"judgeRule"` // lucky | bigsmall | oddeven

	// Data sources configured in UI
	Sources []Source `json:"sources"`

	// Machines list (UI configured)
	Machines []any `json:"machines"`
}

type Source struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"` // ankr-rest | ankr-rpc | trongrid
	Enabled     bool   `json:"enabled"`
	BaseRate    int    `json:"baseRate"`  // requests per second
	MaxRate     int    `json:"maxRate"`   // requests per second
	Endpoint    string `json:"endpoint"`  // full url
	APIKey      string `json:"apiKey"`    // optional
	TimeoutMS   int    `json:"timeoutMs"` // per request
	LastError   string `json:"-"`
	LastOKEpoch int64  `json:"-"`
}

type Store struct {
	mu   sync.Mutex
	path string
	cfg  Config
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.Load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)

	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.cfg = defaultConfig()
			return s.saveLocked()
		}
		return err
	}

	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		// 配置损坏：仍然启动，但用默认配置覆盖
		s.cfg = defaultConfig()
		return s.saveLocked()
	}

	if c.JudgeRule == "" {
		c.JudgeRule = "lucky"
	}
	s.cfg = c
	return nil
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o600)
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

func (s *Store) Set(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = c
	return s.saveLocked()
}

// ===== admin =====

func (s *Store) VerifyAdmin(user, pass string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.AdminUser == "" || s.cfg.AdminPassHash == "" {
		return false
	}
	if strings.TrimSpace(user) != s.cfg.AdminUser {
		return false
	}
	return hashPass(pass) == s.cfg.AdminPassHash
}

func (s *Store) SetAdmin(user, pass string) error {
	user = strings.TrimSpace(user)
	if user == "" || pass == "" {
		return errors.New("INVALID_ADMIN")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.AdminUser = user
	s.cfg.AdminPassHash = hashPass(pass)
	return s.saveLocked()
}

func hashPass(pass string) string {
	h := sha256.Sum256([]byte(pass))
	return hex.EncodeToString(h[:])
}

// ===== tokens / ip whitelist =====

func (s *Store) HasToken(t string) bool {
	t = strings.TrimSpace(t)
	if t == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.cfg.Tokens {
		if v == t {
			return true
		}
	}
	return false
}

func (s *Store) AddToken(t string) error {
	t = strings.TrimSpace(t)
	if t == "" {
		return errors.New("INVALID_TOKEN")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.cfg.Tokens {
		if v == t {
			return nil
		}
	}
	s.cfg.Tokens = append(s.cfg.Tokens, t)
	return s.saveLocked()
}

func (s *Store) DeleteToken(t string) error {
	t = strings.TrimSpace(t)
	if t == "" {
		return errors.New("INVALID_TOKEN")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.cfg.Tokens))
	for _, v := range s.cfg.Tokens {
		if v != t {
			out = append(out, v)
		}
	}
	s.cfg.Tokens = out
	return s.saveLocked()
}

func (s *Store) IsWhitelistedIP(ip string) bool {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.cfg.IPWhitelist {
		if strings.TrimSpace(v) == ip {
			return true
		}
	}
	return false
}

func (s *Store) SetIPWhitelist(list []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.IPWhitelist = list
	return s.saveLocked()
}

func defaultConfig() Config {
	return Config{
		JudgeRule:   "lucky",
		Tokens:     []string{},
		IPWhitelist: []string{},
		Sources:    []Source{},
		Machines:   []any{},
	}
}
