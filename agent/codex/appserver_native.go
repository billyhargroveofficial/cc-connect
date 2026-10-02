package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// These extensions are opt-in. Existing messaging projects keep their legacy
// event contract, while a studio can retain the provider's typed transcript.
type appServerNativeOptions struct {
	enabled               bool
	config                map[string]any
	serviceTier           *string
	developerInstructions string
	observer              core.NativeEventHandler
	tools                 []map[string]any
	toolHandler           core.DynamicToolHandler
	toolTimeout           time.Duration
}

// Keep the provider turn identity on normalized events as well. A goal may
// start its next turn before the host consumes the preceding result.
type appServerEventScope struct {
	threadID string
	turnID   string
}

func (s *appServerSession) currentEventScope() appServerEventScope {
	s.stateMu.Lock()
	turnID := s.currentTurn
	s.stateMu.Unlock()
	return appServerEventScope{threadID: s.CurrentSessionID(), turnID: turnID}
}

func (s *appServerSession) eventScope(scopes []appServerEventScope) appServerEventScope {
	if len(scopes) != 0 {
		return scopes[0]
	}
	return s.currentEventScope()
}

func appServerParamsScope(params map[string]any) appServerEventScope {
	threadID, _ := params["threadId"].(string)
	turnID, _ := params["turnId"].(string)
	return appServerEventScope{threadID: threadID, turnID: turnID}
}

func (s *appServerSession) toolItemMetadata(item map[string]any) map[string]any {
	if !s.native.enabled {
		return nil
	}
	id, _ := item["id"].(string)
	if id == "" {
		return nil
	}
	return map[string]any{"toolCallId": id}
}

var _ core.NativeEventObserver = (*Agent)(nil)
var _ core.DynamicToolAgent = (*Agent)(nil)
var _ core.AgentRPCSession = (*appServerSession)(nil)

func (a *Agent) SetNativeEventHandler(handler core.NativeEventHandler) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.appServerNative.observer = handler
	if handler != nil {
		a.appServerNative.enabled = true
	}
}

func (a *Agent) SetDynamicTools(tools []map[string]any, handler core.DynamicToolHandler) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.appServerNative.tools = cloneAppServerTools(tools)
	a.appServerNative.toolHandler = handler
}

func cloneAppServerMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	data, err := json.Marshal(src)
	if err != nil {
		slog.Warn("codex app-server: cannot copy configuration", "error", err)
		return nil
	}
	var dst map[string]any
	if err := json.Unmarshal(data, &dst); err != nil {
		return nil
	}
	return dst
}

func cloneAppServerTools(src []map[string]any) []map[string]any {
	if src == nil {
		return nil
	}
	dst := make([]map[string]any, len(src))
	for i, tool := range src {
		dst[i] = cloneAppServerMap(tool)
	}
	return dst
}

// RPC exposes the app-server control surface to trusted host code. A web API
// must validate its own allowed methods and thread ownership before using it.
func (s *appServerSession) RPC(ctx context.Context, method string, params any, result any) error {
	if ctx == nil {
		return fmt.Errorf("codex app-server RPC requires a context")
	}
	if strings.TrimSpace(method) == "" {
		return fmt.Errorf("codex app-server RPC requires a method")
	}
	if err := s.requestWithContext(ctx, method, params, result, appServerRequestTimeout); err != nil {
		return err
	}
	if method == "thread/settings/update" {
		// Keep Send's overrides in step even when its next call beats the
		// authoritative thread/settings/updated notification.
		data, _ := json.Marshal(params)
		var update struct {
			ThreadID    string          `json:"threadId"`
			Cwd         *string         `json:"cwd"`
			Model       *string         `json:"model"`
			Effort      *string         `json:"effort"`
			ServiceTier json.RawMessage `json:"serviceTier"`
		}
		if json.Unmarshal(data, &update) == nil && s.isCurrentThread(update.ThreadID) {
			s.runtimeMu.Lock()
			if update.Cwd != nil {
				s.workDir = *update.Cwd
			}
			if update.Model != nil {
				s.model = *update.Model
			}
			if update.Effort != nil {
				s.effort = normalizeRuntimeReasoningEffort(*update.Effort)
			}
			if len(update.ServiceTier) != 0 {
				var tier *string
				if json.Unmarshal(update.ServiceTier, &tier) == nil {
					s.serviceTier = stringValue(tier)
					s.serviceTierConfigured = true
				}
			}
			s.runtimeMu.Unlock()
		}
	}
	return nil
}

func (s *appServerSession) acceptsNativeThread(threadID string) bool {
	if s.isCurrentThread(threadID) {
		return true
	}
	if !s.native.enabled || threadID == "" {
		return false
	}
	s.nativeMu.Lock()
	_, ok := s.nativeDescendants[threadID]
	s.nativeMu.Unlock()
	return ok
}

type nativeEventScope struct {
	ThreadID string `json:"threadId"`
	Thread   struct {
		ID             string `json:"id"`
		ParentThreadID string `json:"parentThreadId"`
	} `json:"thread"`
	Item struct {
		Type              string   `json:"type"`
		Tool              string   `json:"tool"`
		AgentThreadID     string   `json:"agentThreadId"`
		ReceiverThreadIDs []string `json:"receiverThreadIds"`
	} `json:"item"`
}

