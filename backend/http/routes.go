package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tron-signal/backend/app"
	"tron-signal/backend/block"
	"tron-signal/backend/judge"
	"tron-signal/backend/machine"
	"tron-signal/backend/source"
	"tron-signal/backend/ws"
)

// ===== deps =====

type ConfigReader interface {
	GetWhitelist() []string
	HasToken(token string) bool
	CheckAdmin(username, password string) bool
	SetAdmin(username, password string)
	AddToken(token string)
	DeleteToken(token string)
	SetWhitelist(list []string)
	SetJudgeRule(rule judge.RuleType)
	UpsertSource(sc source.Config)
	DeleteSource(id string)
	SetSourceExtra(id string, ex any) // 这里为了避免循环依赖，先用 any；后续 SourceExtra 会在 config 模块里强类型处理
	SetMachines(ms []machine.Config)
	SetPoll(baseTickMS int, auto bool, waitMinutes int)
	Save() error
}

// AuthStore：简单 session store（下一步我会给 auth/store.go）
// 这里先用接口，routes.go 可以先编译通过
type AuthStore interface {
	NewSession() string
	Set(sessionID string, ttl time.Duration)
	Has(sessionID string) bool
	Delete(sessionID string)
}

type RouterDeps struct {
	Core      *app.Core
	Ring      *block.RingBuffer
	Mgr       *machine.Manager
	Dispatcher *source.Dispatcher
	Hub       *ws.Hub

	AuthStore AuthStore
	Cfg       ConfigReader

	WebDir  string
	DocsDir string
	LogDir  string
}

// ===== Router =====

