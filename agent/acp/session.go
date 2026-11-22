package acp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// toolInputCacheMaxEntries caps toolInputByID growth; beyond this we evict
// roughly half the map (iteration order is arbitrary) to bound memory.
const toolInputCacheMaxEntries = 1000

type acpSession struct {
	workDir string
	events  chan core.Event
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	alive   atomic.Bool

	cmd *exec.Cmd
	tr  *transport

	acpSessMu sync.RWMutex
	acpSessID string

	sendMu sync.Mutex

	permMu   sync.Mutex
	permByID map[string]permState

	toolInputMu   sync.Mutex
	toolInputByID map[string]string // toolCallId -> summarized tool input

	// pendingBackendTools tracks server-side tool calls (e.g. Grok WebSearch/XSearch)
	// whose initial tool_call carries only {"backend":true} and whose real query/url
	// arrives in the subsequent tool_call_update rawOutput.
	pendingBackendMu    sync.Mutex
	pendingBackendTools map[string]pendingBackendTool

	// thinkingMu buffers streamed agent_thought_chunk deltas so compact progress
	// cards receive complete thoughts/paragraphs instead of one line per few words.
	thinkingMu  sync.Mutex
	thinkingBuf strings.Builder

	// modesMu guards availableModes and currentMode. Both fields are
	// populated on handshake (session/new or session/load response) and
	// updated whenever SetLiveMode succeeds or the server announces a
	// mode change via session/update.
	modesMu        sync.RWMutex
	availableModes []acpModeInfo
	currentMode    string

	// modelsMu guards availableModels and currentModel.
	modelsMu        sync.RWMutex
	availableModels []acpModelInfo
	currentModel    string

	// supportsImage records AgentCapabilities.PromptCapabilities.Image from initialize.
	supportsImage atomic.Bool

	// contextMu guards contextUsage reported by session/prompt _meta.
	contextMu    sync.RWMutex
	contextUsage *core.ContextUsage

	// replaying is true while session/load is in flight so replayed
	// historical session/update notifications are not forwarded into IM.
	replaying atomic.Bool

	callbacks sessionCallbacks // may be nil (tests, integration harness)
}

var _ core.ContextUsageReporter = (*acpSession)(nil)

type permState struct {
	RPCID   json.RawMessage
	Options []permissionOption
}

type pendingBackendTool struct {
	Title string
	Kind  string
}

// acpSessionConfig bundles the inputs newACPSession needs. It's a
// struct rather than a long positional argument list because we keep
// adding optional knobs (initialMode, callbacks) and would otherwise
// break every call site each time.
type acpSessionConfig struct {
	command                string
	args                   []string
	extraEnv               []string
	workDir                string
	resumeSessionID        string
	authMethod             string
	initialMode            string           // if non-empty, applied via session/set_mode after session/new
	initialModel           string           // if non-empty, applied via session/set_model or session/set_config_option
	initialReasoningEffort string           // if non-empty, applied via session/set_config_option(reasoning_effort)
	callbacks              sessionCallbacks // may be nil
}

func newACPSession(ctx context.Context, cfg acpSessionConfig) (*acpSession, error) {
	absWorkDir, err := filepath.Abs(cfg.workDir)
	if err != nil {
		absWorkDir = cfg.workDir
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	s := &acpSession{
		workDir:       absWorkDir,
		events:        make(chan core.Event, 512),
		ctx:           sessionCtx,
		cancel:        cancel,
		permByID:            make(map[string]permState),
		toolInputByID:       make(map[string]string),
		pendingBackendTools: make(map[string]pendingBackendTool),
		acpSessID:           cfg.resumeSessionID,
		callbacks:     cfg.callbacks,
	}
	s.alive.Store(true)

	cmd := exec.CommandContext(sessionCtx, cfg.command, cfg.args...)
	cmd.Dir = absWorkDir
	cmd.Env = core.MergeEnv(os.Environ(), cfg.extraEnv)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("acp: stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(&stderrBuf, os.Stderr)

	s.cmd = cmd
	s.tr = newTransport(stdout, stdin, s.onNotification, s.onServerRequest)

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("acp: start %s: %w", cfg.command, err)
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.tr.readLoop(sessionCtx)
		waitErr := cmd.Wait()
		if waitErr != nil {
			msg := stderrBuf.String()
			if msg != "" {
				slog.Error("acp: process exited", "error", waitErr, "stderr", msg)
				s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("%s", strings.TrimSpace(msg))})
			} else {
				slog.Debug("acp: process exited", "error", waitErr)
			}
		}
		s.alive.Store(false)
	}()

	if err := s.handshake(cfg.resumeSessionID, cfg.authMethod); err != nil {
		_ = s.Close()
		return nil, err
	}

	// Apply the agent-level mode preference now that we have a session
	// id. If set_mode fails (e.g. modeId unknown to this backend) we
	// log and carry on with whatever mode the server defaulted to —
	// the alternative would be to reject the session entirely, which
	// is worse UX for a non-critical control.
	if strings.TrimSpace(cfg.initialMode) != "" {
		if ok := s.SetLiveMode(cfg.initialMode); !ok {
			slog.Warn("acp: initial mode could not be applied",
				"mode", cfg.initialMode,
				"session_id", s.currentACPSessionID(),
			)
		}
	}

	if strings.TrimSpace(cfg.initialModel) != "" {
		if ok := s.applyModel(cfg.initialModel); !ok {
			slog.Warn("acp: initial model could not be applied",
				"model", cfg.initialModel,
				"session_id", s.currentACPSessionID(),
			)
		}
	}

	if strings.TrimSpace(cfg.initialReasoningEffort) != "" {
		if ok := s.applyReasoningEffort(cfg.initialReasoningEffort); !ok {
			slog.Warn("acp: initial reasoning effort could not be applied",
				"effort", cfg.initialReasoningEffort,
				"session_id", s.currentACPSessionID(),
			)
		}
	}

	return s, nil
}

