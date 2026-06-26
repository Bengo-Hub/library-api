package handlers

import (
	"encoding/json"
	"net/http"
)

// RespondJSON is the exported variant used by the router and other packages.
func RespondJSON(w http.ResponseWriter, status int, v any) {
	respondJSON(w, status, v)
}

// respondJSON writes v as a JSON response with the given status code.
func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// respondError writes a uniform error envelope. code is a stable machine-readable
// string (e.g. "subscription_inactive") consumed by the frontends.
func respondError(w http.ResponseWriter, status int, message, code string) {
	respondJSON(w, status, map[string]any{
		"error": message,
		"code":  code,
	})
}