func NewRouter(d RouterDeps) http.Handler {
	mux := http.NewServeMux()

	// 静态资源（UI）
	// UI 自己会走 /api/admin/me 判断是否已登录（移动端优化在前端做）
	mux.Handle("/", http.FileServer(http.Dir(d.WebDir)))

	// WS：仅广播信号（门禁：白名单/Token）
	mux.Handle("/ws", requireTokenOrWhitelist(d.Cfg, http.HandlerFunc(d.Hub.Handle)))

	// SSE：UI 状态更新（门禁：白名单/Token；是否要求 admin 登录由前端控制）
	mux.Handle("/sse/status", requireTokenOrWhitelist(d.Cfg, http.HandlerFunc(
		HandleSSE(d.Core, d.Ring, d.Mgr, d.Hub.Count),
	)))

	// ===== API（门禁：白名单/Token）=====
	api := http.NewServeMux()

	// status（1108 + 500/404 runtime + blocks）
	api.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, 405, map[string]any{"ok": false})
			return
		}
		writeJSON(w, 200, map[string]any{
			"ok":     true,
			"status": d.Core.GetStatus(),
			"blocks": d.Ring.List(),
			"runtime": d.Mgr.RuntimeSnapshot(),
			"wsClients": d.Hub.Count(),
			"judgeRule": d.CoreRuleName(), // helper below
		})
	})

	// 判定规则切换（二次确认 + 必须停机清计数器/运行态/去重）
	// POST /api/judge/switch { "rule":"lucky|big|odd", "confirm": true/false }
	api.HandleFunc("/api/judge/switch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]any{"ok": false})
			return
		}
		var req struct {
			Rule    string `json:"rule"`
			Confirm bool   `json:"confirm"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		rule := judge.RuleType(req.Rule)
		if rule != judge.Lucky && rule != judge.Big && rule != judge.Odd {
			writeJSON(w, 400, map[string]any{"ok": false, "msg": "invalid rule"})
			return
		}

		if !req.Confirm {
			// 206/207：二次确认（第二次勾选确认）
			writeJSON(w, 200, map[string]any{
				"ok":          false,
				"needConfirm": true,
				"msg":         "切换判定规则会停止所有状态机并清空计数器/运行态，需勾选确认后再次提交",
			})
			return
		}

		// 205：必须自动停止 + 清空
		d.Core.SwitchJudgeRule(rule)
		d.Cfg.SetJudgeRule(rule)

		writeJSON(w, 200, map[string]any{"ok": true})
	})

	// ===== Machines：前端用“整表保存”最简单 =====
	// GET /api/machines
	api.HandleFunc("/api/machines", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 200, map[string]any{
				"ok":       true,
				"machines": d.Mgr.List(),
				"runtime":  d.Mgr.RuntimeSnapshot(),
			})
		case http.MethodPut:
			var req struct {
				Machines []machine.Config `json:"machines"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			// 直接重建 manager 配置（运行态清零由前端控制是否触发 reset，这里不强制）
			d.Cfg.SetMachines(req.Machines)
			// 热更新：依次 upsert
			for _, mc := range req.Machines {
				d.Mgr.Upsert(mc)
			}
			writeJSON(w, 200, map[string]any{"ok": true})
		default:
			writeJSON(w, 405, map[string]any{"ok": false})
		}
	})

	// ===== Token / 白名单（门禁模型：只区分内网白名单/外网 token）=====
	api.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// 安全：只给管理登录态看（符合你 /docs 要求；token列表也属于管理范畴）
			if !isAdminAuthed(d.AuthStore, r) {
				writeJSON(w, 401, map[string]any{"ok": false})
				return
			}
			// 这里不从 cfg 直接取 tokens（接口里没暴露），后续 config.go 会补 GetTokens()
			writeJSON(w, 200, map[string]any{"ok": true, "msg": "tokens list: todo in config.GetTokens()"})
		case http.MethodPost:
			if !isAdminAuthed(d.AuthStore, r) {
				writeJSON(w, 401, map[string]any{"ok": false})
				return
			}
			t := newToken()
			d.Cfg.AddToken(t)
			writeJSON(w, 200, map[string]any{"ok": true, "token": t})
		case http.MethodDelete:
			if !isAdminAuthed(d.AuthStore, r) {
				writeJSON(w, 401, map[string]any{"ok": false})
				return
			}
			// /api/tokens?token=xxx
			token := r.URL.Query().Get("token")
			if token == "" {
				writeJSON(w, 400, map[string]any{"ok": false})
				return
			}
			d.Cfg.DeleteToken(token)
			writeJSON(w, 200, map[string]any{"ok": true})
		default:
			writeJSON(w, 405, map[string]any{"ok": false})
		}
	})

	api.HandleFunc("/api/whitelist", func(w http.ResponseWriter, r *http.Request) {
		if !isAdminAuthed(d.AuthStore, r) {
			writeJSON(w, 401, map[string]any{"ok": false})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 200, map[string]any{"ok": true, "whitelist": d.Cfg.GetWhitelist()})
		case http.MethodPut:
			var req struct{ Whitelist []string `json:"whitelist"` }
			_ = json.NewDecoder(r.Body).Decode(&req)
			d.Cfg.SetWhitelist(req.Whitelist)
			writeJSON(w, 200, map[string]any{"ok": true})
		default:
			writeJSON(w, 405, map[string]any{"ok": false})
		}
	})

	// ===== Admin Login（管理登录态，仅用于管理/查看 docs/logs/token）=====
	api.HandleFunc("/api/admin/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"ok":     true,
			"authed": isAdminAuthed(d.AuthStore, r),
		})
	})

	api.HandleFunc("/api/admin/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]any{"ok": false})
			return
		}
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !d.Cfg.CheckAdmin(req.Username, req.Password) {
			writeJSON(w, 401, map[string]any{"ok": false})
			return
		}
		sid := d.AuthStore.NewSession()
		d.AuthStore.Set(sid, 24*time.Hour)
		http.SetCookie(w, &http.Cookie{
			Name:     "sid",
			Value:    sid,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	// 首次设置密码（1105 门禁：首次必须进 UI 配置）
	api.HandleFunc("/api/admin/set_password", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]any{"ok": false})
			return
		}
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Username == "" || req.Password == "" {
			writeJSON(w, 400, map[string]any{"ok": false})
			return
		}
		d.Cfg.SetAdmin(req.Username, req.Password)
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	// ===== Docs：/docs 仅管理登录态访问（808）=====
	mux.Handle("/docs", requireAdmin(d.AuthStore, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 简单优雅：直接输出 api.md
		p := filepath.Join(d.DocsDir, "api.md")
		b, err := os.ReadFile(p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write(b)
	})))

	// ===== Logs：管理登录态访问（1001/1002/1006）=====
	mux.Handle("/api/logs", requireAdmin(d.AuthStore, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ?q=xxx&major=1
		q := r.URL.Query().Get("q")
		major := r.URL.Query().Get("major") == "1"

		// 读取当天最新 log（先实现最小可用；轮转模块后续增强）
		latest := latestLogFile(d.LogDir)
		if latest == "" {
			writeJSON(w, 200, map[string]any{"ok": true, "lines": []string{}})
			return
		}
		b, _ := os.ReadFile(latest)
		lines := filterLines(string(b), q, major)
		writeJSON(w, 200, map[string]any{"ok": true, "file": filepath.Base(latest), "lines": lines})
	})))

	// API 总门禁（白名单/Token）
	mux.Handle("/api/", requireTokenOrWhitelist(d.Cfg, api))

	return mux
}