// handshake runs initialize → optional authenticate → session/load or
// session/new, and caches any modes the server advertises so
// SetLiveMode / PermissionModes can answer correctly.
func (s *acpSession) handshake(resumeSessionID string, authMethod string) error {
	initParams := map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{
				"readTextFile":  false,
				"writeTextFile": false,
			},
			"terminal": false,
		},
		"clientInfo": map[string]any{
			"name":    "cc-connect",
			"version": "1.0.0",
		},
	}
	res, err := s.tr.call(s.ctx, "initialize", initParams)
	if err != nil {
		return fmt.Errorf("acp: initialize: %w", err)
	}

	var initOut acpInitializeResult
	if err := json.Unmarshal(res, &initOut); err != nil {
		return fmt.Errorf("acp: parse initialize result: %w", err)
	}
	listSupported := len(initOut.AgentCapabilities.SessionCapabilities.List) > 0
	s.supportsImage.Store(initOut.AgentCapabilities.PromptCapabilities.Image)
	slog.Debug("acp: initialized",
		"protocol", initOut.ProtocolVersion,
		"load_session", initOut.AgentCapabilities.LoadSession,
		"list_sessions", listSupported,
		"prompt_image", initOut.AgentCapabilities.PromptCapabilities.Image,
	)
	if s.callbacks != nil {
		s.callbacks.reportListSupported(listSupported)
	}

	if strings.TrimSpace(authMethod) != "" {
		if _, err := s.tr.call(s.ctx, "authenticate", map[string]any{
			"methodId": authMethod,
		}); err != nil {
			return fmt.Errorf("acp: authenticate (%s): %w", authMethod, err)
		}
		slog.Debug("acp: authenticated", "method_id", authMethod)
	}

	wantResume := resumeSessionID != "" && resumeSessionID != core.ContinueSession
	if wantResume && initOut.AgentCapabilities.LoadSession {
		loadParams := map[string]any{
			"sessionId":  resumeSessionID,
			"cwd":        s.workDir,
			"mcpServers": []any{},
		}
		s.replaying.Store(true)
		loadRes, err := s.tr.call(s.ctx, "session/load", loadParams)
		s.replaying.Store(false)
		if err != nil {
			slog.Warn("acp: session/load failed, starting new session", "error", err)
		} else {
			var lr struct {
				SessionID string          `json:"sessionId"`
				Modes     *acpModesBlock  `json:"modes"`
				Models    *acpModelsBlock `json:"models"`
			}
			if json.Unmarshal(loadRes, &lr) == nil && lr.SessionID != "" {
				s.setACPSessionID(lr.SessionID)
				s.absorbModes(lr.Modes)
				s.absorbModels(lr.Models)
				return nil
			}
		}
	}

	newParams := map[string]any{
		"cwd":        s.workDir,
		"mcpServers": []any{},
	}
	newRes, err := s.tr.call(s.ctx, "session/new", newParams)
	if err != nil {
		return fmt.Errorf("acp: session/new: %w", err)
	}
	var sn struct {
		SessionID string          `json:"sessionId"`
		Modes     *acpModesBlock  `json:"modes"`
		Models    *acpModelsBlock `json:"models"`
	}
	if err := json.Unmarshal(newRes, &sn); err != nil {
		return fmt.Errorf("acp: parse session/new: %w", err)
	}
	if sn.SessionID == "" {
		return fmt.Errorf("acp: session/new: empty sessionId")
	}
	s.setACPSessionID(sn.SessionID)
	s.absorbModes(sn.Modes)
	s.absorbModels(sn.Models)
	return nil
}

func (s *acpSession) absorbModels(block *acpModelsBlock) {
	if block == nil || len(block.AvailableModels) == 0 {
		return
	}
	s.modelsMu.Lock()
	s.availableModels = append(s.availableModels[:0], block.AvailableModels...)
	if block.CurrentModelID != "" {
		s.currentModel = block.CurrentModelID
	}
	s.modelsMu.Unlock()
	if s.callbacks != nil {
		s.callbacks.reportModels(*block)
	}
}

func (s *acpSession) matchAvailableModel(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	lower := strings.ToLower(input)
	s.modelsMu.RLock()
	defer s.modelsMu.RUnlock()
	for _, m := range s.availableModels {
		if strings.ToLower(m.ModelID) == lower || strings.ToLower(m.Name) == lower {
			return m.ModelID
		}
	}
	return input
}

