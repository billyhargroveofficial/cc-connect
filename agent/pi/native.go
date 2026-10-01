package pi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

var (
	_ core.NativeEventObserver = (*Agent)(nil)
	_ core.AgentRPCSession     = (*piSession)(nil)
)

type piSessionOptions struct {
	nativeEvents  bool
	nativeHandler core.NativeEventHandler
	sessionDir    string
}

type piRPCResponse struct {
	data json.RawMessage
	err  error
}

func parseCLIArgs(value any) ([]string, error) {
	switch args := value.(type) {
	case nil:
		return nil, nil
	case []string:
		return append([]string(nil), args...), nil
	case []any:
		result := make([]string, len(args))
		for i, value := range args {
			arg, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("pi: cli_args[%d] must be a string", i)
			}
			result[i] = arg
		}
		return result, nil
	default:
		return nil, fmt.Errorf("pi: cli_args must be a list of strings")
	}
}

func sessionDirFromArgs(args []string) string {
	var dir string
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if args[i] == "--session-dir" && i+1 < len(args) {
			i++
			dir = args[i]
		} else if strings.HasPrefix(args[i], "--session-dir=") {
			dir = strings.TrimPrefix(args[i], "--session-dir=")
		}
	}
	return dir
}

func (a *Agent) sessionsDirectory() string {
	a.mu.Lock()
	dir, workDir := a.sessionDir, a.workDir
	a.mu.Unlock()
	if dir != "" {
		return dir
	}
	return piSessionDir(workDir)
}

// appendSessionArgs uses strict file lookup only for an explicitly configured
// session directory. The legacy --session-id behavior is unchanged otherwise.
func (s *piSession) appendSessionArgs(args []string, resumeID string) ([]string, error) {
	if s.sessionDir == "" {
		if resumeID != "" {
			args = append(args, "--session-id", resumeID)
		}
		return args, nil
	}
	if sessionDirFromArgs(args) == "" {
		args = append(args, "--session-dir", s.sessionDir)
	}
	if resumeID == "" {
		return args, nil
	}
	path, err := strictSessionFile(s.sessionDir, resumeID)
	if err != nil {
		return nil, err
	}
	return append(args, "--session", path), nil
}

func strictSessionFile(dir, id string) (string, error) {
	var path string
	switch {
	case id == core.ContinueSession:
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", fmt.Errorf("pi: read session directory: %w", err)
		}
		var latest time.Time
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			info, err := entry.Info()
			if err == nil && info.Mode().IsRegular() && (path == "" || info.ModTime().After(latest)) {
				path, latest = filepath.Join(dir, entry.Name()), info.ModTime()
			}
		}
	case filepath.IsAbs(id):
		path = id
	case strings.HasSuffix(id, ".jsonl"):
		path = filepath.Join(dir, id)
	default:
		path = findSessionFile(dir, id)
	}
	if path == "" {
		return "", fmt.Errorf("pi: session %q not found in configured session directory", id)
	}
	// Validate the real paths so a symlink cannot resume another bot's file.
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("pi: resolve session directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("pi: resolve session file: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("pi: resolve session directory: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("pi: resolve session file: %w", err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("pi: session file is outside configured session directory")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || filepath.Ext(resolved) != ".jsonl" {
		return "", fmt.Errorf("pi: session %q is not a regular session file", id)
	}
	return resolved, nil
}

func (s *piSession) observeNativeEvent(method string, raw map[string]any) {
	if s.nativeHandler == nil {
		return
	}
	params, err := json.Marshal(raw)
	if err != nil {
		slog.Warn("pi: encode native event", "method", method, "error", err)
		return
	}
	event := core.NativeEvent{
		Backend: "pi", Method: method, Params: params, Timestamp: time.Now().UTC(),
	}
	if raw["id"] != nil {
		event.RequestID, _ = json.Marshal(raw["id"])
	}
	s.nativeHandler(event)
}

func (s *piSession) sendNativePrompt(prompt string, images []core.ImageAttachment) error {
	params := map[string]any{"message": prompt}
	if len(images) > 0 {
		content := make([]map[string]any, 0, len(images))
		for _, image := range images {
			mime := image.MimeType
			if mime == "" {
				mime = http.DetectContentType(image.Data)
			}
			content = append(content, map[string]any{
				"type": "image", "mimeType": mime, "data": base64.StdEncoding.EncodeToString(image.Data),
			})
		}
		params["images"] = content
	}
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	return s.RPC(ctx, "prompt", params, nil)
}

