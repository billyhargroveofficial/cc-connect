package bots

import (
	"fmt"
	"net/http"
)

func (s *Server) botContext(w http.ResponseWriter, r *http.Request) {
	if !s.needRuntime(w) {
		return
	}
	result, err := s.runtime.Context(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) compactBot(w http.ResponseWriter, r *http.Request) {
	if !s.needRuntime(w) {
		return
	}
	var input struct {
		Instructions string `json:"instructions"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(input.Instructions) > 24000 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("compaction instructions are too long"))
		return
	}
	result, err := s.runtime.Compact(r.Context(), r.PathValue("id"), input.Instructions)
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}