func (s *acpSession) applyModel(model string) bool {
	if !s.alive.Load() {
		return false
	}
	sid := s.currentACPSessionID()
	if sid == "" {
		return false
	}
	modelID := s.matchAvailableModel(model)
	if modelID == "" {
		return false
	}
	s.modelsMu.RLock()
	cur := s.currentModel
	s.modelsMu.RUnlock()
	if strings.EqualFold(cur, modelID) {
		return true
	}

	// Prefer session/set_model (ACP unstable models API, supported by agy acp).
	if _, err := s.tr.call(s.ctx, "session/set_model", map[string]any{
		"sessionId": sid,
		"modelId":   modelID,
	}); err == nil {
		s.recordAppliedModel(modelID)
		slog.Info("acp: model applied via session/set_model", "model", modelID, "session_id", sid)
		return true
	} else {
		slog.Debug("acp: session/set_model failed, trying session/set_config_option", "model", modelID, "error", err)
	}

	// Fallback: session/set_config_option (split optional -low/-medium/-high/-xhigh thought_level suffix).
	baseModel, thoughtLevel := splitModelThoughtLevel(modelID)
	if _, err := s.tr.call(s.ctx, "session/set_config_option", map[string]any{
		"sessionId": sid,
		"configId":  "model",
		"value":     baseModel,
	}); err != nil {
		slog.Warn("acp: session/set_config_option(model) failed", "model", baseModel, "session_id", sid, "error", err)
		return false
	}
	if thoughtLevel != "" {
		if _, err := s.tr.call(s.ctx, "session/set_config_option", map[string]any{
			"sessionId": sid,
			"configId":  "thought_level",
			"value":     thoughtLevel,
		}); err != nil {
			slog.Warn("acp: session/set_config_option(thought_level) failed", "level", thoughtLevel, "session_id", sid, "error", err)
		}
	}
	s.recordAppliedModel(modelID)
	slog.Info("acp: model applied via session/set_config_option", "model", baseModel, "thought_level", thoughtLevel, "session_id", sid)
	return true
}

func (s *acpSession) applyReasoningEffort(effort string) bool {
	if !s.alive.Load() {
		return false
	}
	sid := s.currentACPSessionID()
	effort = strings.TrimSpace(effort)
	if sid == "" || effort == "" {
		return false
	}
	if _, err := s.tr.call(s.ctx, "session/set_config_option", map[string]any{
		"sessionId": sid,
		"configId":  "reasoning_effort",
		"value":     effort,
	}); err != nil {
		slog.Debug("acp: session/set_config_option(reasoning_effort) failed",
			"effort", effort,
			"session_id", sid,
			"error", err,
		)
		return false
	}
	slog.Info("acp: reasoning effort applied via session/set_config_option", "effort", effort, "session_id", sid)
	return true
}

func (s *acpSession) recordAppliedModel(modelID string) {
	s.modelsMu.Lock()
	s.currentModel = modelID
	available := append([]acpModelInfo(nil), s.availableModels...)
	s.modelsMu.Unlock()
	if s.callbacks != nil {
		s.callbacks.reportModels(acpModelsBlock{
			CurrentModelID:  modelID,
			AvailableModels: available,
		})
	}
}

func splitModelThoughtLevel(modelID string) (string, string) {
	for _, suffix := range []string{"-xhigh", "-high", "-medium", "-low"} {
		if strings.HasSuffix(strings.ToLower(modelID), suffix) && len(modelID) > len(suffix) {
			return modelID[:len(modelID)-len(suffix)], strings.TrimPrefix(suffix, "-")
		}
	}
	return modelID, ""
}

// absorbModes copies a modes block into the session's cache and fans
// it out to the parent agent callbacks (if any). Both the session and
// the agent need the information: the session uses it to validate
// SetLiveMode inputs; the agent uses it to render `/mode` menus in IM.
func (s *acpSession) absorbModes(block *acpModesBlock) {
	if block == nil || len(block.AvailableModes) == 0 {
		return
	}
	s.modesMu.Lock()
	s.availableModes = append(s.availableModes[:0], block.AvailableModes...)
	if block.CurrentModeID != "" {
		s.currentMode = block.CurrentModeID
	}
	s.modesMu.Unlock()
	if s.callbacks != nil {
		s.callbacks.reportModes(*block)
	}
}

func (s *acpSession) setACPSessionID(id string) {
	s.acpSessMu.Lock()
	s.acpSessID = id
	s.acpSessMu.Unlock()
}

func (s *acpSession) currentACPSessionID() string {
	s.acpSessMu.RLock()
	defer s.acpSessMu.RUnlock()
	return s.acpSessID
}

// CurrentMode returns the ACP modeId most recently applied or reported
// for this session. Empty when the server never sent a modes block.
func (s *acpSession) CurrentMode() string {
	s.modesMu.RLock()
	defer s.modesMu.RUnlock()
	return s.currentMode
}

