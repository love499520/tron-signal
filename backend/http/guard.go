package httpapi

import (
	"net/http"
	"strings"

	"tron-signal/backend/auth"
	"tron-signal/backend/config"
)

func GuardManager(store *config.Store, sessions *auth.MemoryStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sessions.IsLoggedIn(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func GuardManagerFunc(store *config.Store, sessions *auth.MemoryStore, fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sessions.IsLoggedIn(r) {
			JSONStatus(w, http.StatusUnauthorized, map[string]any{
				"ok":    false,
				"error": "NEED_LOGIN",
			})
			return
		}
		fn(w, r)
	}
}

// 外部访问入口：只区分 内网白名单 / 外网 Token，不区分 HTTP/WS
func GuardAccessFunc(store *config.Store, fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store.IsWhitelistedIP(ClientIP(r)) {
			fn(w, r)
			return
		}

		token := bearerToken(r)
		if token == "" {
			JSONStatus(w, http.StatusUnauthorized, map[string]any{
				"ok":    false,
				"error": "NEED_TOKEN",
			})
			return
		}

		if !store.HasToken(token) {
			JSONStatus(w, http.StatusForbidden, map[string]any{
				"ok":    false,
				"error": "BAD_TOKEN",
			})
			return
		}

		fn(w, r)
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 {
		return ""
	}
	if strings.ToLower(strings.TrimSpace(parts[0])) != "bearer" {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
