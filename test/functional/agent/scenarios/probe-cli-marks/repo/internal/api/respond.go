// Package api holds the HTTP handlers.
package api

import (
	"encoding/json"
	"net/http"

	"example.com/app/internal/log"
)

// JSON writes v as a JSON response with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Warn("write response", "err", err)
	}
}

// Error writes the error envelope {"error": msg} with the given status.
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}
