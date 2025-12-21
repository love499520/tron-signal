package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"tron-signal/backend/app"
)

// writeEvent 统一写 SSE 事件
func writeEvent(w http.ResponseWriter, event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if event != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", event)
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// Headers 设置 SSE 必要 Header
func Headers(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx
}

// StatusHandler
// - 定时推送系统状态
// - UI 连接状态 / 判定规则 / 高度 / 事故数
func StatusHandler(core *app.Core, interval time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		Headers(w)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// 立即推一次
		_ = writeEvent(w, "status", core.Status())

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				_ = writeEvent(w, "status", core.Status())
			}
		}
	}
}

// BlocksHandler
// - 推送最近 50 个区块快照
// - UI 自行处理滚动行为（是否在顶部）
func BlocksHandler(core *app.Core, interval time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		Headers(w)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// 首次全量
		_ = writeEvent(w, "blocks", core.Blocks())

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				_ = writeEvent(w, "blocks", core.Blocks())
			}
		}
	}
}
