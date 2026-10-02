package bots

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func botToolDefinitions(chief bool) []map[string]any {
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	text := map[string]any{"type": "string"}
	tools := []map[string]any{
		{"name": "bots_list", "description": "List the owner's persistent bots and their current state.", "inputSchema": object(map[string]any{})},
		{"name": "bots_status", "description": "Read a bot's current state and most recent completed result.", "inputSchema": object(map[string]any{"botId": text}, "botId")},
		{"name": "bots_send", "description": "Delegate bounded work to another persistent bot and wait for its actual result. Never delegate back to a busy caller.", "inputSchema": object(map[string]any{"botId": text, "message": text}, "botId", "message")},
		{"name": "bots_publish_files", "description": "Send prepared files, pictures, documents or archives from your own workspace to this conversation as downloadable attachments. Paths are relative to your workspace; originals are preserved.", "inputSchema": object(map[string]any{"paths": map[string]any{"type": "array", "items": text, "minItems": 1, "maxItems": 10}, "caption": text}, "paths")},
	}
	if chief {
		tools = append(tools, map[string]any{"name": "bots_create", "description": "Create a persistent specialist bot with its own workspace and role. Only the coordinator can create bots.", "inputSchema": object(map[string]any{"name": text, "role": text, "backend": map[string]any{"type": "string", "enum": []string{"codex", "pi"}}, "model": text}, "name", "role")})
	}
	return tools
}

// DynamicTool is shared by Codex's native dynamic tool callback and Pi's
// authenticated loopback extension route. There is no second task system.
func (r *Runtime) DynamicTool(ctx context.Context, sourceID, name string, args json.RawMessage, callIDs ...string) (core.DynamicToolResult, error) {
	if err := ctx.Err(); err != nil {
		return toolResult(nil, err), err
	}
	s, err := r.state(sourceID)
	if err != nil {
		return toolResult(nil, err), err
	}
	s.mu.Lock()
	t := s.current
	s.mu.Unlock()
	if t == nil {
		err := fmt.Errorf("%w: tools require an active bot turn", ErrConflict)
		return toolResult(nil, err), err
	}
	t.mu.Lock()
	t.toolCalls++
	exhausted := t.toolCalls > r.cfg.MaxToolCalls
	t.metrics.pause(r.cfg.Now())
	t.mu.Unlock()
	var arguments map[string]any
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(args, &arguments); err != nil {
		return toolResult(nil, fmt.Errorf("invalid tool arguments")), nil
	}
	callID := ""
	if len(callIDs) > 0 {
		callID = callIDs[0]
	}
	if _, err := r.store.AppendEvent(sourceID, t.id, "agent", map[string]any{"type": "tool_use", "toolName": name, "toolInput": string(args), "toolInputRaw": arguments, "toolCallId": callID}); err != nil {
		return toolResult(nil, err), err
	}
	toolCtx, cancel := context.WithTimeout(ctx, r.cfg.ToolTimeout)
	stopSource := context.AfterFunc(t.ctx, cancel)
	defer func() { stopSource(); cancel() }()
	var output any
	if exhausted {
		err = fmt.Errorf("tool call limit reached for this turn")
	} else {
		output, err = r.executeBotTool(toolCtx, sourceID, name, args)
	}
	result := toolResult(output, err)
	encoded, encodeErr := json.Marshal(result.ContentItems)
	if encodeErr != nil {
		return result, encodeErr
	}
	_, journalErr := r.store.AppendEvent(sourceID, t.id, "agent", map[string]any{"type": "tool_result", "toolName": name, "toolCallId": callID, "toolResult": string(encoded), "toolSuccess": result.Success, "toolStatus": map[bool]string{true: "completed", false: "failed"}[result.Success]})
	// Tool failures are content, not an HTTP transport failure: the model sees
	// the actual reason and can recover without silently retrying a delegation.
	return result, journalErr
}

func toolResult(output any, err error) core.DynamicToolResult {
	if err != nil {
		output = map[string]any{"error": err.Error(), "result": output}
	}
	encoded, encodeErr := json.Marshal(output)
	if encodeErr != nil {
		encoded, _ = json.Marshal(map[string]any{"error": encodeErr.Error()})
		err = encodeErr
	}
	return core.DynamicToolResult{Success: err == nil, ContentItems: []map[string]any{{"type": "inputText", "text": string(encoded)}}}
}

