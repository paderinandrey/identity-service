package main

import (
	"encoding/json"
	"net/http"
)

// routes exposes the projection for the stand's assertions.
func routes(p *Projection) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /projection", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"users": p.Users()})
	})
	mux.HandleFunc("GET /projection/{id}", func(w http.ResponseWriter, r *http.Request) {
		u, ok := p.User(r.PathValue("id"))
		if !ok {
			http.Error(w, "not in projection", http.StatusNotFound)
			return
		}
		writeJSON(w, u)
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, p.Stats())
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
