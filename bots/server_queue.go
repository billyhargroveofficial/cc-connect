package bots

import "net/http"

func (s *Server) messageQueue(w http.ResponseWriter, r *http.Request) {
	if !s.needRuntime(w) {
		return
	}
	queue, err := s.runtime.Queue(r.PathValue("id"))
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, queue)
}

func (s *Server) resumeMessageQueue(w http.ResponseWriter, r *http.Request) {
	if !s.needRuntime(w) {
		return
	}
	if err := s.runtime.ResumeQueue(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) cancelQueuedMessage(w http.ResponseWriter, r *http.Request) {
	if !s.needRuntime(w) {
		return
	}
	if err := s.runtime.CancelQueuedMessage(r.Context(), r.PathValue("id"), r.PathValue("messageId")); err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) steerQueuedMessage(w http.ResponseWriter, r *http.Request) {
	if !s.needRuntime(w) {
		return
	}
	receipt, err := s.runtime.SteerQueuedMessage(r.Context(), r.PathValue("id"), r.PathValue("messageId"))
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, receipt)
}
