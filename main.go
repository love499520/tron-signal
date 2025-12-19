package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

/*
	Tron 实时区块监听与交易信号系统（落地版）
	- 纯标准库：无第三方依赖
	- Web 管理台：首次 setup + login
	- API Key 管理：最多 3 个，热更新
	- 规则：ON/OFF 阈值（滑块）；HIT：t+x（x 可配）+ expect
	- 区块来源：轮询 Tron Fullnode /wallet/getnowblock（可后续替换为 TronGrid WS）
	- 去重：RingBuffer(50) on (height+hash)
	- ON/OFF 判定：hash 最后两位 “字母/数字 类型异或”
	- 状态机：waitingReverse（触发后需先见反向状态才能重新计数）
	- 信号广播：/ws 服务器端 WS 广播（不缓存、不重试、不确认）
	- SSE：/sse/status 推最新块信息给页面
	- 重启：运行态强制清零（不恢复任何历史状态）
*/

const (
	listenAddr   = ":8080"
	dataDir      = "data"
	configPath   = "data/config.json"
	logDir       = "logs"
	logRetention = 3 // days

	ringSize = 50

	// 轮询间隔：为了“实时”，默认 1s
	pollInterval = 1 * time.Second

	// Tron Fullnode API（可用 TronGrid 公共网关）
	defaultNodeURL = "https://api.trongrid.io"
)

// ---------- Config / Models ----------

type Config struct {
	Web WebCred `json:"web"`

	APIKeys []string `json:"apiKeys"`

	Rules Rules `json:"rules"`

	Access AccessControl `json:"access"`
}

type WebCred struct {
	Initialized bool   `json:"initialized"`
	Username    string `json:"username"`
	SaltHex     string `json:"saltHex"`
	HashHex     string `json:"hashHex"`
}

type AccessControl struct {
	IPWhitelist []string          `json:"ipWhitelist"`
	Tokens      map[string]uint64 `json:"tokens"` // token -> usage count
}

type Rules struct {
	On  ThresholdRule `json:"on"`
	Off ThresholdRule `json:"off"`
	Hit HitRule       `json:"hit"`
}

type ThresholdRule struct {
	Enabled   bool `json:"enabled"`
	Threshold int  `json:"threshold"` // 0-20; 0 means never trigger
}

type HitRule struct {
	Enabled bool   `json:"enabled"`
	Expect  string `json:"expect"` // "ON" or "OFF"
	Offset  int    `json:"offset"` // x, >=1
}

type Status struct {
	Listening     bool   `json:"listening"`
	LastHeight    int64  `json:"lastHeight"`
	LastHash      string `json:"lastHash"`
	LastTimeISO   string `json:"lastTimeISO"`
	Reconnects    uint64 `json:"reconnects"`
	ConnectedKeys int    `json:"connectedKeys"`
}

// Signal broadcast to trading program
type Signal struct {
	Type       string `json:"type"`       // "ON"|"OFF"|"HIT"
	Height     int64  `json:"height"`     // current block height (trigger/hit block)
	BaseHeight int64  `json:"baseHeight"` // trigger base height (for HIT: trigger base)
	State      string `json:"state"`      // "ON"|"OFF" (for HIT: the state observed at t+x)
	TimeISO    string `json:"time"`       // ISO timestamp
}

// ---------- Globals (runtime state must be reset every boot) ----------

var (
	cfgMu sync.RWMutex
	cfg   Config

	// sessions: token -> username
	sessMu   sync.Mutex
	sessions = map[string]string{}

	// runtime: forced reset every start
	rtMu sync.Mutex
	rt   RuntimeState

	// ws clients (broadcast)
	wsMu      sync.Mutex
	wsClients = map[*wsConn]struct{}{}

	// sse subscribers
	sseMu   sync.Mutex
	sseSubs = map[chan Status]struct{}{}

	// logger
	logger *log.Logger
)

type RuntimeState struct {
	// counters
	OnCounter  int
	OffCounter int

	// state machine
	WaitingReverse bool
	LastTriggered  string // "ON"|"OFF" (empty at start)
	BaseHeight     int64

	// hit waiting
	HitWaiting   bool
	HitBase      int64
	HitOffset    int
	HitExpect    string // "ON"|"OFF"
	HitArmedTime time.Time

	// ring buffer (height+hash)
	Ring ringBuffer

	// last status
	LastHeight int64
	LastHash   string
	LastTime   time.Time

	// listening
	Listening bool
}

