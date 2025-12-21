package httpapi

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"tron-signal/backend/auth"
	"tron-signal/backend/config"
)

// ====== Access Guard (内网白名单免 Token / 外网必须 Token) ======

func GuardAccess(store *config.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if allowByIPWhitelist(store, r) || allowByToken(store, r) {
			next.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"ok":    false,
			"error": "UNAUTHORIZED",
		})
	})
}

func GuardAccessFunc(store *config.Store, fn http.HandlerFunc) http.HandlerFunc {
	h := GuardAccess(store, fn)
	return func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }
}

// ====== Manager Guard (仅管理登录态) ======

func GuardManager(store *config.Store, sessions *auth.MemoryStore, next http.Handler) http.Handler {
	_ = store // 预留：未来可做 manager 白名单策略；目前仅看 session
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sessions != nil && sessions.IsLoggedIn(r) {
			next.ServeHTTP(w, r)
			return
		}
		// 管理页/文档：未登录则跳转登录页（移动端友好）
		http.Redirect(w, r, "/login", http.StatusFound)
	})
}

func GuardManagerFunc(store *config.Store, sessions *auth.MemoryStore, fn http.HandlerFunc) http.HandlerFunc {
	h := GuardManager(store, sessions, fn)
	return func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }
}

// ====== helpers ======

func allowByToken(store *config.Store, r *http.Request) bool {
	cfg := store.Get()

	// 外网必须 Token：Authorization: Bearer <token>
	authz := r.Header.Get("Authorization")
	authz = strings.TrimSpace(authz)
	if authz == "" {
		return false
	}
	parts := strings.SplitN(authz, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return false
	}

	for _, t := range cfg.Tokens {
		if t == token {
			return true
		}
	}
	return false
}

func allowByIPWhitelist(store *config.Store, r *http.Request) bool {
	cfg := store.Get()
	ip := clientIP(r)
	if ip == "" {
		return false
	}
	// IP 白名单支持：精确 IP 或 CIDR
	for _, item := range cfg.IPWhitelist {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		// CIDR
		if strings.Contains(item, "/") {
			_, n, err := net.ParseCIDR(item)
			if err != nil {
				continue
			}
			if n.Contains(net.ParseIP(ip)) {
				return true
			}
			continue
		}
		// 精确 IP
		if item == ip {
			return true
		}
	}
	return false
}

func clientIP(r *http.Request) string {
	// 优先 X-Forwarded-For（只取第一个）
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[0])
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	// 其次 X-Real-IP
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		if net.ParseIP(xr) != nil {
			return xr
		}
	}
	// 最后 RemoteAddr
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && net.ParseIP(host) != nil {
		return host
	}
	// RemoteAddr 可能直接就是 IP
	if net.ParseIP(r.RemoteAddr) != nil {
		return r.RemoteAddr
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
