package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"tron-signal/backend/app"
)

func apiStatusHandler(core *app.Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		NoCache(w)
		JSON(w, core.GetStatus())
	}
}

func sseStatusHandler(core *app.Core) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		NoCache(w)

		flusher, ok := w.(http.Flusher)
		if !ok {
			JSONStatus(w, http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "SSE_NOT_SUPPORTED",
			})
			return
		}

		h := w.Header()
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no")

		ch, cancel := core.SubscribeStatus()
		defer cancel()

		writeSSEJSON(w, "status", core.GetStatus())
		flusher.Flush()

		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case v, ok := <-ch:
				if !ok {
					return
				}
				writeSSEJSON(w, "status", v)
				flusher.Flush()
			case <-ticker.C:
				_, _ = w.Write([]byte(": ping\n\n"))
				flusher.Flush()
			}
		}
	})
}

func writeSSEJSON(w http.ResponseWriter, event string, v any) {
	b, _ := json.Marshal(v)
	_, _ = w.Write([]byte("event: " + event + "\n"))
	_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
}
