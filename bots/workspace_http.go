package bots

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
)

func workspaceBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkspaceFile+4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func workspaceError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		status = http.StatusNotFound
	}
	if errors.Is(err, ErrConflict) {
		status = http.StatusConflict
	}
	writeError(w, status, err)
}

func (w *Workspace) mutate(runtime *Runtime, botID string, change func() error) error {
	if runtime == nil {
		return change()
	}
	if botID != "" {
		return runtime.WithIdleBot(botID, change)
	}
	ids := []string{}
	for _, bot := range w.store.ListBots() {
		if bot.Status != "archived" {
			ids = append(ids, bot.ID)
		}
	}
	// Collect all gates before acquiring any bot lock. Shared edits cannot race
	// a new send and never re-enter runtime methods from a mutation callback.
	return runtime.WithIdleBots(ids, change)
}

// RegisterHTTP adds handlers to an already authenticated, same-origin API mux.
func (w *Workspace) RegisterHTTP(mux *http.ServeMux, runtime *Runtime) {
	mux.HandleFunc("GET /api/studio/bots/{id}/instructions", func(out http.ResponseWriter, r *http.Request) {
		content, path, err := w.Instructions(r.PathValue("id"))
		if err != nil {
			workspaceError(out, err)
			return
		}
		writeJSON(out, http.StatusOK, map[string]string{"content": content, "path": path})
	})
	mux.HandleFunc("GET /api/studio/user/instructions", func(out http.ResponseWriter, r *http.Request) {
		content, path, err := w.UserInstructions()
		if err != nil {
			workspaceError(out, err)
			return
		}
		writeJSON(out, http.StatusOK, map[string]string{"content": content, "path": path})
	})
	putInstructions := func(out http.ResponseWriter, r *http.Request) {
		var body struct {
			Content string `json:"content"`
		}
		if err := workspaceBody(out, r, &body); err != nil {
			workspaceError(out, err)
			return
		}
		id := r.PathValue("id")
		if err := w.mutate(runtime, id, func() error { return w.SetInstructions(id, body.Content) }); err != nil {
			workspaceError(out, err)
			return
		}
		var content, path string
		if id == "" {
			content, path, _ = w.UserInstructions()
		} else {
			content, path, _ = w.Instructions(id)
		}
		writeJSON(out, http.StatusOK, map[string]string{"content": content, "path": path})
	}
	mux.HandleFunc("PUT /api/studio/bots/{id}/instructions", putInstructions)
	mux.HandleFunc("PUT /api/studio/user/instructions", putInstructions)
	mux.HandleFunc("GET /api/studio/bots/{id}/skills", func(out http.ResponseWriter, r *http.Request) {
		skills, err := w.BotSkills(r.PathValue("id"))
		if err != nil {
			workspaceError(out, err)
			return
		}
		writeJSON(out, http.StatusOK, map[string]any{"skills": skills})
	})
	mux.HandleFunc("GET /api/studio/user/skills", func(out http.ResponseWriter, r *http.Request) {
		skills, err := w.UserSkills()
		if err != nil {
			workspaceError(out, err)
			return
		}
		writeJSON(out, http.StatusOK, map[string]any{"skills": skills})
	})
	mux.HandleFunc("PATCH /api/studio/bots/{id}/skills", func(out http.ResponseWriter, r *http.Request) {
		var body struct {
			DisabledSkills []string `json:"disabledSkills"`
		}
		if err := workspaceBody(out, r, &body); err != nil {
			workspaceError(out, err)
			return
		}
		id := r.PathValue("id")
		var bot Bot
		if err := w.mutate(runtime, id, func() error { var err error; bot, err = w.SetDisabledSkills(id, body.DisabledSkills); return err }); err != nil {
			workspaceError(out, err)
			return
		}
		writeJSON(out, http.StatusOK, bot)
	})
	createSkill := func(out http.ResponseWriter, r *http.Request) {
		var body struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		}
		if err := workspaceBody(out, r, &body); err != nil {
			workspaceError(out, err)
			return
		}
		id := r.PathValue("id")
		var skill Skill
		if err := w.mutate(runtime, id, func() error { var err error; skill, err = w.CreateSkill(id, body.Name, body.Content); return err }); err != nil {
			workspaceError(out, err)
			return
		}
		writeJSON(out, http.StatusCreated, skill)
	}
	mux.HandleFunc("POST /api/studio/bots/{id}/skills", createSkill)
	mux.HandleFunc("POST /api/studio/user/skills", createSkill)
	mux.HandleFunc("GET /api/studio/skills/content", func(out http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		content, err := w.SkillContent(path)
		if err != nil {
			workspaceError(out, err)
			return
		}
		writeJSON(out, http.StatusOK, map[string]string{"content": content, "path": path})
	})
	mux.HandleFunc("PUT /api/studio/skills/content", func(out http.ResponseWriter, r *http.Request) {
		var body struct {
			Content string `json:"content"`
		}
		if err := workspaceBody(out, r, &body); err != nil {
			workspaceError(out, err)
			return
		}
		path := r.URL.Query().Get("path")
		if err := w.mutate(runtime, "", func() error { return w.SetSkillContent(path, body.Content) }); err != nil {
			workspaceError(out, err)
			return
		}
		writeJSON(out, http.StatusOK, map[string]string{"content": body.Content, "path": path})
	})
	mux.HandleFunc("POST /api/studio/bots/{id}/uploads", func(out http.ResponseWriter, r *http.Request) {
		data, name, mimeType, err := workspaceMultipart(out, r)
		if err != nil {
			workspaceError(out, err)
			return
		}
		attachment, err := w.StoreUpload(r.PathValue("id"), name, mimeType, data)
		if err != nil {
			workspaceError(out, err)
			return
		}
		writeJSON(out, http.StatusCreated, attachment)
	})
	mux.HandleFunc("GET /api/studio/bots/{id}/files/{attachmentId}", w.serveUpload)
	mux.HandleFunc("POST /api/studio/transcribe", w.handleTranscribe)
}

func workspaceMultipart(out http.ResponseWriter, r *http.Request) ([]byte, string, string, error) {
	r.Body = http.MaxBytesReader(out, r.Body, maxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		return nil, "", "", err
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return nil, "", "", errors.New("multipart field file is required")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		return nil, "", "", err
	}
	if len(data) == 0 {
		return nil, "", "", errors.New("upload is empty")
	}
	if len(data) > maxUploadBytes {
		return nil, "", "", errors.New("upload exceeds 25 MiB")
	}
	return data, header.Filename, header.Header.Get("Content-Type"), nil
}

func (w *Workspace) serveUpload(out http.ResponseWriter, r *http.Request) {
	attachments, err := w.ResolveAttachments(r.PathValue("id"), []Attachment{{ID: r.PathValue("attachmentId")}})
	if err != nil {
		workspaceError(out, err)
		return
	}
	attachment := attachments[0]
	root, err := os.OpenRoot(w.store.Root())
	if err != nil {
		workspaceError(out, err)
		return
	}
	defer root.Close()
	rel, err := filepath.Rel(w.store.Root(), attachment.Path)
	if err != nil {
		workspaceError(out, err)
		return
	}
	file, err := root.Open(rel)
	if err != nil {
		workspaceError(out, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		workspaceError(out, err)
		return
	}
	out.Header().Set("Content-Type", attachment.MimeType)
	out.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Name}))
	out.Header().Set("X-Content-Type-Options", "nosniff")
	out.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(out, r, attachment.Name, info.ModTime(), file)
}