type ringBuffer struct {
	buf   [ringSize]string
	idx   int
	full  bool
	index map[string]struct{}
}

func (r *ringBuffer) reset() {
	r.idx = 0
	r.full = false
	r.index = make(map[string]struct{}, ringSize)
	for i := 0; i < ringSize; i++ {
		r.buf[i] = ""
	}
}

func (r *ringBuffer) has(key string) bool {
	_, ok := r.index[key]
	return ok
}

func (r *ringBuffer) add(key string) {
	if r.index == nil {
		r.reset()
	}
	// if slot occupied, delete old
	old := r.buf[r.idx]
	if old != "" {
		delete(r.index, old)
	}
	r.buf[r.idx] = key
	r.index[key] = struct{}{}

	r.idx++
	if r.idx >= ringSize {
		r.idx = 0
		r.full = true
	}
}

// ---------- Utilities ----------

func ensureDirs() error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	return nil
}

func rotateLogs() (*os.File, error) {
	// log file per day: logs/YYYY-MM-DD.log
	name := time.Now().Format("2006-01-02") + ".log"
	path := filepath.Join(logDir, name)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}

	// cleanup old logs
	entries, err := os.ReadDir(logDir)
	if err == nil {
		cutoff := time.Now().AddDate(0, 0, -logRetention)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			// parse date prefix
			fn := e.Name()
			if !strings.HasSuffix(fn, ".log") || len(fn) < len("2006-01-02.log") {
				continue
			}
			ds := strings.TrimSuffix(fn, ".log")
			t, parseErr := time.Parse("2006-01-02", ds)
			if parseErr != nil {
				continue
			}
			if t.Before(cutoff) {
				_ = os.Remove(filepath.Join(logDir, fn))
			}
		}
	}

	return f, nil
}

func loadConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// defaults
			c.Access.Tokens = map[string]uint64{}
			return c, nil
		}
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.Access.Tokens == nil {
		c.Access.Tokens = map[string]uint64{}
	}
	return c, nil
}

func saveConfigLocked(c Config) error {
	tmp := configPath + ".tmp"
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, configPath)
}

func randHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func mustJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

// ---------- Auth ----------

func isLoggedIn(r *http.Request) bool {
	c, err := r.Cookie("TSID")
	if err != nil || c.Value == "" {
		return false
	}
	sessMu.Lock()
	defer sessMu.Unlock()
	_, ok := sessions[c.Value]
	return ok
}

func requireLogin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfgMu.RLock()
		initialized := cfg.Web.Initialized
		cfgMu.RUnlock()
		if !initialized {
			// force setup
			if r.URL.Path != "/setup" && r.URL.Path != "/api/setup" {
				http.Redirect(w, r, "/setup", http.StatusFound)
				return
			}
			next(w, r)
			return
		}
		if !isLoggedIn(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next(w, r)
	}
}

func setupPage(w http.ResponseWriter, r *http.Request) {
	cfgMu.RLock()
	initialized := cfg.Web.Initialized
	cfgMu.RUnlock()
	if initialized {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><title>Setup</title>
<style>body{font-family:system-ui;padding:24px;max-width:480px;margin:auto}input{width:100%;padding:10px;margin:8px 0}button{padding:10px 14px}</style>
</head><body>
<h2>首次设置账号密码</h2>
<p>设置完成后才能进入系统。</p>
<form method="post" action="/api/setup">
<label>用户名</label><input name="u" required>
<label>密码</label><input name="p" type="password" required>
<button type="submit">保存</button>
</form>
</body></html>`)
}

func setupSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	u := strings.TrimSpace(r.FormValue("u"))
	p := r.FormValue("p")
	if u == "" || p == "" {
		http.Error(w, "username/password required", http.StatusBadRequest)
		return
	}

	salt, err := randHex(16)
	if err != nil {
		http.Error(w, "rand failed", http.StatusInternalServerError)
		return
	}
	hash := sha256Hex(salt + ":" + p)

	cfgMu.Lock()
	defer cfgMu.Unlock()
	if cfg.Web.Initialized {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	cfg.Web = WebCred{
		Initialized: true,
		Username:    u,
		SaltHex:     salt,
		HashHex:     hash,
	}
	if err := saveConfigLocked(cfg); err != nil {
		http.Error(w, "save config failed", http.StatusInternalServerError)
		return
	}
	logger.Println("SYSTEM_SETUP_DONE")
	http.Redirect(w, r, "/login", http.StatusFound)
}

func loginPage(w http.ResponseWriter, r *http.Request) {
	cfgMu.RLock()
	initialized := cfg.Web.Initialized
	cfgMu.RUnlock()
	if !initialized {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><title>Login</title>
<style>body{font-family:system-ui;padding:24px;max-width:480px;margin:auto}input{width:100%;padding:10px;margin:8px 0}button{padding:10px 14px}</style>
</head><body>
<h2>登录</h2>
<form method="post" action="/api/login">
<label>用户名</label><input name="u" required>
<label>密码</label><input name="p" type="password" required>
<button type="submit">登录</button>
</form>
</body></html>`)
}

func loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	u := strings.TrimSpace(r.FormValue("u"))
	p := r.FormValue("p")

	cfgMu.RLock()
	web := cfg.Web
	cfgMu.RUnlock()

	if !web.Initialized {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}

	if u != web.Username {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	hash := sha256Hex(web.SaltHex + ":" + p)
	if hash != web.HashHex {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	sid, err := randHex(24)
	if err != nil {
		http.Error(w, "rand failed", http.StatusInternalServerError)
		return
	}
	sessMu.Lock()
	sessions[sid] = u
	sessMu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     "TSID",
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	// login gate satisfied -> attempt start listener if keys available
	tryStartListener()

	http.Redirect(w, r, "/", http.StatusFound)
}

func logout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("TSID")
	if err == nil && c.Value != "" {
		sessMu.Lock()
		delete(sessions, c.Value)
		sessMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "TSID", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// ---------- Access control (optional; used for external HTTP only) ----------

func ipAllowed(remoteAddr string, whitelist []string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, w := range whitelist {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		// exact match (simple)
		if ip.String() == w {
			return true
		}
	}
	return false
}

func tokenOK(r *http.Request) (string, bool) {
	tok := strings.TrimSpace(r.Header.Get("X-Token"))
	if tok == "" {
		tok = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if tok == "" {
		return "", false
	}
	cfgMu.RLock()
	_, ok := cfg.Access.Tokens[tok]
	cfgMu.RUnlock()
	return tok, ok
}

func externalGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// internal pages should already be protected by login; this is for external APIs if you want
		cfgMu.RLock()
		whitelist := append([]string(nil), cfg.Access.IPWhitelist...)
		cfgMu.RUnlock()

		if ipAllowed(r.RemoteAddr, whitelist) {
			next(w, r)
			return
		}

		tok, ok := tokenOK(r)
		if !ok {
			http.Error(w, "token required", http.StatusUnauthorized)
			return
		}

		cfgMu.Lock()
		cfg.Access.Tokens[tok]++
		_ = saveConfigLocked(cfg)
		cfgMu.Unlock()

		next(w, r)
	}
}

// ---------- Web UI static ----------

func indexHandler(w http.ResponseWriter, r *http.Request) {
	// serve web/index.html
	http.ServeFile(w, r, filepath.Join("web", "index.html"))
}

func staticHandler() http.Handler {
	return http.StripPrefix("/", http.FileServer(http.Dir("./web")))
}

// ---------- API endpoints ----------

func apiStatus(w http.ResponseWriter, r *http.Request) {
	rtMu.Lock()
	defer rtMu.Unlock()

	st := Status{
		Listening:     rt.Listening,
		LastHeight:    rt.LastHeight,
		LastHash:      rt.LastHash,
		LastTimeISO:   isoOrEmpty(rt.LastTime),
		Reconnects:    atomic.LoadUint64(&reconnects),
		ConnectedKeys: currentKeyCount(),
	}
	mustJSON(w, 200, st)
}

func apiGetAPIKeys(w http.ResponseWriter, r *http.Request) {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	mustJSON(w, 200, map[string]any{"apiKeys": cfg.APIKeys})
}

func apiSetAPIKeys(w http.ResponseWriter, r *http.Request) {
	var req struct {
		APIKeys []string `json:"apiKeys"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	keys := make([]string, 0, 3)
	seen := map[string]struct{}{}
	for _, k := range req.APIKeys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
		if len(keys) >= 3 {
			break
		}
	}

	cfgMu.Lock()
	cfg.APIKeys = keys
	if err := saveConfigLocked(cfg); err != nil {
		cfgMu.Unlock()
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	cfgMu.Unlock()

	logger.Printf("APIKEYS_UPDATED count=%d", len(keys))

	// hot-update listener start/stop
	tryStartListener()
	if len(keys) == 0 {
		stopListener()
	}

	mustJSON(w, 200, map[string]any{"ok": true, "apiKeys": keys})
}

func apiGetRules(w http.ResponseWriter, r *http.Request) {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	mustJSON(w, 200, cfg.Rules)
}

func apiSetRules(w http.ResponseWriter, r *http.Request) {
	var rr Rules
	if err := readJSON(r, &rr); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	// sanitize
	rr.On.Threshold = clamp(rr.On.Threshold, 0, 20)
	rr.Off.Threshold = clamp(rr.Off.Threshold, 0, 20)
	rr.Hit.Offset = clamp(rr.Hit.Offset, 1, 20)
	rr.Hit.Expect = strings.ToUpper(strings.TrimSpace(rr.Hit.Expect))
	if rr.Hit.Expect != "ON" && rr.Hit.Expect != "OFF" {
		rr.Hit.Expect = "ON"
	}

	cfgMu.Lock()
	cfg.Rules = rr
	if err := saveConfigLocked(cfg); err != nil {
		cfgMu.Unlock()
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	cfgMu.Unlock()

	logger.Printf("RULES_UPDATED on=(%v,%d) off=(%v,%d) hit=(%v,expect=%s,offset=%d)",
		rr.On.Enabled, rr.On.Threshold, rr.Off.Enabled, rr.Off.Threshold, rr.Hit.Enabled, rr.Hit.Expect, rr.Hit.Offset)

	mustJSON(w, 200, map[string]any{"ok": true, "rules": rr})
}

// ---------- SSE status ----------

func sseStatus(w http.ResponseWriter, r *http.Request) {
	if !isLoggedIn(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}

	ch := make(chan Status, 8)
	sseMu.Lock()
	sseSubs[ch] = struct{}{}
	sseMu.Unlock()

	defer func() {
		sseMu.Lock()
		delete(sseSubs, ch)
		sseMu.Unlock()
		close(ch)
	}()

	// initial push
	rtMu.Lock()
	st := Status{
		Listening:     rt.Listening,
		LastHeight:    rt.LastHeight,
		LastHash:      rt.LastHash,
		LastTimeISO:   isoOrEmpty(rt.LastTime),
		Reconnects:    atomic.LoadUint64(&reconnects),
		ConnectedKeys: currentKeyCount(),
	}
	rtMu.Unlock()
	writeSSE(w, st)
	flusher.Flush()

	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			return
		case s := <-ch:
			writeSSE(w, s)
			flusher.Flush()
		}
	}
}

func writeSSE(w io.Writer, st Status) {
	b, _ := json.Marshal(st)
	fmt.Fprintf(w, "event: status\n")
	fmt.Fprintf(w, "data: %s\n\n", string(b))
}

func broadcastStatus() {
	rtMu.Lock()
	st := Status{
		Listening:     rt.Listening,
		LastHeight:    rt.LastHeight,
		LastHash:      rt.LastHash,
		LastTimeISO:   isoOrEmpty(rt.LastTime),
		Reconnects:    atomic.LoadUint64(&reconnects),
		ConnectedKeys: currentKeyCount(),
	}
	rtMu.Unlock()

	sseMu.Lock()
	defer sseMu.Unlock()
	for ch := range sseSubs {
		select {
		case ch <- st:
		default:
			// drop if slow
		}
	}
}

// ---------- Block polling (listener) ----------

var (
	listenerOnce  sync.Once
	listenerStopC = make(chan struct{})
	reconnects    uint64
)

func currentKeyCount() int {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return len(cfg.APIKeys)
}

func tryStartListener() {
	// start only if initialized+loggedIn gate satisfied (at least one active session) and keys>=1
	cfgMu.RLock()
	keysOK := len(cfg.APIKeys) >= 1
	cfgMu.RUnlock()
	if !keysOK {
		return
	}
	// login gate: if any active session exists
	sessMu.Lock()
	hasSession := len(sessions) > 0
	sessMu.Unlock()
	if !hasSession {
		return
	}

	listenerOnce.Do(func() {
		go listenerLoop()
	})
	// mark listening true (idempotent)
	rtMu.Lock()
	rt.Listening = true
	rtMu.Unlock()
	broadcastStatus()
}

func stopListener() {
	// we allow stop only by closing stop chan once.
	// In this minimal version, we just set Listening=false; loop will observe keys==0 and idle.
	rtMu.Lock()
	rt.Listening = false
	rtMu.Unlock()
	broadcastStatus()
}

type tronNowBlockResp struct {
	BlockID   string `json:"blockID"`
	BlockHeader struct {
		RawData struct {
			Number    int64 `json:"number"`
			Timestamp int64 `json:"timestamp"`
		} `json:"raw_data"`
	} `json:"block_header"`
}

func listenerLoop() {
	logger.Println("LISTENER_LOOP_START")

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	client := &http.Client{Timeout: 8 * time.Second}

	for {
		select {
		case <-listenerStopC:
			logger.Println("LISTENER_LOOP_STOP")
			return
		case <-ticker.C:
			cfgMu.RLock()
			keys := append([]string(nil), cfg.APIKeys...)
			rules := cfg.Rules
			cfgMu.RUnlock()

			// if keys empty or no active session => not allowed to listen (gate)
			sessMu.Lock()
			hasSession := len(sessions) > 0
			sessMu.Unlock()
			if len(keys) == 0 || !hasSession {
				rtMu.Lock()
				rt.Listening = false
				rtMu.Unlock()
				continue
			}
			rtMu.Lock()
			rt.Listening = true
			rtMu.Unlock()

			// pick a key (round-robin by time)
			key := keys[int(time.Now().UnixNano()%int64(len(keys)))]
			height, hash, tISO, err := fetchNowBlock(client, defaultNodeURL, key)
			if err != nil {
				atomic.AddUint64(&reconnects, 1)
				logger.Printf("BLOCK_FETCH_ERROR: %v", err)
				continue
			}

			// update status first (but still need dedupe)
			rtMu.Lock()
			rt.LastHeight = height
			rt.LastHash = hash
			rt.LastTime = parseISOOrNow(tISO)
			rtMu.Unlock()
			broadcastStatus()

			processBlock(height, hash, parseISOOrNow(tISO), rules)
		}
	}
}

func fetchNowBlock(client *http.Client, nodeURL, apiKey string) (height int64, hash string, timeISO string, err error) {
	url := strings.TrimRight(nodeURL, "/") + "/wallet/getnowblock"
	req, _ := http.NewRequest("POST", url, bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		// TronGrid common header
		req.Header.Set("TRON-PRO-API-KEY", apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return 0, "", "", fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var out tronNowBlockResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, "", "", err
	}
	height = out.BlockHeader.RawData.Number
	hash = out.BlockID
	ts := out.BlockHeader.RawData.Timestamp
	// Tron returns ms timestamp
	if ts > 0 {
		timeISO = time.UnixMilli(ts).UTC().Format(time.RFC3339Nano)
	} else {
		timeISO = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return
}

// ---------- ON/OFF 判定（你已确认的映射表） ----------

func blockStateByHash(hash string) (string, bool) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) < 2 {
		return "", false
	}
	a := hash[len(hash)-2]
	b := hash[len(hash)-1]

	t1, ok1 := hexCharType(a)
	t2, ok2 := hexCharType(b)
	if !ok1 || !ok2 {
		return "", false
	}

	// 字母+数字 或 数字+字母 => ON；同类 => OFF
	if t1 != t2 {
		return "ON", true
	}
	return "OFF", true
}

func hexCharType(c byte) (string, bool) {
	switch {
	case c >= '0' && c <= '9':
		return "digit", true
	case c >= 'a' && c <= 'f':
		return "alpha", true
	default:
		return "", false
	}
}

// ---------- Core processing pipeline ----------

func processBlock(height int64, hash string, t time.Time, rules Rules) {
	// Step 2: dedupe (height+hash)
	key := fmt.Sprintf("%d:%s", height, hash)

	rtMu.Lock()
	if rt.Ring.index == nil {
		rt.Ring.reset()
	}
	if rt.Ring.has(key) {
		rtMu.Unlock()
		return
	}
	rt.Ring.add(key)
	rtMu.Unlock()

	// Step 3: judge ON/OFF
	state, ok := blockStateByHash(hash)
	if !ok {
		logger.Printf("DROP_BLOCK_INVALID_HASH height=%d hash=%q", height, hash)
		return
	}

	// Step 4 + 5: state machine + optional hit
	signals := evaluateStateMachine(height, state, t, rules)
	for _, s := range signals {
		broadcastSignal(s)
	}
}

func evaluateStateMachine(height int64, state string, t time.Time, rules Rules) []Signal {
	rtMu.Lock()
	defer rtMu.Unlock()

	// 初始状态：waitingReverse=true
	// 为了让“解除等待”有明确反向：若从未触发过，则默认 LastTriggered="ON"（要求先看到 OFF 才开始计数）
	if rt.LastTriggered == "" {
		rt.LastTriggered = "ON"
		rt.WaitingReverse = true
	}

	// Step 5: if hit waiting and reach t+x -> check once
	var out []Signal
	if rt.HitWaiting && height == rt.HitBase+int64(rt.HitOffset) {
		if state == rt.HitExpect {
			out = append(out, Signal{
				Type:       "HIT",
				Height:     height,
				BaseHeight: rt.HitBase,
				State:      state,
				TimeISO:    t.UTC().Format(time.RFC3339Nano),
			})
			logger.Printf("HIT_SIGNAL height=%d base=%d state=%s", height, rt.HitBase, state)
		} else {
			logger.Printf("HIT_MISS height=%d base=%d got=%s expect=%s", height, rt.HitBase, state, rt.HitExpect)
		}
		// end hit regardless
		rt.HitWaiting = false
	}

	// waitingReverse gate
	if rt.WaitingReverse {
		reverse := reverseOf(rt.LastTriggered)
		if state == reverse {
			rt.WaitingReverse = false
			// reset counters when unlock (clean start)
			rt.OnCounter = 0
			rt.OffCounter = 0
		} else {
			// still waiting, stop here
			return out
		}
	}

	// count stage
	switch state {
	case "ON":
		// reset opposite
		rt.OffCounter = 0
		if rules.On.Enabled {
			if state == "ON" {
				rt.OnCounter++
			} else {
				rt.OnCounter = 0
			}
		} else {
			rt.OnCounter = 0
		}

		if rules.On.Enabled && rules.On.Threshold > 0 && rt.OnCounter >= rules.On.Threshold {
			// trigger ON
			rt.OnCounter = 0
			rt.OffCounter = 0
			rt.WaitingReverse = true
			rt.LastTriggered = "ON"
			rt.BaseHeight = height

			s := Signal{
				Type:       "ON",
				Height:     height,
				BaseHeight: height,
				State:      "ON",
				TimeISO:    t.UTC().Format(time.RFC3339Nano),
			}
			out = append(out, s)
			logger.Printf("ON_SIGNAL height=%d", height)

			// arm hit
			armHitLocked(height, rules)
		}

	case "OFF":
		rt.OnCounter = 0
		if rules.Off.Enabled {
			if state == "OFF" {
				rt.OffCounter++
			} else {
				rt.OffCounter = 0
			}
		} else {
			rt.OffCounter = 0
		}

		if rules.Off.Enabled && rules.Off.Threshold > 0 && rt.OffCounter >= rules.Off.Threshold {
			// trigger OFF
			rt.OnCounter = 0
			rt.OffCounter = 0
			rt.WaitingReverse = true
			rt.LastTriggered = "OFF"
			rt.BaseHeight = height

			s := Signal{
				Type:       "OFF",
				Height:     height,
				BaseHeight: height,
				State:      "OFF",
				TimeISO:    t.UTC().Format(time.RFC3339Nano),
			}
			out = append(out, s)
			logger.Printf("OFF_SIGNAL height=%d", height)

			armHitLocked(height, rules)
		}
	}

	return out
}

func armHitLocked(triggerHeight int64, rules Rules) {
	// only when just triggered and hit enabled
	if !rules.Hit.Enabled {
		return
	}
	expect := strings.ToUpper(strings.TrimSpace(rules.Hit.Expect))
	if expect != "ON" && expect != "OFF" {
		expect = "ON"
	}
	offset := rules.Hit.Offset
	if offset < 1 {
		offset = 1
	}

	rt.HitWaiting = true
	rt.HitBase = triggerHeight
	rt.HitOffset = offset
	rt.HitExpect = expect
	rt.HitArmedTime = time.Now()
	logger.Printf("HIT_ARMED base=%d offset=%d expect=%s", triggerHeight, offset, expect)
}

func reverseOf(s string) string {
	if s == "ON" {
		return "OFF"
	}
	return "ON"
}

func clamp(v, lo, hi int) int {
	return int(math.Max(float64(lo), math.Min(float64(hi), float64(v))))
}

func isoOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseISOOrNow(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Now().UTC()
	}
	return t
}

// ---------- Minimal WebSocket server (standard library only) ----------

type wsConn struct {
	c   net.Conn
	mu  sync.Mutex
	dead atomic.Bool
}

func (w *wsConn) Close() {
	if w.dead.CompareAndSwap(false, true) {
		_ = w.c.Close()
	}
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	// Note: WS broadcast is not protected by token/ip in this minimal version.
	// If you need, add guards here.
	if !isLoggedIn(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		http.Error(w, "hijack failed", http.StatusInternalServerError)
		return
	}

	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		_ = conn.Close()
		return
	}
	accept := wsAcceptKey(key)

	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"

	if _, err := buf.WriteString(resp); err != nil {
		_ = conn.Close()
		return
	}
	if err := buf.Flush(); err != nil {
		_ = conn.Close()
		return
	}

	c := &wsConn{c: conn}
	wsMu.Lock()
	wsClients[c] = struct{}{}
	wsMu.Unlock()

	logger.Printf("WS_CLIENT_CONNECTED remote=%s", r.RemoteAddr)

	// read loop to keep connection healthy (discard frames)
	go func() {
		defer func() {
			wsMu.Lock()
			delete(wsClients, c)
			wsMu.Unlock()
			c.Close()
			logger.Printf("WS_CLIENT_DISCONNECTED remote=%s", r.RemoteAddr)
		}()
		_ = wsReadLoop(conn)
	}()
}

func wsAcceptKey(clientKey string) string {
	const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	h := sha1.Sum([]byte(clientKey + magic))
	return base64.StdEncoding.EncodeToString(h[:])
}

func wsReadLoop(conn net.Conn) error {
	br := bufio.NewReader(conn)
	for {
		// minimal frame parser (masked client-to-server)
		b1, err := br.ReadByte()
		if err != nil {
			return err
		}
		b2, err := br.ReadByte()
		if err != nil {
			return err
		}
		op := b1 & 0x0f
		mask := (b2 & 0x80) != 0
		payloadLen := int(b2 & 0x7f)

		if payloadLen == 126 {
			x1, _ := br.ReadByte()
			x2, _ := br.ReadByte()
			payloadLen = int(uint16(x1)<<8 | uint16(x2))
		} else if payloadLen == 127 {
			// we don't expect huge frames; read 8 bytes
			var n uint64
			for i := 0; i < 8; i++ {
				b, e := br.ReadByte()
				if e != nil {
					return e
				}
				n = (n << 8) | uint64(b)
			}
			if n > 1<<20 {
				return errors.New("ws frame too large")
			}
			payloadLen = int(n)
		}

		var maskKey [4]byte
		if mask {
			if _, err := io.ReadFull(br, maskKey[:]); err != nil {
				return err
			}
		}

		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(br, payload); err != nil {
			return err
		}
		if mask {
			for i := range payload {
				payload[i] ^= maskKey[i%4]
			}
		}

		// handle close
		if op == 0x8 {
			return io.EOF
		}
		// ping/pong ignored (browser handles)
	}
}

func wsWriteText(conn net.Conn, msg []byte) error {
	// server-to-client frames are NOT masked
	// FIN=1, opcode=1 (text)
	var hdr bytes.Buffer
	hdr.WriteByte(0x81)

	n := len(msg)
	switch {
	case n <= 125:
		hdr.WriteByte(byte(n))
	case n <= 65535:
		hdr.WriteByte(126)
		hdr.WriteByte(byte((n >> 8) & 0xff))
		hdr.WriteByte(byte(n & 0xff))
	default:
		hdr.WriteByte(127)
		// 8 bytes
		for i := 7; i >= 0; i-- {
			hdr.WriteByte(byte(uint64(n) >> (uint(i) * 8)))
		}
	}

	if _, err := conn.Write(hdr.Bytes()); err != nil {
		return err
	}
	_, err := conn.Write(msg)
	return err
}

func broadcastSignal(s Signal) {
	b, _ := json.Marshal(s)

	wsMu.Lock()
	defer wsMu.Unlock()
	for c := range wsClients {
		if c.dead.Load() {
			continue
		}
		c.mu.Lock()
		err := wsWriteText(c.c, b)
		c.mu.Unlock()
		if err != nil {
			c.Close()
		}
	}
}

// ---------- main ----------

func resetRuntime() {
	rtMu.Lock()
	defer rtMu.Unlock()

	rt.OnCounter = 0
	rt.OffCounter = 0
	rt.WaitingReverse = true
	rt.HitWaiting = false
	rt.BaseHeight = 0

	rt.LastTriggered = ""
	rt.Ring.reset()

	rt.LastHeight = 0
	rt.LastHash = ""
	rt.LastTime = time.Time{}
	rt.Listening = false
}

func main() {
	if err := ensureDirs(); err != nil {
		panic(err)
	}

	lf, err := rotateLogs()
	if err != nil {
		panic(err)
	}
	defer lf.Close()
	logger = log.New(io.MultiWriter(os.Stdout, lf), "", log.LstdFlags|log.Lmicroseconds)

	// abnormal restart marker
	lockPath := filepath.Join(dataDir, "running.lock")
	if _, err := os.Stat(lockPath); err == nil {
		logger.Println("ABNORMAL_RESTART")
	}
	_ = os.WriteFile(lockPath, []byte(time.Now().Format(time.RFC3339Nano)), 0o644)
	defer os.Remove(lockPath)

	logger.Println("SYSTEM_START")

	loaded, err := loadConfig()
	if err != nil {
		logger.Printf("CONFIG_LOAD_ERROR: %v", err)
		// keep default cfg
	}
	cfgMu.Lock()
	cfg = loaded
	// defaults
	if cfg.Access.Tokens == nil {
		cfg.Access.Tokens = map[string]uint64{}
	}
	// default rules if zero
	if cfg.Rules.Hit.Offset == 0 {
		cfg.Rules.Hit.Offset = 1
	}
	cfgMu.Unlock()

	// runtime must be fully reset every boot
	resetRuntime()

	mux := http.NewServeMux()

	// auth pages
	mux.HandleFunc("/setup", setupPage)
	mux.HandleFunc("/api/setup", setupSubmit)
	mux.HandleFunc("/login", loginPage)
	mux.HandleFunc("/api/login", loginSubmit)
	mux.HandleFunc("/logout", logout)

	// app
	mux.HandleFunc("/", requireLogin(indexHandler))

	// APIs (require login)
	mux.HandleFunc("/api/status", requireLogin(apiStatus))
	mux.HandleFunc("/api/apikey", requireLogin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			apiGetAPIKeys(w, r)
		case "POST":
			apiSetAPIKeys(w, r)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/rules", requireLogin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			apiGetRules(w, r)
		case "POST":
			apiSetRules(w, r)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))

	// SSE + WS (require login)
	mux.HandleFunc("/sse/status", requireLogin(sseStatus))
	mux.HandleFunc("/ws", requireLogin(wsHandler))

	// static assets (only after login gate)
	mux.Handle("/app.js", requireLogin(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join("web", "app.js"))
	}))
	mux.Handle("/style.css", requireLogin(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join("web", "style.css"))
	}))

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           withSecurityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Printf("HTTP_LISTEN %s", listenAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Printf("SERVER_ERROR: %v", err)
	}
}

// ---------- headers ----------

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// basic headers
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'")
		next.ServeHTTP(w, r)
	})
}

// ---------- unused but ready helpers ----------

// If later you want to guard some external endpoints with IP/token, wrap with externalGuard(handler)
var _ = externalGuard

// Example graceful shutdown if you want:
var _ = context.Background
