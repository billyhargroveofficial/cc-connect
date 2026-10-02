package bots

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const sessionCookieName = "connect_bots_session"
const sessionDuration = 7 * 24 * time.Hour

type ServerConfig struct {
	Token          string
	StaticDir      string
	StaticFS       fs.FS
	AllowedOrigins []string
	FlovURL        string
	InternalToken  string
	Workspace      *Workspace
	BotChanged     func(string)
}

type loginAttempt struct {
	count int
	since time.Time
}

type Server struct {
	store       *Store
	runtime     *Runtime
	workspace   *Workspace
	maintenance *Maintenance
	config      ServerConfig
	handler     http.Handler
	apiHandler  http.Handler
	internal    http.Handler
	tokenHash   [32]byte
	cookieKey   [32]byte
	loginMu     sync.Mutex
	loginLimits map[string]loginAttempt
}

func NewServer(store *Store, runtime *Runtime, config ServerConfig) (*Server, error) {
	if store == nil {
		return nil, fmt.Errorf("server requires a store")
	}
	token, err := loadOwnerToken(store.Root(), config.Token)
	if err != nil {
		return nil, err
	}
	s, err := NewTenantServer(store, runtime, config)
	if err != nil {
		return nil, err
	}
	// Keep only the token hash after initialization; never expose tokens via API.
	s.tokenHash = sha256.Sum256([]byte(token))
	s.loginLimits = make(map[string]loginAttempt)
	key := hmac.New(sha256.New, []byte(token))
	key.Write([]byte("connect-bots/session/v1"))
	copy(s.cookieKey[:], key.Sum(nil))
	s.handler = s.buildHandler()
	return s, nil
}

// NewTenantServer creates the workspace services without owner-token login,
// session, or static routes. The host must authenticate requests before
// dispatching them to APIHandler or Handler.
func NewTenantServer(store *Store, runtime *Runtime, config ServerConfig) (*Server, error) {
	if store == nil {
		return nil, fmt.Errorf("server requires a store")
	}
	s := &Server{store: store, runtime: runtime, config: config}
	s.config.Token = ""
	s.workspace = config.Workspace
	if s.workspace == nil {
		s.workspace = NewWorkspace(store)
	}
	s.workspace.Configure(WorkspaceConfig{FlovURL: config.FlovURL})
	s.maintenance = NewMaintenance(store, runtime)
	s.apiHandler = s.buildAPIHandler()
	internal := http.NewServeMux()
	internal.HandleFunc("POST /api/studio/internal/tools", s.internalTool)
	s.internal = internal
	s.handler = s.apiHandler
	return s, nil
}

func (s *Server) Handler() http.Handler               { return s.handler }
func (s *Server) Workspace() *Workspace               { return s.workspace }
func (s *Server) Maintenance() *Maintenance           { return s.maintenance }
func (s *Server) StartBackground(ctx context.Context) { s.maintenance.Start(ctx) }
func (s *Server) Close() error                        { return s.maintenance.Close() }

// APIHandler contains workspace routes only. Its caller is responsible for
// authenticating the account and validating mutation origins.
func (s *Server) APIHandler() http.Handler { return s.apiHandler }

// InternalHandler retains the separate internal credential and loopback checks;
// account authentication alone never grants access to runtime tools.
func (s *Server) InternalHandler() http.Handler { return s.internal }