func decodeToolArguments(args json.RawMessage, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("%w: tool arguments must be one object", ErrInvalid)
	}
	return nil
}

func (r *Runtime) executeBotTool(ctx context.Context, sourceID, name string, args json.RawMessage) (any, error) {
	switch name {
	case "bots_list":
		var input struct{}
		if err := decodeToolArguments(args, &input); err != nil {
			return nil, err
		}
		return map[string]any{"bots": r.store.ListBots()}, nil
	case "bots_status":
		var input struct {
			BotID string `json:"botId"`
		}
		if err := decodeToolArguments(args, &input); err != nil {
			return nil, err
		}
		bot, err := r.store.GetBot(input.BotID)
		if err != nil {
			return nil, err
		}
		events, err := r.store.Events(input.BotID, 0)
		if err != nil {
			return nil, err
		}
		last := TurnResult{}
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Type != "turn" {
				continue
			}
			var result TurnResult
			if json.Unmarshal(events[i].Data, &result) == nil && terminalStatus(result.Status) {
				last, _ = r.persistedTurn(input.BotID, events[i].TurnID)
				break
			}
		}
		return map[string]any{"bot": bot, "busy": r.Busy(bot.ID), "lastResult": last}, nil
	case "bots_create":
		source, err := r.store.GetBot(sourceID)
		if err != nil {
			return nil, err
		}
		if !source.Chief {
			return nil, fmt.Errorf("only the coordinator may create bots")
		}
		var input struct {
			Name    string `json:"name"`
			Role    string `json:"role"`
			Backend string `json:"backend"`
			Model   string `json:"model"`
		}
		if err := decodeToolArguments(args, &input); err != nil {
			return nil, err
		}
		if strings.TrimSpace(input.Role) == "" {
			return nil, fmt.Errorf("a specialist role is required")
		}
		return r.store.CreateBot(Bot{Name: input.Name, Role: input.Role, Backend: input.Backend, Model: input.Model})
	case "bots_publish_files":
		return r.publishFiles(sourceID, args)
	case "bots_send":
		var input struct {
			BotID   string `json:"botId"`
			Message string `json:"message"`
		}
		if err := decodeToolArguments(args, &input); err != nil {
			return nil, err
		}
		return r.delegate(ctx, sourceID, input.BotID, input.Message)
	default:
		return nil, fmt.Errorf("unknown bot tool %q", name)
	}
}

func (r *Runtime) delegate(ctx context.Context, sourceID, targetID, message string) (any, error) {
	if sourceID == targetID {
		return nil, fmt.Errorf("a bot cannot delegate to itself")
	}
	if err := r.claimDelegation(sourceID, targetID); err != nil {
		return nil, err
	}
	defer func() { r.mu.Lock(); delete(r.edges, sourceID); r.mu.Unlock() }()
	source, err := r.store.GetBot(sourceID)
	if err != nil {
		return nil, err
	}
	turnID, err := r.sendMessage(ctx, ctx, targetID, MessageRequest{Text: "Message from bot " + source.Name + " (" + sourceID + "):\n" + message, Source: "bot:" + sourceID})
	if err != nil {
		return nil, err
	}
	result, err := r.WaitTurn(ctx, targetID, turnID)
	if err != nil {
		r.mu.Lock()
		target := r.states[targetID]
		r.mu.Unlock()
		if target != nil {
			target.mu.Lock()
			t := target.current
			target.mu.Unlock()
			if t != nil && t.id == turnID {
				t.cancel()
			}
		}
		return map[string]any{"botId": targetID, "turnId": turnID}, err
	}
	output := map[string]any{"botId": targetID, "turnId": turnID, "status": result.Status, "text": result.Text, "error": result.Error}
	if result.Status != "completed" {
		return output, fmt.Errorf("delegated bot finished with status %s", result.Status)
	}
	return output, nil
}

func (r *Runtime) claimDelegation(sourceID, targetID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.edges[sourceID] != "" {
		return fmt.Errorf("another delegation from this bot is still pending")
	}
	visited := map[string]bool{sourceID: true}
	for next, depth := targetID, 0; next != ""; next, depth = r.edges[next], depth+1 {
		if visited[next] {
			return fmt.Errorf("delegation would create a cycle")
		}
		if depth >= 6 {
			return fmt.Errorf("delegation depth limit reached")
		}
		visited[next] = true
	}
	r.edges[sourceID] = targetID
	return nil
}