// SetLiveMode applies a permission mode change to the running session
// via `session/set_mode`. Returns true on success, false if the mode
// is unknown / the call errors / the session is closed.
//
// This is the implementation of core.LiveModeSwitcher for ACP
// sessions; the engine invokes it when the user runs `/mode <x>`,
// `/plan`, `/bypass`, etc. while a session is active.
//
// Client-side validation is important because at least one ACP server
// (devin acp in 2026.4.9) silently accepts unknown modeIds without
// any error, so a server-only check would let typos go undetected.
func (s *acpSession) SetLiveMode(mode string) bool {
	if !s.alive.Load() {
		return false
	}
	sid := s.currentACPSessionID()
	if sid == "" {
		return false
	}
	modeID := s.matchAvailableMode(mode)
	if modeID == "" {
		slog.Debug("acp: SetLiveMode rejected unknown mode",
			"mode", mode,
			"session_id", sid,
		)
		return false
	}
	if _, err := s.tr.call(s.ctx, "session/set_mode", map[string]any{
		"sessionId": sid,
		"modeId":    modeID,
	}); err != nil {
		slog.Warn("acp: session/set_mode failed",
			"mode", modeID,
			"session_id", sid,
			"error", err,
		)
		return false
	}
	s.modesMu.Lock()
	s.currentMode = modeID
	s.modesMu.Unlock()
	if s.callbacks != nil {
		// Re-publish current modeId so Agent.GetMode stays in sync.
		s.modesMu.RLock()
		available := append([]acpModeInfo(nil), s.availableModes...)
		s.modesMu.RUnlock()
		s.callbacks.reportModes(acpModesBlock{
			CurrentModeID:  modeID,
			AvailableModes: available,
		})
	}
	slog.Info("acp: live mode applied", "mode", modeID, "session_id", sid)
	return true
}

// matchAvailableMode resolves a user-typed mode string to a known ACP
// modeId from the cached availableModes list. Matching is case-
// insensitive on both id and display name to accommodate IM input.
// Returns "" if nothing matches or if modes are unknown (first session
// hasn't handshaked yet).
func (s *acpSession) matchAvailableMode(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	lower := strings.ToLower(input)
	s.modesMu.RLock()
	defer s.modesMu.RUnlock()
	for _, m := range s.availableModes {
		if strings.ToLower(m.ID) == lower || strings.ToLower(m.Name) == lower {
			return m.ID
		}
	}
	return ""
}

func (s *acpSession) onNotification(method string, params json.RawMessage) {
	if method != "session/update" {
		slog.Debug("acp: notification", "method", method)
		return
	}
	s.maybeAbsorbCurrentModeUpdate(params)
	if s.replaying.Load() {
		slog.Debug("acp: suppressing replayed session/update during session/load")
		return
	}
	s.cacheToolCallInput(params)
	sid := s.currentACPSessionID()
	// Debug log to capture raw session/update JSON for troubleshooting vendor compatibility
	slog.Debug("acp: session/update", "session_id", sid, "params", string(params))
	if handled, evs := s.handleDeferredBackendToolUpdate(sid, params); handled {
		if len(evs) > 0 {
			s.flushThinking(sid)
			for _, ev := range evs {
				s.emit(ev)
			}
		}
		return
	}
	for _, ev := range mapSessionUpdate(sid, params) {
		if ev.Type == core.EventThinking {
			s.appendThinkingChunk(sid, ev.Content)
			continue
		}
		s.flushThinking(sid)
		s.emit(ev)
	}
}

func (s *acpSession) appendThinkingChunk(sessionID string, chunk string) {
	if chunk == "" {
		return
	}
	var toEmit []string
	s.thinkingMu.Lock()
	s.thinkingBuf.WriteString(chunk)
	full := s.thinkingBuf.String()
	if idx := strings.LastIndex(full, "\n\n"); idx >= 0 {
		head := full[:idx]
		tail := full[idx+2:]
		s.thinkingBuf.Reset()
		s.thinkingBuf.WriteString(tail)
		for _, para := range strings.Split(head, "\n\n") {
			if p := strings.TrimSpace(para); p != "" {
				toEmit = append(toEmit, p)
			}
		}
	}
	s.thinkingMu.Unlock()
	for _, p := range toEmit {
		s.emit(core.Event{
			Type:      core.EventThinking,
			Content:   p,
			SessionID: sessionID,
		})
	}
}

func (s *acpSession) flushThinking(sessionID string) {
	s.thinkingMu.Lock()
	if s.thinkingBuf.Len() == 0 {
		s.thinkingMu.Unlock()
		return
	}
	text := strings.TrimSpace(s.thinkingBuf.String())
	s.thinkingBuf.Reset()
	s.thinkingMu.Unlock()
	if text == "" {
		return
	}
	s.emit(core.Event{
		Type:      core.EventThinking,
		Content:   text,
		SessionID: sessionID,
	})
}

func isDeferredBackendToolInput(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	backend, ok := m["backend"].(bool)
	if !ok || !backend {
		return false
	}
	return summarizeACPToolInput("", raw) == ""
}

func normalizeBackendToolName(rawName, title string) string {
	n := strings.ToLower(strings.TrimSpace(rawName))
	switch n {
	case "x_keyword_search", "x_semantic_search":
		return "x_search"
	case "x_user_search", "x_thread_fetch", "open_page", "web_search":
		return n
	case "open_page_with_find":
		return "open_page"
	case "search":
		return "web_search"
	}
	t := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(title), ":"))
	switch {
	case strings.Contains(t, "x search"):
		return "x_search"
	case strings.Contains(t, "web search"):
		return "web_search"
	case t != "":
		return t
	default:
		return "search"
	}
}