func (s *Server) buildAPIHandler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/studio/bots", s.listBots)
	api.HandleFunc("POST /api/studio/bots", s.createBot)
	api.HandleFunc("GET /api/studio/bots/{id}", s.getBot)
	api.HandleFunc("PATCH /api/studio/bots/{id}", s.patchBot)
	api.HandleFunc("DELETE /api/studio/bots/{id}", s.archiveBot)
	api.HandleFunc("GET /api/studio/bots/{id}/events", s.botEvents)
	api.HandleFunc("GET /api/studio/events", s.streamEvents)
	api.HandleFunc("POST /api/studio/bots/{id}/messages", s.sendMessage)
	api.HandleFunc("GET /api/studio/bots/{id}/queue", s.messageQueue)
	api.HandleFunc("POST /api/studio/bots/{id}/queue/resume", s.resumeMessageQueue)
	api.HandleFunc("DELETE /api/studio/bots/{id}/queue/{messageId}", s.cancelQueuedMessage)
	api.HandleFunc("POST /api/studio/bots/{id}/queue/{messageId}/steer", s.steerQueuedMessage)
	api.HandleFunc("POST /api/studio/bots/{id}/stop", s.stopBot)
	api.HandleFunc("POST /api/studio/bots/{id}/permission", s.permission)
	api.HandleFunc("GET /api/studio/capabilities", s.capabilities)
	api.HandleFunc("GET /api/studio/bots/{id}/goal", s.goal)
	api.HandleFunc("PUT /api/studio/bots/{id}/goal", s.goal)
	api.HandleFunc("DELETE /api/studio/bots/{id}/goal", s.goal)
	api.HandleFunc("GET /api/studio/bots/{id}/context", s.botContext)
	api.HandleFunc("POST /api/studio/bots/{id}/compact", s.compactBot)
	s.workspace.RegisterHTTP(api, s.runtime)
	s.maintenance.RegisterHTTP(api)
	return api
}

func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/studio/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /api/studio/session", s.session)
	mux.HandleFunc("POST /api/studio/login", s.login)
	mux.HandleFunc("POST /api/studio/logout", s.logout)
	mux.Handle("POST /api/studio/internal/tools", s.InternalHandler())
	mux.Handle("/api/studio/", s.requireAuth(s.APIHandler()))
	mux.Handle("/", s.staticHandler())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) listBots(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"bots": s.store.ListBots()})
}

func (s *Server) getBot(w http.ResponseWriter, r *http.Request) {
	bot, err := s.store.GetBot(r.PathValue("id"))
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, bot)
}

func (s *Server) createBot(w http.ResponseWriter, r *http.Request) {
	var fields map[string]json.RawMessage
	if err := decodeJSON(w, r, &fields); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	for field := range fields {
		if !editableBotField(field) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("field %q is not editable", field))
			return
		}
	}
	encoded, _ := json.Marshal(fields)
	var input Bot
	if err := json.Unmarshal(encoded, &input); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid bot profile"))
		return
	}
	bot, err := s.store.CreateBot(input)
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	s.botChanged(bot.ID)
	writeJSON(w, http.StatusCreated, bot)
}

func (s *Server) patchBot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var fields map[string]json.RawMessage
	if err := decodeJSON(w, r, &fields); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var bot Bot
	mutateIdle := s.mutateIdleBot
	if _, tierOnly := fields["serviceTier"]; tierOnly && len(fields) == 1 && s.runtime != nil {
		// Tier-only edits retain the loaded native thread. Its next turn/control
		// request applies the setting before continuing; accepting a turn still
		// shares the same lifecycle lock as the persistent profile mutation.
		mutateIdle = s.runtime.WithIdleBotAccess
	}
	err := mutateIdle(id, func() error {
		var err error
		bot, err = s.store.PatchBot(id, fields)
		return err
	})
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	s.botChanged(id)
	writeJSON(w, http.StatusOK, bot)
}

func (s *Server) archiveBot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var bot Bot
	err := s.mutateIdleBot(id, func() error {
		var err error
		bot, err = s.store.ArchiveBot(id)
		return err
	})
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	s.botChanged(id)
	writeJSON(w, http.StatusOK, bot)
}

func (s *Server) botEvents(w http.ResponseWriter, r *http.Request) {
	after, err := eventCursor(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	events, err := s.store.Events(r.PathValue("id"), after)
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	after, err := eventCursor(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming is unavailable"))
		return
	}
	events, cancel, err := s.store.Subscribe(after)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: event\ndata: %s\n\n", event.Seq, data); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	if !s.needRuntime(w) {
		return
	}
	var input MessageRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	receipt, err := s.runtime.SubmitMessage(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, receipt)
}

