package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"tron-signal/backend/app"
)

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

		enc := json.NewEncoder(w)

		tick := time.NewTicker(1 * time.Second)
		defer tick.Stop()

		// 初始推送
		_ = writeSSE(enc, w, "status", core.GetStatus())
		flusher.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				_ = writeSSE(enc, w, "status", core.GetStatus())
				flusher.Flush()
			}
		}
	})
}

func writeSSE(enc *json.Encoder, w http.ResponseWriter, event string, v any) error {
	// event
	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
		return err
	}
	// data:
	if _, err := fmt.Fprint(w, "data: "); err != nil {
		return err
	}
	if err := enc.Encode(v); err != nil {
		return err
	}
	// blank line ends event
	_, err := fmt.Fprint(w, "\n")
	return err
}
