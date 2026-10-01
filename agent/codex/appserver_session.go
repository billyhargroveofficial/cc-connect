package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type rpcResponseEnvelope struct {
	ID     any             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcNotificationEnvelope struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initResponse struct {
	ProtocolVersion string `json:"protocolVersion"`
}

type threadStartResponse struct {
	Cwd             string  `json:"cwd"`
	Model           string  `json:"model"`
	ReasoningEffort *string `json:"reasoningEffort"`
	ServiceTier     *string `json:"serviceTier"`
	Thread          struct {
		ID string `json:"id"`
	} `json:"thread"`
}

type threadResumeResponse struct {
	Cwd             string  `json:"cwd"`
	Model           string  `json:"model"`
	ReasoningEffort *string `json:"reasoningEffort"`
	ServiceTier     *string `json:"serviceTier"`
	Thread          struct {
		ID string `json:"id"`
	} `json:"thread"`
}

type turnStartResponse struct {
	Turn struct {
		ID string `json:"id"`
	} `json:"turn"`
}

type turnNotification struct {
	ThreadID string `json:"threadId"`
	Turn     struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"turn"`
}

type itemNotification struct {
	ThreadID string         `json:"threadId"`
	TurnID   string         `json:"turnId"`
	Item     map[string]any `json:"item"`
}

type errorNotification struct {
	Message  string `json:"message"`
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type appServerRateLimitsResponse struct {
	RateLimits          appServerRateLimitSnapshot            `json:"rateLimits"`
	RateLimitsByLimitID map[string]appServerRateLimitSnapshot `json:"rateLimitsByLimitId"`
}

type appServerRateLimitSnapshot struct {
	LimitID   string                    `json:"limitId"`
	LimitName string                    `json:"limitName"`
	PlanType  string                    `json:"planType"`
	Primary   *appServerRateLimitWindow `json:"primary"`
	Secondary *appServerRateLimitWindow `json:"secondary"`
	Credits   *appServerCreditsSnapshot `json:"credits"`
}

type appServerRateLimitWindow struct {
	UsedPercent        int   `json:"usedPercent"`
	WindowDurationMins int   `json:"windowDurationMins"`
	ResetsAt           int64 `json:"resetsAt"`
}

type appServerCreditsSnapshot struct {
	Balance    *string `json:"balance"`
	HasCredits bool    `json:"hasCredits"`
	Unlimited  bool    `json:"unlimited"`
}

type appServerRequestUserInputParams struct {
	ThreadID  string                              `json:"threadId"`
	TurnID    string                              `json:"turnId"`
	ItemID    string                              `json:"itemId"`
	Questions []appServerRequestUserInputQuestion `json:"questions"`
}

type appServerRequestUserInputQuestion struct {
	ID       string                            `json:"id"`
	Header   string                            `json:"header"`
	Question string                            `json:"question"`
	IsOther  bool                              `json:"isOther"`
	IsSecret bool                              `json:"isSecret"`
	Options  []appServerRequestUserInputOption `json:"options"`
}

type appServerRequestUserInputOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type appServerRequestUserInputResponse struct {
	Answers map[string]appServerRequestUserInputAnswer `json:"answers"`
}

type appServerRequestUserInputAnswer struct {
	Answers []string `json:"answers"`
}

type appServerSession struct {
	url                   string
	workDir               string
	model                 string
	effort                string
	serviceTier           string
	serviceTierConfigured bool
	mode                  string
	baseURL               string
	modelProvider         string
	extraEnv              []string
	codexHome             string
	promptPreamble        string
	attachedTransport     bool

	events chan core.Event

	ctx    context.Context
	cancel context.CancelFunc

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	procMu  sync.Mutex
	writeMu sync.Mutex

	nextID atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan rpcResponseEnvelope

	approvalsMu      sync.Mutex
	pendingApprovals map[string]chan core.PermissionResult

	threadID atomic.Value
	alive    atomic.Bool

	eventsMu     sync.Mutex
	eventsClosed bool
	wg           sync.WaitGroup

	stateMu      sync.Mutex
	pendingMsgs  []string
	currentTurn  string
	turnRevision uint64
	preambleSent bool

	runtimeMu sync.RWMutex
	usage     *core.UsageReport
	context   *core.ContextUsage

	native             appServerNativeOptions
	nativeMu           sync.Mutex
	nativeDescendants  map[string]struct{}
	nativeThreadStarts map[string]json.RawMessage
}

const (
	appServerRequestTimeout      = 120 * time.Second
	appServerUsageRefreshTimeout = 1500 * time.Millisecond
)

func newAppServerSession(ctx context.Context, url, workDir, model, effort, mode, resumeID, baseURL, modelProvider string, extraEnv []string, codexHome string, systemPrompt string, appendPrompt string, nativeOptions ...appServerNativeOptions) (*appServerSession, error) {
	workDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("codex app-server working directory: %w", err)
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	s := &appServerSession{
		url:              url,
		workDir:          workDir,
		model:            model,
		effort:           effort,
		mode:             mode,
		baseURL:          baseURL,
		modelProvider:    modelProvider,
		extraEnv:         append([]string(nil), extraEnv...),
		codexHome:        strings.TrimSpace(codexHome),
		promptPreamble:   buildCodexPromptPreamble(systemPrompt, appendPrompt),
		events:           make(chan core.Event, 128),
		ctx:              sessionCtx,
		cancel:           cancel,
		pending:          make(map[int64]chan rpcResponseEnvelope),
		pendingApprovals: make(map[string]chan core.PermissionResult),
		preambleSent:     resumeID != "" && resumeID != core.ContinueSession,
	}
	if len(nativeOptions) != 0 {
		s.native = nativeOptions[0]
		if s.native.serviceTier != nil {
			s.serviceTier, s.serviceTierConfigured = *s.native.serviceTier, true
		}
	}
	s.alive.Store(true)

	if err := s.connect(); err != nil {
		cancel()
		return nil, err
	}

	if err := s.initialize(); err != nil {
		_ = s.Close()
		return nil, err
	}

	if err := s.ensureThread(resumeID); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := s.refreshUsage(context.Background()); err != nil {
		slog.Debug("codex app-server: initial rate limit fetch failed", "error", err)
	}

	return s, nil
}

func (s *appServerSession) connect() error {
	if s.url == "managed://" || s.native.enabled && isExternalAppServerURL(s.url) {
		var reader io.Reader
		var writer io.WriteCloser
		var err error
		if s.url == "managed://" {
			reader, writer, err = connectManagedAppServer(s.ctx, s.codexHome)
		} else {
			reader, writer, err = connectExternalAppServer(s.ctx, s.url)
		}
		if err != nil {
			return fmt.Errorf("codex attached app-server: %w", err)
		}
		s.attachedTransport = true
		s.procMu.Lock()
		s.stdin = writer
		s.procMu.Unlock()
		slog.Info("codex app-server session connected", "transport", "attached", "work_dir", s.workDir)
		s.wg.Add(1)
		go s.readLoop(reader)
		return nil
	}

	args := []string{"app-server"}
	if strings.TrimSpace(s.url) != "" {
		args = append(args, "--listen", strings.TrimSpace(s.url))
	}
	if model := strings.TrimSpace(s.model); model != "" {
		args = append(args, "-c", fmt.Sprintf("model=%q", model))
	}
	if effort := strings.TrimSpace(s.effort); effort != "" {
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
	}
	if provider := strings.TrimSpace(s.modelProvider); provider != "" {
		args = append(args, "-c", fmt.Sprintf("model_provider=%q", provider))
	}
	if baseURL := strings.TrimSpace(s.baseURL); baseURL != "" {
		args = append(args, "-c", fmt.Sprintf("openai_base_url=%q", baseURL))
	}
	cmd := exec.CommandContext(s.ctx, "codex", args...)
	cmd.Dir = s.workDir
	env := append([]string(nil), s.extraEnv...)
	if s.codexHome != "" {
		env = append(env, "CODEX_HOME="+s.codexHome)
	}
	if len(env) > 0 {
		cmd.Env = core.MergeEnv(os.Environ(), env)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("codex app-server stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("codex app-server stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("codex app-server stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("codex app-server start: %w", err)
	}

	s.procMu.Lock()
	s.cmd = cmd
	s.stdin = stdin
	s.procMu.Unlock()

	slog.Info("codex app-server session started", "transport", "stdio", "pid", cmd.Process.Pid, "work_dir", s.workDir)

	s.wg.Add(3)
	go s.readLoop(stdout)
	go s.stderrLoop(stderr)
	go s.waitLoop()
	return nil
}

func (s *appServerSession) initialize() error {
	params := map[string]any{
		"clientInfo": map[string]any{
			"name":    "cc-connect-codex-agent",
			"title":   "CC Connect Codex Agent",
			"version": "0.1.0",
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
			"optOutNotificationMethods": []string{
				"command/exec/outputDelta",
				"item/agentMessage/delta",
				"item/plan/delta",
				"item/fileChange/outputDelta",
				"item/reasoning/summaryTextDelta",
				"item/reasoning/textDelta",
			},
		},
	}
	if s.native.enabled {
		// Rich clients consume typed item lifecycle events and deltas. The
		// legacy messaging bridge retains its existing non-streaming defaults.
		params["capabilities"] = map[string]any{"experimentalApi": true}
	}

	var resp initResponse
	if err := s.request("initialize", params, &resp); err != nil {
		return fmt.Errorf("codex app-server initialize: %w", err)
	}
	if err := s.notify("initialized", nil); err != nil {
		return fmt.Errorf("codex app-server initialized notify: %w", err)
	}
	return nil
}

func (s *appServerSession) ensureThread(resumeID string) error {
	if resumeID != "" && resumeID != core.ContinueSession {
		params := s.threadRequestParams()
		params["threadId"] = resumeID
		params["persistExtendedHistory"] = true
		params["excludeTurns"] = true

		var resp threadResumeResponse
		if err := s.request("thread/resume", params, &resp); err != nil {
			return err
		}
		if resp.Thread.ID == "" {
			return fmt.Errorf("codex app-server resume returned empty thread id")
		}
		s.applyThreadRuntimeState(resp.Cwd, resp.Model, resp.ReasoningEffort, resp.ServiceTier)
		s.threadID.Store(resp.Thread.ID)
		s.flushNativeThreadStart(resp.Thread.ID)
		if s.native.serviceTier != nil && *s.native.serviceTier == "" {
			// A resume with an omitted/null override can retain the saved tier.
			// The settings method explicitly distinguishes clearing from omission.
			if err := s.RPC(s.ctx, "thread/settings/update", map[string]any{"threadId": resp.Thread.ID, "serviceTier": nil}, nil); err != nil {
				return fmt.Errorf("codex app-server clear service tier: %w", err)
			}
		}
		slog.Info("codex app-server thread resumed", "thread_id", resp.Thread.ID)
		return nil
	}

	var resp threadStartResponse
	params := s.threadRequestParams()
	if len(s.native.tools) != 0 {
		params["dynamicTools"] = cloneAppServerTools(s.native.tools)
	}
	if err := s.request("thread/start", params, &resp); err != nil {
		return err
	}
	if resp.Thread.ID == "" {
		return fmt.Errorf("codex app-server start returned empty thread id")
	}
	s.applyThreadRuntimeState(resp.Cwd, resp.Model, resp.ReasoningEffort, resp.ServiceTier)
	s.threadID.Store(resp.Thread.ID)
	s.flushNativeThreadStart(resp.Thread.ID)
	slog.Info("codex app-server thread started", "thread_id", resp.Thread.ID)
	return nil
}

func (s *appServerSession) threadRequestParams() map[string]any {
	params := map[string]any{
		"experimentalRawEvents":  false,
		"persistExtendedHistory": false,
		"cwd":                    s.GetWorkDir(),
	}
	config := cloneAppServerMap(s.native.config)
	if effort := s.GetReasoningEffort(); effort != "" {
		if config == nil {
			config = make(map[string]any)
		}
		config["model_reasoning_effort"] = effort
	}
	if len(config) != 0 {
		params["config"] = config
	}
	if s.native.developerInstructions != "" {
		params["developerInstructions"] = s.native.developerInstructions
	}
	if s.modelProvider != "" {
		params["modelProvider"] = s.modelProvider
	}
	if model := s.GetModel(); model != "" {
		params["model"] = model
	}
	if tier, configured := s.getServiceTier(); configured {
		if tier == "" {
			params["serviceTier"] = nil
		} else {
			params["serviceTier"] = tier
		}
	}
	if approval, sandbox := appServerModeSettings(s.mode); approval != "" {
		params["approvalPolicy"] = approval
		if sandbox != "" {
			params["sandbox"] = sandbox
		}
	}
	return params
}

func appServerModeSettings(mode string) (approval string, sandbox string) {
	switch normalizeMode(mode) {
	case "auto-edit", "full-auto":
		return "never", "workspace-write"
	case "yolo":
		return "never", "danger-full-access"
	default:
		return "on-request", "read-only"
	}
}

func (s *appServerSession) applyThreadRuntimeState(workDir, model string, effort, serviceTier *string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if dir := strings.TrimSpace(workDir); dir != "" {
		s.workDir = dir
	}
	if m := strings.TrimSpace(model); m != "" {
		s.model = m
	}
	s.effort = normalizeRuntimeReasoningEffort(stringValue(effort))
	// Older servers may omit this field. Retain an explicit requested tier in
	// that case; a settings notification/RPC can still clear it with null.
	if serviceTier != nil {
		s.serviceTier = *serviceTier
	}
}

func (s *appServerSession) refreshUsage(ctx context.Context) error {
	timeout := appServerUsageRefreshTimeout
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			if until := time.Until(deadline); until > 0 && until < timeout {
				timeout = until
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if timeout <= 0 {
		return context.DeadlineExceeded
	}

	var resp appServerRateLimitsResponse
	if err := s.requestWithTimeout("account/rateLimits/read", map[string]any{}, &resp, timeout); err != nil {
		return err
	}
	s.storeUsage(mapAppServerRateLimits(resp))
	return nil
}

func (s *appServerSession) cachedUsage() *core.UsageReport {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return cloneUsageReport(s.usage)
}

func (s *appServerSession) cachedContextUsage() *core.ContextUsage {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return cloneContextUsage(s.context)
}

func (s *appServerSession) storeUsage(report *core.UsageReport) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.usage = cloneUsageReport(report)
}

func (s *appServerSession) storeContextUsage(usage *core.ContextUsage) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.context = cloneContextUsage(usage)
}

func (s *appServerSession) Send(prompt string, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(s.GetWorkDir(), messageID, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}

	prompt, imagePaths, err := s.stageImages(prompt, images)
	if err != nil {
		return err
	}

	s.stateMu.Lock()
	if !s.preambleSent {
		prompt = prependCodexPromptPreamble(prompt, s.promptPreamble)
		s.preambleSent = true
	}
	s.stateMu.Unlock()

	threadID := s.CurrentSessionID()
	if threadID == "" {
		return fmt.Errorf("codex app-server thread id is empty")
	}

	input := make([]map[string]any, 0, 1+len(imagePaths))
	input = append(input, map[string]any{
		"type":          "text",
		"text":          prompt,
		"text_elements": []any{},
	})
	for _, path := range imagePaths {
		input = append(input, map[string]any{
			"type": "localImage",
			"path": path,
		})
	}

	params := map[string]any{
		"threadId": threadID,
		"input":    input,
		"cwd":      s.GetWorkDir(),
	}
	if model := s.GetModel(); model != "" {
		params["model"] = model
	}
	if effort := s.GetReasoningEffort(); effort != "" {
		params["effort"] = effort
	}
	if tier, configured := s.getServiceTier(); configured && tier != "" {
		params["serviceTier"] = tier
	}
	if approval, _ := appServerModeSettings(s.mode); approval != "" {
		params["approvalPolicy"] = approval
	}

	var resp turnStartResponse
	s.stateMu.Lock()
	turnRevision := s.turnRevision
	s.stateMu.Unlock()
	if err := s.request("turn/start", params, &resp); err != nil {
		return fmt.Errorf("codex app-server turn/start: %w", err)
	}
	if resp.Turn.ID == "" {
		return fmt.Errorf("codex app-server turn/start returned empty turn id")
	}

	s.stateMu.Lock()
	if !s.native.enabled || s.turnRevision == turnRevision {
		// The reader can finish this turn and start a goal continuation before
		// this caller receives the response. Its lifecycle state is newer.
		s.currentTurn = resp.Turn.ID
		s.pendingMsgs = s.pendingMsgs[:0]
	}
	s.stateMu.Unlock()

	return nil
}

func (s *appServerSession) stageImages(prompt string, images []core.ImageAttachment) (string, []string, error) {
	if len(images) == 0 {
		return prompt, nil, nil
	}

	imgDir := filepath.Join(s.GetWorkDir(), ".cc-connect", "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("codex app-server: create image dir: %w", err)
	}

	imagePaths := make([]string, 0, len(images))
	for i, img := range images {
		ext := codexImageExt(img.MimeType)
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(imgDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			return "", nil, fmt.Errorf("codex app-server: save image: %w", err)
		}
		imagePaths = append(imagePaths, fpath)
	}

	if strings.TrimSpace(prompt) == "" {
		prompt = "Please analyze the attached image(s)."
	}

	return prompt, imagePaths, nil
}

func (s *appServerSession) RespondPermission(requestID string, result core.PermissionResult) error {
	s.approvalsMu.Lock()
	ch := s.pendingApprovals[requestID]
	if ch == nil {
		// Native JSON clients receive a decoded string ID; legacy renderers
		// retain the JSON-encoded ID (including its quotation marks).
		encoded, _ := json.Marshal(requestID)
		ch = s.pendingApprovals[string(encoded)]
	}
	s.approvalsMu.Unlock()
	if ch == nil {
		return fmt.Errorf("codex app-server: no pending approval for request %s", requestID)
	}
	select {
	case ch <- result:
	default:
	}
	return nil
}

func (s *appServerSession) handleServerRequest(probe map[string]json.RawMessage) {
	rawID := probe["id"]
	var method string
	if err := json.Unmarshal(probe["method"], &method); err != nil {
		return
	}
	params := probe["params"]
	s.observeNative(method, params, rawID)
	// The managed app-server can serve multiple threads over one process.
	// Only this session's thread may ask us to approve or answer a tool call.
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval",
		"item/permissions/requestApproval", "item/tool/requestUserInput", "item/tool/call":
		var scope struct {
			ThreadID string `json:"threadId"`
		}
		if err := json.Unmarshal(params, &scope); err != nil || !s.acceptsNativeThread(scope.ThreadID) {
			return
		}
	}

	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		s.handleApprovalRequest(rawID, method, params)
	case "item/permissions/requestApproval":
		s.handlePermissionsApproval(rawID, params)
	case "item/tool/requestUserInput":
		s.handleRequestUserInput(rawID, params)
	case "item/tool/call":
		s.handleDynamicToolCall(rawID, params)
	default:
		_ = s.writeJSON(map[string]any{
			"jsonrpc": "2.0", "id": rawID,
			"error": map[string]any{"code": -32601, "message": "method not found"},
		})
	}
}

func (s *appServerSession) handleApprovalRequest(rawID json.RawMessage, method string, paramsRaw json.RawMessage) {
	requestID := string(rawID)
	var params map[string]any
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		return
	}

	toolName, toolInput := method, appServerJSON(params)
	switch method {
	case "item/commandExecution/requestApproval":
		toolName = "Bash"
		if cmd, _ := params["command"].(string); cmd != "" {
			toolInput = cmd
			if cwd, _ := params["cwd"].(string); cwd != "" {
				toolInput += "\n(in " + cwd + ")"
			}
		}
	case "item/fileChange/requestApproval":
		toolName = "Patch"
		if reason, _ := params["reason"].(string); reason != "" {
			toolInput = reason
		}
	}

	ch := make(chan core.PermissionResult, 1)
	s.approvalsMu.Lock()
	s.pendingApprovals[requestID] = ch
	s.approvalsMu.Unlock()

	s.flushPendingAsThinking()
	s.emit(core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    requestID,
		ToolName:     toolName,
		ToolInput:    toolInput,
		ToolInputRaw: params,
	}, appServerParamsScope(params))

	go func() {
		timer := time.NewTimer(5 * time.Minute)
		defer timer.Stop()
		var result core.PermissionResult
		select {
		case result = <-ch:
		case <-s.ctx.Done():
			result = core.PermissionResult{Behavior: "deny"}
		case <-timer.C:
			result = core.PermissionResult{Behavior: "deny"}
		}
		s.approvalsMu.Lock()
		delete(s.pendingApprovals, requestID)
		s.approvalsMu.Unlock()

		decision := "decline"
		if strings.EqualFold(result.Behavior, "allow") {
			decision = "accept"
		}
		_ = s.writeJSON(map[string]any{
			"jsonrpc": "2.0", "id": rawID,
			"result": map[string]any{"decision": decision},
		})
	}()
}

func (s *appServerSession) handlePermissionsApproval(rawID json.RawMessage, paramsRaw json.RawMessage) {
	requestID := string(rawID)
	var params map[string]any
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		return
	}

	ch := make(chan core.PermissionResult, 1)
	s.approvalsMu.Lock()
	s.pendingApprovals[requestID] = ch
	s.approvalsMu.Unlock()

	s.flushPendingAsThinking()
	s.emit(core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    requestID,
		ToolName:     "Permissions",
		ToolInput:    appServerJSON(params),
		ToolInputRaw: params,
	}, appServerParamsScope(params))

	go func() {
		timer := time.NewTimer(5 * time.Minute)
		defer timer.Stop()
		var result core.PermissionResult
		select {
		case result = <-ch:
		case <-s.ctx.Done():
			result = core.PermissionResult{Behavior: "deny"}
		case <-timer.C:
			result = core.PermissionResult{Behavior: "deny"}
		}
		s.approvalsMu.Lock()
		delete(s.pendingApprovals, requestID)
		s.approvalsMu.Unlock()

		if strings.EqualFold(result.Behavior, "allow") {
			perms := params["permissions"]
			if perms == nil {
				perms = map[string]any{}
			}
			_ = s.writeJSON(map[string]any{
				"jsonrpc": "2.0", "id": rawID,
				"result": map[string]any{"permissions": perms, "scope": "turn"},
			})
		} else {
			_ = s.writeJSON(map[string]any{
				"jsonrpc": "2.0", "id": rawID,
				"result": map[string]any{"permissions": map[string]any{}},
			})
		}
	}()
}

func (s *appServerSession) handleRequestUserInput(rawID json.RawMessage, paramsRaw json.RawMessage) {
	requestID := string(rawID)
	var params appServerRequestUserInputParams
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		_ = s.writeJSON(map[string]any{
			"jsonrpc": "2.0", "id": rawID,
			"error": map[string]any{"code": -32602, "message": "invalid params"},
		})
		return
	}

	questions := appServerRequestUserInputQuestions(params.Questions)
	if len(questions) == 0 {
		_ = s.writeJSON(map[string]any{
			"jsonrpc": "2.0", "id": rawID,
			"result": appServerRequestUserInputResponse{Answers: map[string]appServerRequestUserInputAnswer{}},
		})
		return
	}

	rawInput := appServerRequestUserInputRawInput(params)
	ch := make(chan core.PermissionResult, 1)
	s.approvalsMu.Lock()
	s.pendingApprovals[requestID] = ch
	s.approvalsMu.Unlock()

	s.flushPendingAsThinking()
	s.emit(core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    requestID,
		ToolName:     "AskUserQuestion",
		ToolInput:    appServerJSON(rawInput),
		ToolInputRaw: rawInput,
		Questions:    questions,
	}, appServerEventScope{threadID: params.ThreadID, turnID: params.TurnID})

	go func() {
		timer := time.NewTimer(5 * time.Minute)
		defer timer.Stop()
		var result core.PermissionResult
		select {
		case result = <-ch:
		case <-s.ctx.Done():
			result = core.PermissionResult{Behavior: "deny"}
		case <-timer.C:
			result = core.PermissionResult{Behavior: "deny"}
		}
		s.approvalsMu.Lock()
		delete(s.pendingApprovals, requestID)
		s.approvalsMu.Unlock()

		response := appServerRequestUserInputResponseFromResult(params.Questions, result)
		_ = s.writeJSON(map[string]any{
			"jsonrpc": "2.0", "id": rawID,
			"result": response,
		})
	}()
}

func (s *appServerSession) handleDynamicToolCall(rawID json.RawMessage, paramsRaw json.RawMessage) {
	if s.native.toolHandler != nil {
		s.runDynamicTool(rawID, paramsRaw)
		return
	}
	_ = s.writeJSON(map[string]any{
		"jsonrpc": "2.0", "id": rawID,
		"result": map[string]any{
			"success":      false,
			"contentItems": []map[string]any{{"type": "inputText", "text": "tool not available on this client"}},
		},
	})
}

func appServerRequestUserInputQuestions(input []appServerRequestUserInputQuestion) []core.UserQuestion {
	questions := make([]core.UserQuestion, 0, len(input))
	for _, in := range input {
		questionText := strings.TrimSpace(in.Question)
		if questionText == "" {
			continue
		}
		q := core.UserQuestion{
			Question: questionText,
			Header:   strings.TrimSpace(in.Header),
		}
		for _, opt := range in.Options {
			q.Options = append(q.Options, core.UserQuestionOption{
				Label:       strings.TrimSpace(opt.Label),
				Description: strings.TrimSpace(opt.Description),
			})
		}
		questions = append(questions, q)
	}
	return questions
}

func appServerRequestUserInputRawInput(params appServerRequestUserInputParams) map[string]any {
	questions := make([]any, 0, len(params.Questions))
	for _, in := range params.Questions {
		q := map[string]any{
			"id":       in.ID,
			"header":   in.Header,
			"question": in.Question,
			"isOther":  in.IsOther,
			"isSecret": in.IsSecret,
			"options":  appServerRequestUserInputRawOptions(in.Options),
		}
		questions = append(questions, q)
	}
	return map[string]any{
		"threadId":  params.ThreadID,
		"turnId":    params.TurnID,
		"itemId":    params.ItemID,
		"questions": questions,
	}
}

func appServerRequestUserInputRawOptions(options []appServerRequestUserInputOption) []any {
	out := make([]any, 0, len(options))
	for _, opt := range options {
		out = append(out, map[string]any{
			"label":       opt.Label,
			"description": opt.Description,
		})
	}
	return out
}

func appServerRequestUserInputResponseFromResult(questions []appServerRequestUserInputQuestion, result core.PermissionResult) appServerRequestUserInputResponse {
	response := appServerRequestUserInputResponse{Answers: map[string]appServerRequestUserInputAnswer{}}
	if !strings.EqualFold(result.Behavior, "allow") {
		return response
	}

	answersRaw, _ := result.UpdatedInput["answers"].(map[string]any)
	if len(answersRaw) == 0 {
		return response
	}

	for _, q := range questions {
		id := strings.TrimSpace(q.ID)
		text := strings.TrimSpace(q.Question)
		if id == "" || text == "" {
			continue
		}
		values := appServerRequestUserInputAnswerValues(answersRaw[text])
		if len(values) == 0 {
			continue
		}
		response.Answers[id] = appServerRequestUserInputAnswer{Answers: values}
	}
	return response
}

func appServerRequestUserInputAnswerValues(raw any) []string {
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []string:
		values := make([]string, 0, len(v))
		for _, s := range v {
			if strings.TrimSpace(s) != "" {
				values = append(values, s)
			}
		}
		return values
	case []any:
		values := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				values = append(values, s)
			}
		}
		return values
	case map[string]any:
		return appServerRequestUserInputAnswerValues(v["answers"])
	case appServerRequestUserInputAnswer:
		return appServerRequestUserInputAnswerValues(v.Answers)
	default:
		return nil
	}
}

func (s *appServerSession) rejectPendingApprovals(err error) {
	s.approvalsMu.Lock()
	defer s.approvalsMu.Unlock()
	for id, ch := range s.pendingApprovals {
		delete(s.pendingApprovals, id)
		select {
		case ch <- core.PermissionResult{Behavior: "deny"}:
		default:
		}
	}
}

func (s *appServerSession) Events() <-chan core.Event {
	return s.events
}

func (s *appServerSession) CurrentSessionID() string {
	v, _ := s.threadID.Load().(string)
	return v
}

func (s *appServerSession) GetWorkDir() string {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.workDir
}

func (s *appServerSession) GetModel() string {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return strings.TrimSpace(s.model)
}

func (s *appServerSession) GetReasoningEffort() string {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return strings.TrimSpace(s.effort)
}

func (s *appServerSession) getServiceTier() (string, bool) {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.serviceTier, s.serviceTierConfigured
}

func (s *appServerSession) GetUsage(ctx context.Context) (*core.UsageReport, error) {
	if err := s.refreshUsage(ctx); err != nil {
		if cached := s.cachedUsage(); cached != nil {
			return cached, nil
		}
		return nil, err
	}
	if cached := s.cachedUsage(); cached != nil {
		return cached, nil
	}
	return nil, fmt.Errorf("codex app-server usage unavailable")
}

func (s *appServerSession) GetContextUsage() *core.ContextUsage {
	return s.cachedContextUsage()
}

func (s *appServerSession) Alive() bool {
	return s.alive.Load()
}

func (s *appServerSession) Close() error {
	s.alive.Store(false)
	s.cancel()

	s.procMu.Lock()
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.procMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}

	s.closeEvents()
	return nil
}

func (s *appServerSession) readLoop(r io.Reader) {
	defer s.wg.Done()
	scanner := bufio.NewScanner(r)
	scanBuf := make([]byte, 0, 64*1024)
	const maxLineSize = 10 * 1024 * 1024 // 10MB
	scanner.Buffer(scanBuf, maxLineSize)

	for scanner.Scan() {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		data := scanner.Bytes()

		var probe map[string]json.RawMessage
		if err := json.Unmarshal(data, &probe); err != nil {
			slog.Debug("codex app-server: invalid JSON", "error", err)
			continue
		}

		_, hasID := probe["id"]
		_, hasMethod := probe["method"]

		switch {
		case hasID && !hasMethod:
			// Response to one of our requests.
			var resp rpcResponseEnvelope
			if err := json.Unmarshal(data, &resp); err != nil {
				slog.Debug("codex app-server: bad response envelope", "error", err)
				continue
			}
			s.handleResponse(resp)

		case hasID && hasMethod:
			// Server-initiated request that requires a response (e.g. approval).
			s.handleServerRequest(probe)

		default:
			// Notification (no id).
			var notif rpcNotificationEnvelope
			if err := json.Unmarshal(data, &notif); err != nil {
				slog.Debug("codex app-server: bad notification envelope", "error", err)
				continue
			}
			s.handleNotification(notif.Method, notif.Params)
		}
	}

	err := scanner.Err()
	if s.url == "managed://" || s.attachedTransport {
		s.managedTransportClosed(err)
		return
	}
	if err != nil {
		if s.ctx.Err() == nil && !errors.Is(err, io.EOF) {
			slog.Warn("codex app-server read failed", "error", err)
			if errors.Is(err, bufio.ErrTooLong) {
				s.emitError(fmt.Errorf("codex app-server line exceeds max size (%d bytes): %w", maxLineSize, err))
			} else {
				s.emitError(fmt.Errorf("codex app-server connection closed: %w", err))
			}
		}
		s.alive.Store(false)
		s.rejectPending(err)
		s.rejectPendingApprovals(err)
		return
	}

	s.alive.Store(false)
	s.rejectPending(io.EOF)
	s.rejectPendingApprovals(io.EOF)
}

// An attached app-server may restart while the chat is idle. End the local session
// quietly so the engine resumes its saved thread on the next user message.
func (s *appServerSession) managedTransportClosed(err error) {
	if err == nil {
		err = io.EOF
	}
	intentional := s.ctx.Err() != nil
	s.stateMu.Lock()
	turnActive := s.currentTurn != ""
	scope := appServerEventScope{threadID: s.CurrentSessionID(), turnID: s.currentTurn}
	s.currentTurn = ""
	s.turnRevision++
	s.pendingMsgs = nil
	s.stateMu.Unlock()

	s.alive.Store(false)
	if !intentional {
		if turnActive {
			slog.Warn("codex attached app-server disconnected during a turn", "error", err)
			s.emitError(fmt.Errorf("codex app-server connection closed: %w", err), scope)
		} else {
			slog.Info("codex attached app-server disconnected while idle; next message will resume the thread", "error", err)
		}
	}
	s.rejectPending(err)
	s.rejectPendingApprovals(err)
	s.procMu.Lock()
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	s.procMu.Unlock()
	s.cancel()
	s.closeEvents()
}

func (s *appServerSession) stderrLoop(r io.Reader) {
	defer s.wg.Done()
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		slog.Debug("codex app-server stderr", "line", line)
	}
	if err := scanner.Err(); err != nil && s.ctx.Err() == nil {
		slog.Debug("codex app-server stderr read failed", "error", err)
	}
}

func (s *appServerSession) waitLoop() {
	defer s.wg.Done()

	s.procMu.Lock()
	cmd := s.cmd
	s.procMu.Unlock()
	if cmd == nil {
		return
	}

	err := cmd.Wait()
	if s.ctx.Err() == nil && err != nil {
		slog.Warn("codex app-server exited unexpectedly", "error", err)
		s.emitError(fmt.Errorf("codex app-server exited: %w", err))
	}
	s.alive.Store(false)
	if err == nil {
		err = io.EOF
	}
	s.rejectPending(err)
}

func (s *appServerSession) handleResponse(resp rpcResponseEnvelope) {
	id, ok := rpcIDToInt64(resp.ID)
	if !ok {
		return
	}

	s.pendingMu.Lock()
	ch := s.pending[id]
	delete(s.pending, id)
	s.pendingMu.Unlock()

	if ch == nil {
		return
	}

	select {
	case ch <- resp:
	default:
	}
}

func (s *appServerSession) handleNotification(method string, paramsRaw json.RawMessage) {
	s.observeNative(method, paramsRaw, nil)
	switch method {
	case "thread/settings/updated":
		var notif struct {
			ThreadID       string `json:"threadId"`
			ThreadSettings struct {
				Cwd         string          `json:"cwd"`
				Model       string          `json:"model"`
				Effort      *string         `json:"effort"`
				ServiceTier json.RawMessage `json:"serviceTier"`
			} `json:"threadSettings"`
		}
		if err := json.Unmarshal(paramsRaw, &notif); err == nil && s.isCurrentThread(notif.ThreadID) {
			s.applyThreadRuntimeState(notif.ThreadSettings.Cwd, notif.ThreadSettings.Model, notif.ThreadSettings.Effort, nil)
			if len(notif.ThreadSettings.ServiceTier) != 0 {
				var tier *string
				if json.Unmarshal(notif.ThreadSettings.ServiceTier, &tier) == nil {
					s.runtimeMu.Lock()
					s.serviceTier = stringValue(tier)
					s.runtimeMu.Unlock()
				}
			}
		}
	case "turn/started":
		var notif turnNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil && s.isCurrentThread(notif.ThreadID) {
			s.stateMu.Lock()
			if s.currentTurn != "" && s.currentTurn != notif.Turn.ID {
				s.stateMu.Unlock()
				return
			}
			s.currentTurn = notif.Turn.ID
			s.turnRevision++
			s.pendingMsgs = s.pendingMsgs[:0]
			s.stateMu.Unlock()
			s.storeContextUsage(nil)
		}

	case "item/started":
		var notif itemNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil && s.isCurrentThread(notif.ThreadID) && s.isCurrentTurn(notif.TurnID) {
			s.handleItemStarted(notif.Item, appServerEventScope{threadID: notif.ThreadID, turnID: notif.TurnID})
		}

	case "item/completed":
		var notif itemNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil && s.isCurrentThread(notif.ThreadID) && s.isCurrentTurn(notif.TurnID) {
			s.handleItemCompleted(notif.Item, appServerEventScope{threadID: notif.ThreadID, turnID: notif.TurnID})
		}

	case "turn/completed":
		var notif turnNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil && s.isCurrentThread(notif.ThreadID) && s.isCurrentTurn(notif.Turn.ID) {
			if strings.EqualFold(strings.TrimSpace(notif.Turn.Status), "failed") || notif.Turn.Error != nil {
				errMsg := ""
				if notif.Turn.Error != nil {
					errMsg = strings.TrimSpace(notif.Turn.Error.Message)
				}
				if errMsg == "" {
					errMsg = "turn failed (no details)"
				}
				s.failTurn(fmt.Errorf("%s", errMsg), appServerEventScope{threadID: notif.ThreadID, turnID: notif.Turn.ID})
			} else {
				s.completeTurn(appServerEventScope{threadID: notif.ThreadID, turnID: notif.Turn.ID})
			}
		}

	case "thread/status/changed":
		var notif struct {
			ThreadID string `json:"threadId"`
			Status   struct {
				Type string `json:"type"`
			} `json:"status"`
		}
		if err := json.Unmarshal(paramsRaw, &notif); err == nil && s.isCurrentThread(notif.ThreadID) && notif.Status.Type == "idle" {
			// In codex 0.125+, thread going idle signals turn completion.
			s.completeTurn()
		}

	case "account/rateLimits/updated":
		var notif appServerRateLimitsResponse
		if err := json.Unmarshal(paramsRaw, &notif); err == nil {
			s.storeUsage(mapAppServerRateLimits(notif))
		}

	case "thread/tokenUsage/updated":
		var notif appServerThreadTokenUsageNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil && s.isCurrentThread(notif.ThreadID) {
			s.storeContextUsage(mapAppServerTokenUsage(notif))
		}

	case "error":
		var notif errorNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil && strings.TrimSpace(notif.Message) != "" &&
			(!s.native.enabled || s.isCurrentThread(notif.ThreadID)) {
			s.emitError(fmt.Errorf("%s", notif.Message), appServerEventScope{threadID: notif.ThreadID, turnID: notif.TurnID})
		}
	}
}

func (s *appServerSession) isCurrentThread(threadID string) bool {
	return threadID != "" && threadID == s.CurrentSessionID()
}

func (s *appServerSession) isCurrentTurn(turnID string) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return turnID != "" && turnID == s.currentTurn
}

func (s *appServerSession) handleItemStarted(item map[string]any, scopes ...appServerEventScope) {
	itemType, _ := item["type"].(string)
	if itemType == "" {
		return
	}

	switch itemType {
	case "agentMessage", "reasoning", "userMessage", "plan", "hookPrompt", "contextCompaction":
		return
	}

	s.flushPendingAsThinking(scopes...)

	switch itemType {
	case "commandExecution":
		command, _ := item["command"].(string)
		s.emit(core.Event{Type: core.EventToolUse, ToolName: "Bash", ToolInput: command, Metadata: s.toolItemMetadata(item)}, scopes...)

	case "mcpToolCall":
		server, _ := item["server"].(string)
		tool, _ := item["tool"].(string)
		name := strings.Trim(strings.Join([]string{server, tool}, ":"), ":")
		s.emit(core.Event{Type: core.EventToolUse, ToolName: "MCP", ToolInput: name + "\n" + appServerJSON(item["arguments"]), Metadata: s.toolItemMetadata(item)}, scopes...)

	case "webSearch":
		toolName, input := appServerWebToolDisplay(item)
		s.emit(core.Event{Type: core.EventToolUse, ToolName: toolName, ToolInput: input, Metadata: s.toolItemMetadata(item)}, scopes...)

	case "dynamicToolCall":
		tool, _ := item["tool"].(string)
		s.emit(core.Event{Type: core.EventToolUse, ToolName: tool, ToolInput: appServerJSON(item["arguments"]), Metadata: s.toolItemMetadata(item)}, scopes...)

	case "fileChange":
		s.emit(core.Event{Type: core.EventToolUse, ToolName: "Patch", ToolInput: appServerJSON(item["changes"]), Metadata: s.toolItemMetadata(item)}, scopes...)
	}
}

func (s *appServerSession) handleItemCompleted(item map[string]any, scopes ...appServerEventScope) {
	itemType, _ := item["type"].(string)
	if itemType == "" {
		return
	}

	switch itemType {
	case "reasoning":
		text := appServerReasoningText(item)
		if text != "" {
			s.emit(core.Event{Type: core.EventThinking, Content: text}, scopes...)
		}

	case "agentMessage":
		text, _ := item["text"].(string)
		if strings.TrimSpace(text) != "" {
			s.stateMu.Lock()
			s.pendingMsgs = append(s.pendingMsgs, text)
			s.stateMu.Unlock()
		}

	case "commandExecution":
		command, _ := item["command"].(string)
		status, _ := item["status"].(string)
		output, _ := item["aggregatedOutput"].(string)
		exitCode, hasExitCode := toInt(item["exitCode"])
		var exitCodePtr *int
		if hasExitCode {
			exitCodePtr = &exitCode
		}
		success := appServerToolSuccess(status, exitCodePtr)
		s.emit(core.Event{
			Type:         core.EventToolResult,
			ToolName:     "Bash",
			ToolInput:    command,
			ToolResult:   truncate(strings.TrimSpace(output), 500),
			ToolStatus:   strings.TrimSpace(status),
			ToolExitCode: exitCodePtr,
			ToolSuccess:  &success,
			Metadata:     s.toolItemMetadata(item),
		}, scopes...)

	case "mcpToolCall":
		tool, _ := item["tool"].(string)
		status, _ := item["status"].(string)
		result := appServerJSON(item["result"])
		if errText := appServerJSON(item["error"]); strings.TrimSpace(errText) != "" && result == "" {
			result = errText
		}
		success := appServerToolSuccess(status, nil)
		s.emit(core.Event{
			Type:        core.EventToolResult,
			ToolName:    tool,
			ToolResult:  truncate(strings.TrimSpace(result), 500),
			ToolStatus:  strings.TrimSpace(status),
			ToolSuccess: &success,
			Metadata:    s.toolItemMetadata(item),
		}, scopes...)

	case "webSearch":
		toolName, result := appServerWebToolDisplay(item)
		s.emit(core.Event{
			Type:       core.EventToolResult,
			ToolName:   toolName,
			ToolResult: truncate(strings.TrimSpace(result), 500),
			Metadata:   s.toolItemMetadata(item),
		}, scopes...)

	case "dynamicToolCall":
		tool, _ := item["tool"].(string)
		status, _ := item["status"].(string)
		result := appServerDynamicToolText(item["contentItems"])
		success := appServerToolSuccess(status, nil)
		s.emit(core.Event{
			Type:        core.EventToolResult,
			ToolName:    tool,
			ToolResult:  truncate(strings.TrimSpace(result), 500),
			ToolStatus:  strings.TrimSpace(status),
			ToolSuccess: &success,
			Metadata:    s.toolItemMetadata(item),
		}, scopes...)
	}
}

func appServerWebToolDisplay(item map[string]any) (string, string) {
	action, _ := item["action"].(map[string]any)
	// Codex app-server uses the webSearch item type for both searches and
	// follow-up page actions. Rendering every item as WebSearch produces a
	// misleading blank "websearch" row for openPage/findInPage/other actions.
	actionType, _ := action["type"].(string)
	switch strings.ToLower(strings.TrimSpace(actionType)) {
	case "openpage", "findinpage", "other":
		return "WebFetch", appServerWebFetchSummary(item, action)
	case "search":
		if query := appServerWebSearchQuery(item, action); query != "" {
			return "WebSearch", query
		}
		return "WebSearch", "search"
	}

	if query := appServerWebSearchQuery(item, action); query != "" {
		return "WebSearch", query
	}
	return "WebFetch", appServerWebFetchSummary(item, action)
}

func appServerWebSearchQuery(item, action map[string]any) string {
	for _, source := range []map[string]any{item, action} {
		if query, _ := source["query"].(string); strings.TrimSpace(query) != "" {
			return strings.TrimSpace(query)
		}
	}

	if rawQueries, ok := action["queries"].([]any); ok {
		queries := make([]string, 0, len(rawQueries))
		for _, raw := range rawQueries {
			if query, ok := raw.(string); ok && strings.TrimSpace(query) != "" {
				queries = append(queries, strings.TrimSpace(query))
			}
		}
		return strings.Join(queries, " | ")
	}

	return ""
}

func appServerWebFetchSummary(item, action map[string]any) string {
	pattern, _ := action["pattern"].(string)
	pattern = strings.TrimSpace(pattern)
	pageURL, _ := action["url"].(string)
	pageURL = appServerWebDisplayURL(pageURL)
	if pattern != "" && pageURL != "" {
		return pattern + " · " + pageURL
	}
	if pattern != "" {
		return pattern
	}
	if pageURL != "" {
		return pageURL
	}

	for _, source := range []map[string]any{item, action} {
		for _, key := range []string{"title", "refId", "ref_id"} {
			if value, _ := source[key].(string); strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}

	if results, ok := item["results"].([]any); ok {
		for _, raw := range results {
			result, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{"title", "refId", "ref_id"} {
				if value, _ := result[key].(string); strings.TrimSpace(value) != "" {
					return strings.TrimSpace(value)
				}
			}
		}
	}

	return "open page"
}

func appServerWebDisplayURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func appServerReasoningText(item map[string]any) string {
	var parts []string
	if summary, ok := item["summary"].([]any); ok {
		for _, entry := range summary {
			if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
	}
	if len(parts) == 0 {
		if content, ok := item["content"].([]any); ok {
			for _, entry := range content {
				if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

func appServerDynamicToolText(raw any) string {
	items, ok := raw.([]any)
	if !ok {
		return appServerJSON(raw)
	}
	var parts []string
	for _, entry := range items {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if text, _ := m["text"].(string); strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return appServerJSON(raw)
	}
	return strings.Join(parts, "\n")
}

func appServerToolSuccess(status string, exitCode *int) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	if exitCode != nil {
		return *exitCode == 0
	}
	return s == "completed" || s == "success" || s == "succeeded" || s == "ok"
}

func mapAppServerRateLimits(payload appServerRateLimitsResponse) *core.UsageReport {
	report := &core.UsageReport{Provider: "codex"}

	var snapshots []appServerRateLimitSnapshot
	if len(payload.RateLimitsByLimitID) > 0 {
		keys := make([]string, 0, len(payload.RateLimitsByLimitID))
		for key := range payload.RateLimitsByLimitID {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			snapshots = append(snapshots, payload.RateLimitsByLimitID[key])
		}
	} else if payload.RateLimits.LimitID != "" || payload.RateLimits.Primary != nil || payload.RateLimits.Secondary != nil || payload.RateLimits.Credits != nil {
		snapshots = append(snapshots, payload.RateLimits)
	}

	for _, snapshot := range snapshots {
		if report.Plan == "" && strings.TrimSpace(snapshot.PlanType) != "" {
			report.Plan = strings.TrimSpace(snapshot.PlanType)
		}
		if report.Credits == nil && snapshot.Credits != nil {
			report.Credits = &core.UsageCredits{
				HasCredits: snapshot.Credits.HasCredits,
				Unlimited:  snapshot.Credits.Unlimited,
			}
			if snapshot.Credits.Balance != nil {
				report.Credits.Balance = strings.TrimSpace(*snapshot.Credits.Balance)
			}
		}

		windows := appServerUsageWindows(snapshot)
		if len(windows) == 0 {
			continue
		}
		limitReached := false
		for _, window := range windows {
			if window.UsedPercent >= 100 {
				limitReached = true
				break
			}
		}

		report.Buckets = append(report.Buckets, core.UsageBucket{
			Name:         appServerBucketName(snapshot),
			Allowed:      !limitReached,
			LimitReached: limitReached,
			Windows:      windows,
		})
	}

	return report
}

func appServerBucketName(snapshot appServerRateLimitSnapshot) string {
	if name := strings.TrimSpace(snapshot.LimitName); name != "" {
		return name
	}
	if id := strings.TrimSpace(snapshot.LimitID); id != "" {
		return id
	}
	return "Rate limit"
}

func appServerUsageWindows(snapshot appServerRateLimitSnapshot) []core.UsageWindow {
	var windows []core.UsageWindow
	if snapshot.Primary != nil {
		windows = append(windows, appServerUsageWindow("Primary", snapshot.Primary))
	}
	if snapshot.Secondary != nil {
		windows = append(windows, appServerUsageWindow("Secondary", snapshot.Secondary))
	}
	return windows
}

func appServerUsageWindow(name string, window *appServerRateLimitWindow) core.UsageWindow {
	resetAfter := 0
	if window != nil && window.ResetsAt > 0 {
		resetAfter = int(time.Until(time.Unix(window.ResetsAt, 0)).Seconds())
		if resetAfter < 0 {
			resetAfter = 0
		}
	}
	return core.UsageWindow{
		Name:              name,
		UsedPercent:       window.UsedPercent,
		WindowSeconds:     window.WindowDurationMins * 60,
		ResetAfterSeconds: resetAfter,
		ResetAtUnix:       window.ResetsAt,
	}
}

func cloneUsageReport(report *core.UsageReport) *core.UsageReport {
	if report == nil {
		return nil
	}
	cloned := *report
	if len(report.Buckets) > 0 {
		cloned.Buckets = make([]core.UsageBucket, len(report.Buckets))
		for i, bucket := range report.Buckets {
			cloned.Buckets[i] = bucket
			if len(bucket.Windows) > 0 {
				cloned.Buckets[i].Windows = append([]core.UsageWindow(nil), bucket.Windows...)
			}
		}
	}
	if report.Credits != nil {
		credits := *report.Credits
		cloned.Credits = &credits
	}
	return &cloned
}

func normalizeRuntimeReasoningEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "med":
		return "medium"
	case "x-high", "very-high":
		return "xhigh"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func appServerJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "{}" || s == "[]" || s == `""` {
		return ""
	}
	return s
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return int(i), true
		}
	}
	return 0, false
}

func rpcIDToInt64(v any) (int64, bool) {
	switch id := v.(type) {
	case float64:
		return int64(id), true
	case int64:
		return id, true
	case int:
		return int64(id), true
	case json.Number:
		i, err := id.Int64()
		return i, err == nil
	}
	return 0, false
}

func (s *appServerSession) completeTurn(scopes ...appServerEventScope) {
	s.stateMu.Lock()
	if s.currentTurn == "" {
		s.stateMu.Unlock()
		return
	}
	scope := appServerEventScope{threadID: s.CurrentSessionID(), turnID: s.currentTurn}
	if len(scopes) != 0 {
		scope = scopes[0]
	}
	msgs := append([]string(nil), s.pendingMsgs...)
	s.pendingMsgs = s.pendingMsgs[:0]
	s.currentTurn = ""
	s.turnRevision++
	s.stateMu.Unlock()
	for _, text := range msgs {
		if strings.TrimSpace(text) != "" {
			s.emit(core.Event{Type: core.EventText, Content: text}, scope)
		}
	}
	s.emit(core.Event{Type: core.EventResult, SessionID: s.CurrentSessionID(), Done: true}, scope)
}

func (s *appServerSession) failTurn(err error, scopes ...appServerEventScope) {
	s.stateMu.Lock()
	if s.currentTurn == "" {
		s.stateMu.Unlock()
		return
	}
	scope := appServerEventScope{threadID: s.CurrentSessionID(), turnID: s.currentTurn}
	if len(scopes) != 0 {
		scope = scopes[0]
	}
	s.currentTurn = ""
	s.turnRevision++
	s.pendingMsgs = s.pendingMsgs[:0]
	s.stateMu.Unlock()
	s.emitError(err, scope)
	if s.native.enabled {
		// Native consumers retain errors until the terminal result settles the
		// turn. Retryable error notifications alone must not emit this result.
		s.emit(core.Event{Type: core.EventResult, SessionID: s.CurrentSessionID(), Done: true}, scope)
	}
}

func (s *appServerSession) flushPendingAsThinking(scopes ...appServerEventScope) {
	scope := s.eventScope(scopes)
	s.stateMu.Lock()
	msgs := append([]string(nil), s.pendingMsgs...)
	s.pendingMsgs = s.pendingMsgs[:0]
	s.stateMu.Unlock()

	for _, text := range msgs {
		if strings.TrimSpace(text) != "" {
			s.emit(core.Event{Type: core.EventThinking, Content: text}, scope)
		}
	}
}

func (s *appServerSession) flushPendingAsText(scopes ...appServerEventScope) {
	scope := s.eventScope(scopes)
	s.stateMu.Lock()
	msgs := append([]string(nil), s.pendingMsgs...)
	s.pendingMsgs = s.pendingMsgs[:0]
	s.stateMu.Unlock()

	for _, text := range msgs {
		if strings.TrimSpace(text) != "" {
			s.emit(core.Event{Type: core.EventText, Content: text}, scope)
		}
	}
}

func (s *appServerSession) emit(event core.Event, scopes ...appServerEventScope) {
	if s.native.enabled {
		scope := s.eventScope(scopes)
		if scope.threadID != "" || scope.turnID != "" {
			if event.Metadata == nil {
				event.Metadata = make(map[string]any, 2)
			}
			if scope.threadID != "" {
				event.Metadata["threadId"] = scope.threadID
			}
			if scope.turnID != "" {
				event.Metadata["turnId"] = scope.turnID
			}
		}
	}
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	if s.eventsClosed {
		return
	}
	select {
	case s.events <- event:
	default:
		slog.Warn("codex appserver: event channel full, dropping event", "type", event.Type)
	}
}

func (s *appServerSession) closeEvents() {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	if !s.eventsClosed && s.events != nil {
		close(s.events)
		s.eventsClosed = true
	}
}

func (s *appServerSession) emitError(err error, scopes ...appServerEventScope) {
	if err == nil {
		return
	}
	s.emit(core.Event{Type: core.EventError, Error: err}, scopes...)
}

func (s *appServerSession) rejectPending(err error) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	for id, ch := range s.pending {
		delete(s.pending, id)
		select {
		case ch <- rpcResponseEnvelope{ID: id, Error: &rpcError{Message: err.Error()}}:
		default:
		}
	}
}

func (s *appServerSession) request(method string, params any, out any) error {
	return s.requestWithTimeout(method, params, out, appServerRequestTimeout)
}

func (s *appServerSession) requestWithTimeout(method string, params any, out any, timeout time.Duration) error {
	return s.requestWithContext(context.Background(), method, params, out, timeout)
}

func (s *appServerSession) writeJSONWithTimeout(method string, v any, timeout time.Duration) error {
	return s.writeJSONWithContext(context.Background(), method, v, timeout)
}

func (s *appServerSession) contextDone() <-chan struct{} {
	if s.ctx == nil {
		return nil
	}
	return s.ctx.Done()
}

func (s *appServerSession) contextErr() error {
	if s.ctx == nil {
		return context.Canceled
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return context.Canceled
}

func (s *appServerSession) abortTransport() {
	s.alive.Store(false)
	if s.cancel != nil {
		s.cancel()
	}

	s.procMu.Lock()
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.procMu.Unlock()
}

func (s *appServerSession) notify(method string, params any) error {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		payload["params"] = params
	}
	return s.writeJSON(payload)
}

func (s *appServerSession) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("codex app-server encode: %w", err)
	}

	s.procMu.Lock()
	stdin := s.stdin
	s.procMu.Unlock()
	if stdin == nil {
		return fmt.Errorf("codex app-server connection is closed")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("codex app-server write: %w", err)
	}
	return nil
}
