package auth

import (
	"encoding/json"
	"net/http"
)

// ConfigReader：避免 auth 包直接依赖 config 包（解耦）
type ConfigReader interface {
	GetWhitelist() []string
	HasToken(token string) bool

	// 管理登录账号（用于 /login）
	CheckAdmin(username, password string) bool
}

// RequireTokenOrWhitelist
// - 内网白名单：免 Token
// - 外网：必须 Token
// - 不区分 HTTP/WS：所有入口统一套这个 middleware
func RequireTokenOrWhitelist(cfg ConfigReader) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ClientIP(r)
			if InWhitelist(ip, cfg.GetWhitelist()) {
				next.ServeHTTP(w, r)
				return
			}
			token := ExtractToken(r)
			if token == "" || !cfg.HasToken(token) {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok":    false,
					"error": "UNAUTHORIZED",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAdminSession：仅允许管理登录态访问
func RequireAdminSession(store *AuthStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie("admin_session")
			if err != nil || c == nil || c.Value == "" || !store.ValidSession(c.Value) {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok":    false,
					"error": "ADMIN_REQUIRED",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
