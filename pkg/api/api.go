package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hdprajwal/emberd/pkg/proto"
	"github.com/hdprajwal/emberd/pkg/sandbox"
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
	Error      string `json:"error,omitempty"`
}

// Server wires the HTTP surface to a sandbox.Manager.
type Server struct {
	mgr sandbox.Manager
}

func NewServer(mgr sandbox.Manager) *Server {
	return &Server{mgr: mgr}
}

// Register attaches the sandbox routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /info", s.handleInfo)
	mux.HandleFunc("POST /sandboxes", s.handleCreate)
	mux.HandleFunc("POST /sandboxes/{id}/exec", s.handleExec)
	mux.HandleFunc("DELETE /sandboxes/{id}", s.handleDelete)
}

// handleInfo returns the daemon's resolved sandbox configuration (guest RAM,
// vCPUs, boot path, packs, work dir) as JSON so tooling such as the benchmark
// harness records real guest facts instead of hardcoding them. The body is
// small and static, so no size limit applies; there is no auth, matching the
// other routes and the same bind address.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.Info())
}

const (
	maxCreateBody = 4 << 10 // 4 KiB — language_pack name only
	maxExecBody   = 1 << 20 // 1 MiB — code + stdin
)

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req CreateSandboxRequest
	if r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxCreateBody)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	if req.LanguagePack == "" {
		req.LanguagePack = "python"
	}

	sb, err := s.mgr.Create(r.Context(), req.LanguagePack)
	switch {
	case errors.Is(err, sandbox.ErrUnknownPack):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, CreateSandboxResponse{ID: sb.ID})
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	r.Body = http.MaxBytesReader(w, r.Body, maxExecBody)
	var req ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	res, err := s.mgr.Exec(r.Context(), id, proto.ExecRequest{
		Code:      req.Code,
		Stdin:     req.Stdin,
		TimeoutMs: req.TimeoutMs,
	})
	switch {
	case errors.Is(err, sandbox.ErrNotFound):
		writeError(w, http.StatusNotFound, "sandbox not found")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, ExecResponse{
		Stdout:     res.Stdout,
		Stderr:     res.Stderr,
		ExitCode:   res.ExitCode,
		DurationMs: res.DurationMs,
		Error:      res.Error,
	})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	err := s.mgr.Delete(r.Context(), id)
	switch {
	case errors.Is(err, sandbox.ErrNotFound):
		writeError(w, http.StatusNotFound, "sandbox not found")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