func (s *Server) stopBot(w http.ResponseWriter, r *http.Request) {
	if !s.needRuntime(w) {
		return
	}
	if err := s.runtime.Stop(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) permission(w http.ResponseWriter, r *http.Request) {
	if !s.needRuntime(w) {
		return
	}
	var input struct {
		RequestID    string         `json:"requestId"`
		Behavior     string         `json:"behavior"`
		UpdatedInput map[string]any `json:"updatedInput"`
		Message      string         `json:"message"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.RequestID == "" || (input.Behavior != "allow" && input.Behavior != "deny") {
		writeError(w, http.StatusBadRequest, fmt.Errorf("requestId and allow/deny behavior are required"))
		return
	}
	if err := s.runtime.Permission(r.Context(), r.PathValue("id"), input.RequestID, core.PermissionResult{Behavior: input.Behavior, UpdatedInput: input.UpdatedInput, Message: input.Message}); err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	if !s.needRuntime(w) {
		return
	}
	var capabilities Capabilities
	var err error
	if botID := r.URL.Query().Get("botId"); botID != "" {
		capabilities, err = s.runtime.CapabilitiesForBot(r.Context(), botID)
	} else {
		capabilities, err = s.runtime.Capabilities(r.Context())
	}
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, capabilities)
}

func (s *Server) goal(w http.ResponseWriter, r *http.Request) {
	if !s.needRuntime(w) {
		return
	}
	fields := make(map[string]any)
	if r.Method == http.MethodPut {
		if err := decodeJSON(w, r, &fields); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	method := map[string]string{http.MethodGet: "get", http.MethodPut: "set", http.MethodDelete: "clear"}[r.Method]
	baseline := s.store.Cursor()
	goal, err := s.runtime.Goal(r.Context(), r.PathValue("id"), method, fields)
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	snapshot, err := goalSnapshot(goal, baseline)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func goalSnapshot(goal any, baseline uint64) (map[string]any, error) {
	object, ok := goal.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("agent returned an invalid goal response")
	}
	snapshot := make(map[string]any, len(object)+1)
	for key, value := range object {
		snapshot[key] = value
	}
	if _, captured := snapshot["cursor"]; !captured {
		snapshot["cursor"] = baseline
	}
	return snapshot, nil
}

func (s *Server) internalTool(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	address := net.ParseIP(host)
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if address == nil || !address.IsLoopback() || s.config.InternalToken == "" || !sameSecret(provided, s.config.InternalToken) {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("internal authentication required"))
		return
	}
	if !s.needRuntime(w) {
		return
	}
	var input struct {
		BotID     string          `json:"botId"`
		CallID    string          `json:"callId"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.BotID == "" || input.Name == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("botId and tool name are required"))
		return
	}
	result, err := s.runtime.DynamicTool(r.Context(), input.BotID, input.Name, input.Arguments, input.CallID)
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) needRuntime(w http.ResponseWriter) bool {
	if s.runtime == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("agent runtime is unavailable"))
		return false
	}
	return true
}

func (s *Server) botChanged(id string) {
	if s.config.BotChanged != nil {
		go s.config.BotChanged(id)
	}
}

func (s *Server) mutateIdleBot(id string, mutate func() error) error {
	if s.runtime == nil {
		return mutate()
	}
	return s.runtime.WithIdleBot(id, mutate)
}

func editableBotField(field string) bool {
	switch field {
	case "name", "role", "avatar", "chief", "backend", "model", "effort", "serviceTier", "disabledSkills", "telegram":
		return true
	default:
		return false
	}
}

func eventCursor(r *http.Request) (uint64, error) {
	cursor := r.URL.Query().Get("after")
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		cursor = last
	}
	if cursor == "" {
		return 0, nil
	}
	after, err := strconv.ParseUint(cursor, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid event cursor")
	}
	return after, nil
}

func (s *Server) staticHandler() http.Handler {
	return newStaticHandler(s.config)
}

func newStaticHandler(config ServerConfig) http.Handler {
	var source fs.FS
	if config.StaticFS != nil {
		source = config.StaticFS
	} else if config.StaticDir != "" {
		source = os.DirFS(config.StaticDir)
	}
	if source == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	}
	files := http.FileServer(http.FS(source))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, fmt.Errorf("route not found"))
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if info, err := fs.Stat(source, name); err == nil && !info.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		// Only extensionless client routes receive the SPA document.
		if filepath.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		index, err := fs.ReadFile(source, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON request")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("request must contain one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func statusForError(err error) int {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, os.ErrNotExist):
		return http.StatusNotFound
	case errors.Is(err, ErrConflict), errors.Is(err, ErrBusy):
		return http.StatusConflict
	case errors.Is(err, ErrInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
