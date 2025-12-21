package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"tron-signal/backend/app"
	"tron-signal/backend/machine"
	"tron-signal/backend/block"
)

// SSE：前端状态更新（1101）
// - UI 状态更新仅用 SSE
// - WS 仅广播信号
//
// endpoint：/sse/status
// payload：包含 status / blocks / machineRuntime
type SSEPayload struct {
	Status   app.Status              `json:"status"`
	Blocks   []block.Block           `json:"blocks"`
	Runtime  map[string]machine.Runtime `json:"runtime"`
	WsClients int                    `json:"wsClients"`
}

func HandleSSE(core *app.Core, ring *block.RingBuffer, mgr *machine.Manager, wsCount func() int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		ticker := time.NewTicker(800 * time.Millisecond)
		defer ticker.Stop()

		notify := r.Context().Done()

		for {
			select {
			case <-notify:
				return
			case <-ticker.C:
				p := SSEPayload{
					Status:    core.GetStatus(),
					Blocks:    ring.List(),
					Runtime:   mgr.RuntimeSnapshot(),
					WsClients: wsCount(),
				}
				b, _ := json.Marshal(p)

				// SSE event
				fmt.Fprintf(w, "event: status\n")
				fmt.Fprintf(w, "data: %s\n\n", string(b))
				flusher.Flush()
			}
		}
	}
}