// ===== helpers =====

func (d RouterDeps) CoreRuleName() string {
	// Core 内没暴露 rule，这里走 config 的 judge_rule（你最终以仓库代码为准）
	// 简化：前端也可以只展示 config 内的 rule
	return "use-config-judge_rule"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func getClientIP(r *http.Request) string {
	// 简化：优先 X-Forwarded-For
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func inWhitelist(cfg ConfigReader, ip string) bool {
	for _, w := range cfg.GetWhitelist() {
		if w == ip {
			return true
		}
		// CIDR
		if strings.Contains(w, "/") {
			if _, n, err := net.ParseCIDR(w); err == nil {
				if n.Contains(net.ParseIP(ip)) {
					return true
				}
			}
		}
	}
	return false
}

func extractToken(r *http.Request) string {
	// 1) header X-Token
	if t := strings.TrimSpace(r.Header.Get("X-Token")); t != "" {
		return t
	}
	// 2) Authorization: Bearer
	if a := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(a), "bearer ") {
		return strings.TrimSpace(a[7:])
	}
	// 3) query token=
	return r.URL.Query().Get("token")
}

func requireTokenOrWhitelist(cfg ConfigReader, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if inWhitelist(cfg, ip) {
			next.ServeHTTP(w, r)
			return
		}
		t := extractToken(r)
		if t == "" || !cfg.HasToken(t) {
			writeJSON(w, 401, map[string]any{"ok": false, "msg": "需要 Token 或内网白名单"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isAdminAuthed(store AuthStore, r *http.Request) bool {
	if store == nil {
		return false
	}
	c, err := r.Cookie("sid")
	if err != nil || c.Value == "" {
		return false
	}
	return store.Has(c.Value)
}

func requireAdmin(store AuthStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAdminAuthed(store, r) {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, "admin login required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ===== logs helpers =====

func latestLogFile(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	var bestTime time.Time

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".log") {
			continue
		}
		p := filepath.Join(dir, name)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.ModTime().After(bestTime) {
			bestTime = st.ModTime()
			best = p
		}
	}
	return best
}

func filterLines(text, q string, major bool) []string {
	raw := strings.Split(text, "\n")
	out := make([]string, 0, 200)
	for i := len(raw) - 1; i >= 0; i-- { // 倒序取最近
		line := raw[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if major {
			if !strings.Contains(line, "MAJOR_") && !strings.Contains(line, "ABNORMAL_RESTART") {
				continue
			}
		}
		if q != "" && !strings.Contains(line, q) {
			continue
		}
		out = append(out, line)
		if len(out) >= 200 {
			break
		}
	}
	return out
}
