package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"tron-signal/backend/app"
)

func apiLogsHandler(core *app.Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		NoCache(w)
		if r.Method != http.MethodGet {
			JSONStatus(w, http.StatusMethodNotAllowed, map[string]any{
				"ok":    false,
				"error": "METHOD_NOT_ALLOWED",
			})
			return
		}

		typ := strings.TrimSpace(r.URL.Query().Get("type"))     // all | major
		source := strings.TrimSpace(r.URL.Query().Get("source")) // optional
		limitStr := strings.TrimSpace(r.URL.Query().Get("limit"))
		if typ == "" {
			typ = "all"
		}

		limit := 200
		if limitStr != "" {
			if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 2000 {
				limit = v
			}
		}

		items := core.ReadLogs(typ, source, limit)

		JSON(w, map[string]any{
			"ok":    true,
			"type":  typ,
			"source": source,
			"limit": limit,
			"items": items,
		})
	}
}