// RPC sends a Pi command and waits only for its acknowledgement/response.
// Model work completes asynchronously through agent_settled, not this call.
func (s *piSession) RPC(ctx context.Context, method string, params any, result any) error {
	if !s.rpc {
		return fmt.Errorf("pi: RPC requires rpc mode")
	}
	if method == "" {
		return fmt.Errorf("pi: RPC method is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	command, err := piRPCParams(params)
	if err != nil {
		return err
	}
	if method == "extension_ui_response" {
		return s.sendExtensionUIResponse(command)
	}
	id := fmt.Sprintf("cc-connect-rpc-%d", s.rpcSeq.Add(1))
	command["id"], command["type"] = id, method
	response := make(chan piRPCResponse, 1)
	s.rpcMu.Lock()
	if !s.alive.Load() {
		s.rpcMu.Unlock()
		return fmt.Errorf("pi: session is closed")
	}
	if s.rpcPending == nil {
		s.rpcPending = make(map[string]chan piRPCResponse)
	}
	s.rpcPending[id] = response
	s.rpcMu.Unlock()
	defer func() {
		s.rpcMu.Lock()
		delete(s.rpcPending, id)
		s.rpcMu.Unlock()
	}()
	if err := s.writeRPCCommand(command); err != nil {
		return err
	}
	select {
	case reply := <-response:
		if reply.err != nil {
			return reply.err
		}
		if result != nil && len(reply.data) > 0 {
			if err := json.Unmarshal(reply.data, result); err != nil {
				return fmt.Errorf("pi: decode %s response: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return fmt.Errorf("pi: RPC %s: %w", method, s.ctx.Err())
	}
}

func piRPCParams(params any) (map[string]any, error) {
	command := make(map[string]any)
	if params == nil {
		return command, nil
	}
	data, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("pi: encode RPC params: %w", err)
	}
	if err := json.Unmarshal(data, &command); err != nil || command == nil {
		return nil, fmt.Errorf("pi: RPC params must be an object")
	}
	return command, nil
}

func (s *piSession) sendExtensionUIResponse(command map[string]any) error {
	// UI responses use the extension's request ID and are one-way:
	// Pi explicitly does not send a normal RPC response for them.
	id, _ := command["id"].(string)
	if id == "" {
		return fmt.Errorf("pi: extension UI response requires its request id")
	}
	if !s.alive.Load() {
		return fmt.Errorf("pi: session is closed")
	}
	command["type"] = "extension_ui_response"
	return s.writeRPCCommand(command)
}

func (s *piSession) deliverRPCResponse(raw map[string]any) {
	id, _ := raw["id"].(string)
	s.rpcMu.Lock()
	response := s.rpcPending[id]
	delete(s.rpcPending, id)
	s.rpcMu.Unlock()
	if response == nil {
		return
	}
	var reply piRPCResponse
	if success, _ := raw["success"].(bool); !success {
		message, _ := raw["error"].(string)
		if message == "" {
			message = "command failed"
		}
		reply.err = fmt.Errorf("pi: RPC %v: %s", raw["command"], message)
	} else if raw["data"] != nil {
		reply.data, reply.err = json.Marshal(raw["data"])
	}
	response <- reply
}

func (s *piSession) failPendingRPC(err error) {
	s.rpcMu.Lock()
	pending := s.rpcPending
	s.rpcPending = nil
	s.rpcMu.Unlock()
	for _, response := range pending {
		response <- piRPCResponse{err: err}
	}
}

func (s *piSession) finishNativeTurn() {
	if s.pendingErr != "" {
		select {
		case s.events <- core.Event{Type: core.EventError, Error: fmt.Errorf("%s", s.pendingErr)}:
		case <-s.ctx.Done():
		}
		s.pendingErr = ""
	}
	select {
	case s.events <- core.Event{Type: core.EventResult, SessionID: s.CurrentSessionID(), Done: true}:
	case <-s.ctx.Done():
	}
}

func (s *piSession) captureNativeUsage(raw map[string]any) {
	switch raw["type"] {
	case "response":
		if data, ok := raw["data"].(map[string]any); ok {
			model, _ := data["model"].(map[string]any)
			if model == nil && raw["command"] == "set_model" {
				model = data
			}
			s.captureNativeModel(model)
		}
	case "message_start", "message_end":
		message, _ := raw["message"].(map[string]any)
		if message["role"] == "assistant" {
			if model, ok := message["model"].(string); ok && model != "" {
				s.nativeModel = model
			}
			if usage, ok := message["usage"].(map[string]any); ok {
				s.storeNativeUsage(usage)
			}
		}
	case "message_update":
		if usage, ok := raw["usage"].(map[string]any); ok {
			s.storeNativeUsage(usage)
		}
	}
}

func (s *piSession) captureNativeModel(model map[string]any) {
	if model == nil {
		return
	}
	if id, ok := model["id"].(string); ok && id != "" {
		s.nativeModel = id
	}
	if window := tokenCount(model["contextWindow"]); window > 0 {
		s.nativeContextWindow = window
	}
}

func (s *piSession) storeNativeUsage(raw map[string]any) {
	input, output := tokenCount(raw["input"]), tokenCount(raw["output"])
	read, write := tokenCount(raw["cacheRead"]), tokenCount(raw["cacheWrite"])
	if input+output+read+write == 0 {
		return // zero fields during streaming do not confirm provider usage
	}
	window := s.nativeContextWindow
	if window == 0 {
		window = s.modelsCW[s.nativeModel]
	}
	if window == 0 {
		window = 200_000
	}
	usage := &core.ContextUsage{
		UsedTokens: input + read + write, TotalTokens: input + output + read + write,
		InputTokens: input, OutputTokens: output, CachedInputTokens: read,
		CacheCreationInputTokens: write, ContextWindow: window,
	}
	s.usageMu.Lock()
	s.lastUsage = usage
	s.usageMu.Unlock()
}

func tokenCount(value any) int {
	var count int
	switch value := value.(type) {
	case float64:
		count = int(value)
	case int:
		count = value
	case json.Number:
		parsed, _ := value.Int64()
		count = int(parsed)
	}
	if count < 0 {
		return 0
	}
	return count
}
