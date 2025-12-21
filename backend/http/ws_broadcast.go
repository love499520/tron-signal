package httpapi

import (
	"net/http"

	"tron-signal/backend/app"
)

func wsBroadcastHandler(core *app.Core) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 这里的 WebSocket 只用于服务端广播信号
		// 具体握手/读写实现放在 core 内部，http 层只做入口
		if err := core.AcceptBroadcastWS(w, r); err != nil {
			JSONStatus(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
	})
}
