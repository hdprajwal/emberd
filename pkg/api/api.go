package api

import (
	"encoding/json"
	"net/http"
)

type CreateSandboxRequest struct {
	LanguagePack string `json:"language_pack"`
}

type CreateSandboxResponse struct {
	ID string `json:"id"`
}

type ExecRequest struct {
	Code      string `json:"code"`
	Stdin     string `json:"stdin,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type ExecResponse struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int    `json:"duration_ms"`
}

func Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /sandboxes", handleCreate)
	mux.HandleFunc("POST /sandboxes/{id}/exec", handleExec)
	mux.HandleFunc("DELETE /sandboxes/{id}", handleDelete)
}

func handleCreate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not implemented"})
}

func handleExec(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not implemented"})
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not implemented"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