func extractBackendToolDetails(fallbackTitle string, rawOutput json.RawMessage) (string, string) {
	if len(rawOutput) == 0 {
		return normalizeBackendToolName("", fallbackTitle), ""
	}
	var ro struct {
		Name   string          `json:"name"`
		Input  json.RawMessage `json:"input"`
		Action *struct {
			Type    string `json:"type"`
			Query   string `json:"query"`
			URL     string `json:"url"`
			Pattern string `json:"pattern"`
		} `json:"action"`
	}
	if json.Unmarshal(rawOutput, &ro) != nil {
		return normalizeBackendToolName("", fallbackTitle), ""
	}
	if ro.Action != nil {
		name := normalizeBackendToolName(ro.Action.Type, fallbackTitle)
		switch {
		case ro.Action.Query != "":
			if ro.Action.URL != "" {
				return name, ro.Action.URL + " (" + ro.Action.Query + ")"
			}
			return name, ro.Action.Query
		case ro.Action.URL != "":
			if ro.Action.Pattern != "" {
				return name, ro.Action.URL + " (" + ro.Action.Pattern + ")"
			}
			return name, ro.Action.URL
		}
		return name, ""
	}
	name := normalizeBackendToolName(ro.Name, fallbackTitle)
	if len(ro.Input) > 0 {
		var inMap map[string]any
		var inStr string
		if json.Unmarshal(ro.Input, &inStr) == nil && strings.TrimSpace(inStr) != "" {
			_ = json.Unmarshal([]byte(inStr), &inMap)
		} else {
			_ = json.Unmarshal(ro.Input, &inMap)
		}
		if len(inMap) > 0 {
			for _, k := range []string{"query", "q", "url", "username", "handle", "screen_name", "tweet_id", "post_id", "thread_id", "id"} {
				if v, ok := inMap[k].(string); ok && strings.TrimSpace(v) != "" {
					return name, strings.TrimSpace(v)
				}
			}
		}
	}
	return name, ""
}

// handleDeferredBackendToolUpdate defers Grok server-side tool_call notifications
// (WebSearch/XSearch) that arrive without arguments until the matching
// tool_call_update provides the real query/url in rawOutput.
func (s *acpSession) handleDeferredBackendToolUpdate(sessionID string, params json.RawMessage) (bool, []core.Event) {
	var wrap struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil || len(wrap.Update) == 0 {
		return false, nil
	}
	sid := wrap.SessionID
	if sid == "" {
		sid = sessionID
	}
	var head struct {
		SessionUpdate string          `json:"sessionUpdate"`
		ToolCallID    string          `json:"toolCallId"`
		Title         string          `json:"title"`
		Kind          string          `json:"kind"`
		Status        string          `json:"status"`
		RawInput      json.RawMessage `json:"rawInput"`
		RawOutput     json.RawMessage `json:"rawOutput"`
	}
	if json.Unmarshal(wrap.Update, &head) != nil || head.ToolCallID == "" {
		return false, nil
	}
	switch head.SessionUpdate {
	case "tool_call":
		if !isDeferredBackendToolInput(head.RawInput) {
			return false, nil
		}
		s.pendingBackendMu.Lock()
		if s.pendingBackendTools == nil {
			s.pendingBackendTools = make(map[string]pendingBackendTool)
		}
		s.pendingBackendTools[head.ToolCallID] = pendingBackendTool{
			Title: head.Title,
			Kind:  head.Kind,
		}
		s.pendingBackendMu.Unlock()
		return true, nil
	case "tool_call_update":
		s.pendingBackendMu.Lock()
		pending, ok := s.pendingBackendTools[head.ToolCallID]
		if ok && (len(head.RawOutput) > 0 || strings.EqualFold(head.Status, "completed") || strings.EqualFold(head.Status, "failed")) {
			delete(s.pendingBackendTools, head.ToolCallID)
		}
		s.pendingBackendMu.Unlock()
		if !ok {
			return false, nil
		}
		if len(head.RawOutput) == 0 && !strings.EqualFold(head.Status, "completed") && !strings.EqualFold(head.Status, "failed") {
			return true, nil
		}
		title := head.Title
		if title == "" {
			title = pending.Title
		}
		toolName, toolInput := extractBackendToolDetails(title, head.RawOutput)
		return true, []core.Event{{
			Type:      core.EventToolUse,
			ToolName:  toolName,
			ToolInput: toolInput,
			SessionID: sid,
		}}
	}
	return false, nil
}

// maybeAbsorbCurrentModeUpdate watches session/update notifications
// for `current_mode_update` (server-driven mode switch, e.g. when the
// user toggles modes via the Windsurf/IDE UI while cc-connect is
// connected). Keeping currentMode in sync here means the IM `/mode`
// indicator reflects the true server state rather than the last
// client-initiated value.
func (s *acpSession) maybeAbsorbCurrentModeUpdate(params json.RawMessage) {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil || len(wrap.Update) == 0 {
		return
	}
	var head struct {
		Kind          string `json:"sessionUpdate"`
		CurrentModeID string `json:"currentModeId"`
	}
	if json.Unmarshal(wrap.Update, &head) != nil {
		return
	}
	if head.Kind != "current_mode_update" || head.CurrentModeID == "" {
		return
	}
	s.modesMu.Lock()
	s.currentMode = head.CurrentModeID
	available := append([]acpModeInfo(nil), s.availableModes...)
	s.modesMu.Unlock()
	if s.callbacks != nil {
		s.callbacks.reportModes(acpModesBlock{
			CurrentModeID:  head.CurrentModeID,
			AvailableModes: available,
		})
	}
}

