package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"tron-signal/backend/config"
)

func apiTokensListHandler(store *config.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		NoCache(w)
		if r.Method != http.MethodGet {
			JSONStatus(w, http.StatusMethodNotAllowed, map[string]any{
				"ok":    false,
				"error": "METHOD_NOT_ALLOWED",
			})
			return
		}

		cfg := store.Get()
		JSON(w, map[string]any{
			"ok":     true,
			"tokens": cfg.Tokens,
		})
	}
}

func apiTokensAddHandler(store *config.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		NoCache(w)
		if r.Method != http.MethodPost {
			JSONStatus(w, http.StatusMethodNotAllowed, map[string]any{
				"ok":    false,
				"error": "METHOD_NOT_ALLOWED",
			})
			return
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			JSONStatus(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "BAD_JSON",
			})
			return
		}

		raw, _ := body["token"].(string)
		raw = strings.TrimSpace(raw)
		if raw == "" {
			JSONStatus(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "MISSING_TOKEN",
			})
			return
		}

		if err := store.AddToken(raw); err != nil {
			JSONStatus(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}

		JSON(w, map[string]any{"ok": true})
	}
}

func apiTokensDeleteHandler(store *config.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		NoCache(w)
		if r.Method != http.MethodPost {
			JSONStatus(w, http.StatusMethodNotAllowed, map[string]any{
				"ok":    false,
				"error": "METHOD_NOT_ALLOWED",
			})
			return
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			JSONStatus(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "BAD_JSON",
			})
			return
		}

		raw, _ := body["token"].(string)
		raw = strings.TrimSpace(raw)
		if raw == "" {
			JSONStatus(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "MISSING_TOKEN",
			})
			return
		}

		if err := store.DeleteToken(raw); err != nil {
			JSONStatus(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}

		JSON(w, map[string]any{"ok": true})
	}
}
