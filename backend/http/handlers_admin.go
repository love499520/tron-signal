package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"tron-signal/backend/auth"
	"tron-signal/backend/logs"
)

// AdminHandlers：登录 + token 管理 + docs gate + 日志查看
type AdminHandlers struct {
	AuthStore *auth.AuthStore
	Cfg       auth.ConfigReader

	LogDir string
}

// POST /api/admin/login {username,password}
func (h *AdminHandlers) Login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)

	if !h.Cfg.CheckAdmin(in.Username, in.Password) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "LOGIN_FAILED"})
		return
	}

	sid := h.AuthStore.CreateSession(24 * time.Hour)
	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// HTTPS 环境可加 Secure
	})
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// POST /api/admin/logout
func (h *AdminHandlers) Logout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("admin_session")
	if err == nil && c != nil && c.Value != "" {
		h.AuthStore.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// GET /api/admin/logs?limit=200&level=MAJOR&majorOnly=1
func (h *AdminHandlers) Logs(w http.ResponseWriter, r *http.Request) {
	limit := 200
	level := r.URL.Query().Get("level")
	majorOnly := r.URL.Query().Get("majorOnly") == "1"

	out, err := logs.TailRead(h.LogDir, limit, level, majorOnly)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "items": []any{}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "items": out})
}
