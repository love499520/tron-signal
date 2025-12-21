package httpapi

import (
	"net/http"

	"tron-signal/backend/app"
	"tron-signal/backend/auth"
	"tron-signal/backend/config"
)

// Register：HTTP 路由总入口（最终重构版）
// 权限模型：内网白名单免 Token，外网访问必须 Token。
// HTTP 与 WS 不再区分鉴权，仅按访问来源区分。
func Register(mux *http.ServeMux, core *app.Core, store *config.Store, sessions *auth.MemoryStore) {
	// ========== Docs ==========
	mux.Handle("/docs", GuardManager(store, sessions, docsIndexHandler()))
	mux.Handle("/docs/", GuardManager(store, sessions,
		http.StripPrefix("/docs/", http.FileServer(http.Dir("web/docs"))),
	))
	mux.Handle("/api/docs/api.md", GuardManager(store, sessions, docsAPIFileHandler()))

	// ========== 登录与管理 ==========
	mux.HandleFunc("/login", loginHandler(core, store, sessions))
	mux.HandleFunc("/logout", logoutHandler(store, sessions))
	mux.HandleFunc("/admin/password", GuardManagerFunc(store, sessions, adminPasswordHandler(core, store)))

	// ========== API ==========
	mux.HandleFunc("/api/status", GuardAccessFunc(store, apiStatusHandler(core)))

	mux.HandleFunc("/api/judge/switch", GuardAccessFunc(store, apiJudgeSwitchHandler(core)))
	mux.HandleFunc("/api/machines", GuardAccessFunc(store, apiMachinesGetHandler(core)))
	mux.HandleFunc("/api/machines/save", GuardAccessFunc(store, apiMachinesSaveHandler(core)))

	mux.HandleFunc("/api/sources", GuardAccessFunc(store, apiSourcesGetHandler(core)))
	mux.HandleFunc("/api/sources/upsert", GuardAccessFunc(store, apiSourcesUpsertHandler(core)))
	mux.HandleFunc("/api/sources/delete", GuardAccessFunc(store, apiSourcesDeleteHandler(core)))

	mux.HandleFunc("/api/tokens", GuardAccessFunc(store, apiTokensGetHandler(core)))
	mux.HandleFunc("/api/tokens/add", GuardAccessFunc(store, apiTokensAddHandler(core)))
	mux.HandleFunc("/api/tokens/delete", GuardAccessFunc(store, apiTokensDeleteHandler(core)))

	mux.HandleFunc("/api/logs", GuardAccessFunc(store, apiLogsGetHandler(core)))

	// ========== SSE ==========
	mux.Handle("/sse/status", GuardAccess(store, sseStatusHandler(core)))

	// ========== WS ==========
	mux.Handle("/ws", GuardAccess(store, wsSignalHandler(core)))

	// ========== 静态资源 ==========
	mux.Handle("/", http.FileServer(http.Dir("web")))
}