func (s *appServerSession) observeNative(method string, params, requestID json.RawMessage) {
	if !s.native.enabled || s.native.observer == nil {
		return
	}
	var scope nativeEventScope
	if err := json.Unmarshal(params, &scope); err != nil {
		return
	}
	threadID := scope.ThreadID
	if method == "thread/started" {
		threadID = scope.Thread.ID
		if s.CurrentSessionID() == "" && threadID != "" {
			// The notification can precede the thread/start response. Retain a
			// bounded set, then publish only the response's exact thread ID.
			s.nativeMu.Lock()
			if s.nativeThreadStarts == nil {
				s.nativeThreadStarts = make(map[string]json.RawMessage)
			}
			if len(s.nativeThreadStarts) >= 32 {
				for id := range s.nativeThreadStarts {
					delete(s.nativeThreadStarts, id)
					break
				}
			}
			s.nativeThreadStarts[threadID] = append(json.RawMessage(nil), params...)
			s.nativeMu.Unlock()
			return
		}
		if s.acceptsNativeThread(scope.Thread.ParentThreadID) {
			s.addNativeDescendant(threadID)
		}
	}
	if !s.acceptsNativeThread(threadID) {
		return
	}
	// Discover children only through an event from a thread we already own.
	// An unrelated client's spawn or thread notification cannot expand scope.
	if scope.Item.Type == "subAgentActivity" {
		s.addNativeDescendant(scope.Item.AgentThreadID)
	}
	if scope.Item.Type == "collabAgentToolCall" && scope.Item.Tool == "spawnAgent" {
		for _, id := range scope.Item.ReceiverThreadIDs {
			s.addNativeDescendant(id)
		}
	}
	s.native.observer(core.NativeEvent{
		Backend: "codex", Method: method,
		RootThreadID: s.CurrentSessionID(),
		Params:       append(json.RawMessage(nil), params...),
		RequestID:    append(json.RawMessage(nil), requestID...),
		Timestamp:    time.Now().UTC(),
	})
}

func (s *appServerSession) addNativeDescendant(threadID string) {
	if threadID == "" || s.isCurrentThread(threadID) {
		return
	}
	s.nativeMu.Lock()
	if s.nativeDescendants == nil {
		s.nativeDescendants = make(map[string]struct{})
	}
	s.nativeDescendants[threadID] = struct{}{}
	s.nativeMu.Unlock()
}

func (s *appServerSession) flushNativeThreadStart(threadID string) {
	s.nativeMu.Lock()
	params := s.nativeThreadStarts[threadID]
	s.nativeThreadStarts = nil
	s.nativeMu.Unlock()
	if len(params) != 0 {
		s.observeNative("thread/started", params, nil)
	}
}

func (s *appServerSession) runDynamicTool(rawID, params json.RawMessage) {
	var call core.DynamicToolCall
	if err := json.Unmarshal(params, &call); err != nil || call.Tool == "" || call.CallID == "" {
		_ = s.writeJSON(map[string]any{
			"jsonrpc": "2.0", "id": rawID,
			"error": map[string]any{"code": -32602, "message": "invalid dynamic tool params"},
		})
		return
	}
	timeout := s.native.toolTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	// A tool may ask another bot for work or issue an RPC. Never run it on the
	// read loop that must receive the corresponding response.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		parentCtx := s.ctx
		if parentCtx == nil {
			parentCtx = context.Background()
		}
		ctx, cancel := context.WithTimeout(parentCtx, timeout)
		defer cancel()
		type outcome struct {
			result core.DynamicToolResult
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			result, err := s.native.toolHandler(ctx, call)
			done <- outcome{result: result, err: err}
		}()
		var out outcome
		select {
		case out = <-done:
		case <-ctx.Done():
			out.err = ctx.Err()
		}
		if out.err != nil {
			out.result = core.DynamicToolResult{
				ContentItems: []map[string]any{{"type": "inputText", "text": out.err.Error()}},
				Success:      false,
			}
		}
		if out.result.ContentItems == nil {
			out.result.ContentItems = []map[string]any{}
		}
		if err := s.writeJSONWithTimeout("item/tool/call", map[string]any{
			"jsonrpc": "2.0", "id": rawID, "result": out.result,
		}, appServerRequestTimeout); err != nil && parentCtx.Err() == nil {
			slog.Warn("codex app-server: dynamic tool response failed", "tool", call.Tool, "error", err)
		}
	}()
}

func (s *appServerSession) requestWithContext(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.ctx != nil && s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		return context.DeadlineExceeded
	}
	id := s.nextID.Add(1)
	ch := make(chan rpcResponseEnvelope, 1)
	s.pendingMu.Lock()
	if s.pending == nil {
		s.pending = make(map[int64]chan rpcResponseEnvelope)
	}
	s.pending[id] = ch
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	if err := s.writeJSONWithContext(ctx, method, map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}, timeout); err != nil {
		return err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			if resp.Error.cause != nil {
				return resp.Error.cause
			}
			return &core.RPCRejectionError{Message: strings.TrimSpace(resp.Error.Message)}
		}
		if out != nil {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("decode %s response: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.contextDone():
		return s.contextErr()
	case <-timer.C:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		return fmt.Errorf("%s timed out", method)
	}
}

func (s *appServerSession) writeJSONWithContext(ctx context.Context, method string, payload any, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.ctx != nil && s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- s.writeJSON(payload) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// Closing only this connection prevents an unfinished write from
		// arriving later after its caller has already cancelled.
		s.abortTransport()
		return ctx.Err()
	case <-s.contextDone():
		return s.contextErr()
	case <-timer.C:
		err := fmt.Errorf("%s write timed out", method)
		slog.Warn("codex app-server write timed out, closing session", "method", method, "timeout", timeout)
		s.abortTransport()
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		return err
	}
}
