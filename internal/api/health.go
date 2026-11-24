package api

import (
	"encoding/json"
	"net/http"

	"github.com/X9st/TianFaSRC/internal/system"
)

type healthResponse struct {
	Status  string                  `json:"status"`
	Service string                  `json:"service"`
	Version string                  `json:"version"`
	Checks  map[string]system.Check `json:"checks"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