// cacheToolCallInput extracts and caches rawInput from tool_call and tool_call_update
// session updates so that handlePermissionRequest can look it up by toolCallId.
// OpenCode ACP bug (#7370): rawInput is empty in tool_call and request_permission,
// but populated in tool_call_update. We cache from both sources.
func (s *acpSession) evictToolInputCacheIfNeededLocked() {
	if len(s.toolInputByID) < toolInputCacheMaxEntries {
		return
	}
	target := toolInputCacheMaxEntries / 2
	for k := range s.toolInputByID {
		if len(s.toolInputByID) <= target {
			break
		}
		delete(s.toolInputByID, k)
	}
}

func (s *acpSession) cacheToolCallInput(params json.RawMessage) {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil || len(wrap.Update) == 0 {
		return
	}
	var head struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if json.Unmarshal(wrap.Update, &head) != nil {
		return
	}
	switch head.SessionUpdate {
	case "tool_call":
		var tc struct {
			ToolCallID string          `json:"toolCallId"`
			Kind       string          `json:"kind"`
			RawInput   json.RawMessage `json:"rawInput"`
		}
		if json.Unmarshal(wrap.Update, &tc) != nil || tc.ToolCallID == "" || len(tc.RawInput) == 0 {
			return
		}
		s.toolInputMu.Lock()
		s.evictToolInputCacheIfNeededLocked()
		input := summarizeACPToolInput(tc.Kind, tc.RawInput)
		s.toolInputByID[tc.ToolCallID] = input
		s.toolInputMu.Unlock()
		slog.Info("acp: cached tool_call input", "toolCallId", tc.ToolCallID, "kind", tc.Kind, "input", input)
	case "tool_call_update":
		var tc struct {
			ToolCallID string          `json:"toolCallId"`
			RawInput   json.RawMessage `json:"rawInput"`
		}
		if json.Unmarshal(wrap.Update, &tc) != nil || tc.ToolCallID == "" || len(tc.RawInput) == 0 {
			return
		}
		input := summarizeACPToolInput("", tc.RawInput)
		if input == "" {
			return
		}
		s.toolInputMu.Lock()
		s.evictToolInputCacheIfNeededLocked()
		s.toolInputByID[tc.ToolCallID] = input
		s.toolInputMu.Unlock()
		slog.Info("acp: cached tool_call_update input", "toolCallId", tc.ToolCallID, "input", input)
	}
}

func (s *acpSession) onServerRequest(method string, id json.RawMessage, params json.RawMessage) {
	switch method {
	case "session/request_permission":
		s.handlePermissionRequest(id, params)
	case "cursor/ask_question", "cursor/create_plan", "cursor/update_todos", "cursor/task", "cursor/generate_image":
		// Cursor CLI extensions — acknowledge so tool flows do not block; IM UX is limited for these.
		slog.Debug("acp: cursor extension request (no-op ack)", "method", method)
		_ = s.tr.respondSuccess(id, map[string]any{})
	default:
		if strings.HasPrefix(method, "cursor/") {
			slog.Debug("acp: unknown cursor extension, ack empty", "method", method)
			_ = s.tr.respondSuccess(id, map[string]any{})
			return
		}
		slog.Info("acp: unhandled server request", "method", method)
		_ = s.tr.respondError(id, -32601, "method not implemented")
	}
}

func (s *acpSession) handlePermissionRequest(id json.RawMessage, params json.RawMessage) {
	var p struct {
		SessionID string `json:"sessionId"`
		ToolCall  struct {
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Kind       string          `json:"kind"`
			RawInput   json.RawMessage `json:"rawInput"`
		} `json:"toolCall"`
		Options []permissionOption `json:"options"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		_ = s.tr.respondError(id, -32602, "invalid params")
		return
	}
	slog.Debug("acp: permission request raw params", "params", string(params))
	reqKey := jsonIDKey(id)
	toolName := p.ToolCall.Title
	if toolName == "" {
		toolName = p.ToolCall.Kind
	}
	if toolName == "" {
		toolName = "permission"
	}

	s.permMu.Lock()
	s.permByID[reqKey] = permState{RPCID: id, Options: p.Options}
	s.permMu.Unlock()

	rawTool := map[string]any{}
	_ = json.Unmarshal(params, &rawTool)

	// OpenCode ACP bug (#7370): rawInput in request_permission is always {},
	// but tool_call_update (which arrives right after) has the real input.
	// Emit in a goroutine so we don't block the read loop, and wait briefly
	// for tool_call_update to populate the cache.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for i := 0; i < 10; i++ {
			s.toolInputMu.Lock()
			toolInput := s.toolInputByID[p.ToolCall.ToolCallID]
			s.toolInputMu.Unlock()
			if toolInput != "" {
				break
			}
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		s.toolInputMu.Lock()
		toolInput := s.toolInputByID[p.ToolCall.ToolCallID]
		s.toolInputMu.Unlock()
		if toolInput == "" {
			toolInput = summarizeACPToolInput(p.ToolCall.Kind, p.ToolCall.RawInput)
		}
		if toolInput == "" {
			toolInput = p.ToolCall.Title
		}
		if toolInput == "" {
			toolInput = p.ToolCall.ToolCallID
		}

		slog.Info("acp: permission request", "request_id", reqKey, "tool", toolName, "input", toolInput)
		s.emit(core.Event{
			Type:         core.EventPermissionRequest,
			RequestID:    reqKey,
			ToolName:     toolName,
			ToolInput:    toolInput,
			ToolInputRaw: rawTool,
			SessionID:    s.currentACPSessionID(),
		})
	}()
}

func (s *acpSession) emit(ev core.Event) {
	if ev.SessionID == "" {
		ev.SessionID = s.currentACPSessionID()
	}
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}

func (s *acpSession) Send(prompt string, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("acp: session closed")
	}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	filePaths := core.SaveFilesToDisk(s.workDir, messageID, files)
	prompt = core.AppendFileRefs(prompt, filePaths)
	if len(images) > 0 {
		prompt = s.appendImageRefs(prompt, images)
	}

	sid := s.currentACPSessionID()
	if sid == "" {
		return fmt.Errorf("acp: no agent session id")
	}

	promptBlocks := []any{
		map[string]any{"type": "text", "text": prompt},
	}
	if s.supportsImage.Load() && len(images) > 0 {
		for _, img := range images {
			if len(img.Data) == 0 {
				continue
			}
			mime := strings.TrimSpace(img.MimeType)
			if mime == "" {
				mime = "image/png"
			}
			promptBlocks = append(promptBlocks, map[string]any{
				"type":     "image",
				"data":     base64.StdEncoding.EncodeToString(img.Data),
				"mimeType": mime,
			})
		}
	}
	params := map[string]any{
		"sessionId": sid,
		"prompt":    promptBlocks,
	}

	slog.Debug("acp: sending session/prompt", "session_id", sid, "prompt_len", len(prompt), "image_blocks", len(promptBlocks)-1)
	res, err := s.tr.call(s.ctx, "session/prompt", params)
	s.flushThinking(sid)
	s.pendingBackendMu.Lock()
	clear(s.pendingBackendTools)
	s.pendingBackendMu.Unlock()
	if err != nil {
		s.emit(core.Event{Type: core.EventError, Error: err})
		return fmt.Errorf("acp: session/prompt: %w", err)
	}
	slog.Debug("acp: session/prompt response", "session_id", sid, "response_len", len(res), "response", string(res))
	inputTokens, outputTokens := s.absorbPromptUsage(prompt, res)

	// Text was streamed via session/update; engine aggregates EventText.
	s.emit(core.Event{
		Type:         core.EventResult,
		SessionID:    sid,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Done:         true,
	})
	return nil
}

func (s *acpSession) currentContextWindow() int {
	s.modelsMu.RLock()
	defer s.modelsMu.RUnlock()
	for _, m := range s.availableModels {
		if m.Meta != nil && m.Meta.TotalContextTokens > 0 && strings.EqualFold(m.ModelID, s.currentModel) {
			return m.Meta.TotalContextTokens
		}
	}
	for _, m := range s.availableModels {
		if m.Meta != nil && m.Meta.TotalContextTokens > 0 {
			return m.Meta.TotalContextTokens
		}
	}
	return 0
}

func (s *acpSession) absorbPromptUsage(prompt string, res json.RawMessage) (int, int) {
	if len(res) == 0 {
		return 0, 0
	}
	var out struct {
		Meta *struct {
			ModelID             string `json:"modelId"`
			InputTokens         int    `json:"inputTokens"`
			OutputTokens        int    `json:"outputTokens"`
			TotalTokens         int    `json:"totalTokens"`
			CachedReadTokens    int    `json:"cachedReadTokens"`
			CacheCreationTokens int    `json:"cacheCreationTokens"`
		} `json:"_meta"`
	}
	if json.Unmarshal(res, &out) != nil || out.Meta == nil {
		return 0, 0
	}
	if out.Meta.ModelID != "" {
		s.modelsMu.Lock()
		if s.currentModel == "" || !strings.EqualFold(s.currentModel, out.Meta.ModelID) {
			s.currentModel = out.Meta.ModelID
		}
		s.modelsMu.Unlock()
	}
	// Grok ACP reports the current conversation context size in _meta.totalTokens
	// while _meta.inputTokens sums across all server-side tool iterations in the turn
	// (e.g. XSearch/WebSearch sub-calls). Prefer totalTokens when present.
	used := out.Meta.TotalTokens
	if used <= 0 {
		used = out.Meta.InputTokens
	}
	if used <= 0 {
		trimmed := strings.TrimSpace(prompt)
		if strings.EqualFold(trimmed, "/compact") || strings.EqualFold(trimmed, "/compress") {
			s.contextMu.Lock()
			s.contextUsage = nil
			s.contextMu.Unlock()
		}
		return 0, out.Meta.OutputTokens
	}
	usage := &core.ContextUsage{
		UsedTokens:               used,
		ContextWindow:            s.currentContextWindow(),
		InputTokens:              out.Meta.InputTokens,
		CachedInputTokens:        out.Meta.CachedReadTokens,
		CacheCreationInputTokens: out.Meta.CacheCreationTokens,
	}
	s.contextMu.Lock()
	s.contextUsage = usage
	s.contextMu.Unlock()
	return used, out.Meta.OutputTokens
}

func (s *acpSession) GetContextUsage() *core.ContextUsage {
	s.contextMu.RLock()
	defer s.contextMu.RUnlock()
	if s.contextUsage == nil {
		return nil
	}
	cp := *s.contextUsage
	return &cp
}

func (s *acpSession) appendImageRefs(prompt string, images []core.ImageAttachment) string {
	imgDir := filepath.Join(s.workDir, ".cc-connect", "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		slog.Warn("acp: mkdir images failed", "error", err)
		return prompt
	}
	var paths []string
	for i, img := range images {
		ext := ".bin"
		switch img.MimeType {
		case "image/jpeg":
			ext = ".jpg"
		case "image/gif":
			ext = ".gif"
		case "image/webp":
			ext = ".webp"
		case "image/png", "":
			ext = ".png"
		}
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(imgDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			slog.Error("acp: save image failed", "error", err)
			continue
		}
		paths = append(paths, fpath)
	}
	if len(paths) == 0 {
		return prompt
	}
	if prompt == "" {
		prompt = "User sent image(s)."
	}
	return prompt + "\n\n[attached image files: " + strings.Join(paths, ", ") + "]"
}

func (s *acpSession) RespondPermission(requestID string, result core.PermissionResult) error {
	if !s.alive.Load() {
		return fmt.Errorf("acp: session closed")
	}

	s.permMu.Lock()
	st, ok := s.permByID[requestID]
	if ok {
		delete(s.permByID, requestID)
	}
	s.permMu.Unlock()
	if !ok {
		return fmt.Errorf("acp: unknown permission request %q", requestID)
	}

	allow := strings.EqualFold(result.Behavior, "allow")
	optID := pickPermissionOptionID(allow, st.Options)
	if allow && optID == "" {
		slog.Warn("acp: allow requested but agent sent no options", "request_id", requestID)
		return s.tr.respondError(st.RPCID, -32603, "no permission options from agent")
	}
	res := buildPermissionResult(allow, optID)

	slog.Debug("acp: permission response", "request_id", requestID, "allow", allow, "option_id", optID)
	return s.tr.respondSuccess(st.RPCID, res)
}

func (s *acpSession) Events() <-chan core.Event {
	return s.events
}

func (s *acpSession) CurrentSessionID() string {
	return s.currentACPSessionID()
}

// CancelTurn sends an ACP session/cancel notification to abort the current
// turn while keeping the session alive. The agent should cancel the active
// LLM request and persist state, then wait for the next user message on the
// same connection. This is used by /stop instead of Close().
func (s *acpSession) CancelTurn() error {
	sid := s.currentACPSessionID()
	if sid == "" {
		return fmt.Errorf("acp: no active session to cancel")
	}
	if s.tr == nil {
		return fmt.Errorf("acp: transport not available")
	}
	slog.Debug("acp: cancelling current turn", "session_id", sid)
	return s.tr.sendNotification("session/cancel", map[string]any{
		"sessionId": sid,
	})
}

func (s *acpSession) Alive() bool {
	return s.alive.Load()
}

func (s *acpSession) Close() error {
	s.alive.Store(false)
	s.cancel()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		close(s.events)
	case <-time.After(8 * time.Second):
		slog.Warn("acp: close timed out waiting for I/O loop")
	}
	return nil
}

// summarizeACPToolInput extracts a human-readable summary from ACP tool rawInput.
func summarizeACPToolInput(kind string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return string(raw)
	}
	if len(m) == 0 {
		return ""
	}
	switch strings.ToLower(kind) {
	case "bash", "shell", "terminal", "execute":
		if cmd, ok := m["command"].(string); ok {
			if desc, ok := m["description"].(string); ok && desc != "" {
				return "# " + desc + "\n" + cmd
			}
			return cmd
		}
	case "read", "write", "edit":
		if fp, ok := m["file_path"].(string); ok {
			return fp
		}
		if fp, ok := m["path"].(string); ok {
			return fp
		}
	case "search":
		if q, ok := m["query"].(string); ok && q != "" {
			return q
		}
		if backend, ok := m["backend"].(bool); ok && backend {
			return ""
		}
	}
	// Fallback: try extracting command or query before formatting JSON.
	if cmd, ok := m["command"].(string); ok {
		if desc, ok := m["description"].(string); ok && desc != "" {
			return "# " + desc + "\n" + cmd
		}
		return cmd
	}
	if q, ok := m["query"].(string); ok && q != "" {
		return q
	}
	if pat, ok := m["pattern"].(string); ok && pat != "" {
		if p, ok := m["path"].(string); ok && p != "" {
			return pat + " in " + p
		}
		return pat
	}
	if backend, ok := m["backend"].(bool); ok && backend {
		return ""
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return string(b)
}
