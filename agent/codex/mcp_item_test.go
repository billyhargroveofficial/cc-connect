package codex

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func newMcpTestSession(t *testing.T) *codexSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &codexSession{ctx: ctx, cancel: cancel, events: make(chan core.Event, 8)}
}

func TestHandleItemStarted_McpToolCallEmitsToolUse(t *testing.T) {
	cs := newMcpTestSession(t)

	cs.handleItemStarted(map[string]any{"item": map[string]any{
		"type":      "mcp_tool_call",
		"server":    "telegram",
		"tool":      "read_chat_slice",
		"arguments": map[string]any{"chat": "Парилка228", "limit": float64(100)},
		"status":    "in_progress",
	}})

	select {
	case evt := <-cs.events:
		if evt.Type != core.EventToolUse {
			t.Fatalf("event type = %v, want EventToolUse", evt.Type)
		}
		if evt.ToolName != "MCP telegram.read_chat_slice" {
			t.Fatalf("ToolName = %q, want %q", evt.ToolName, "MCP telegram.read_chat_slice")
		}
		if evt.ToolInput == "" {
			t.Fatal("ToolInput is empty, want serialized arguments")
		}
	default:
		t.Fatal("no event emitted for mcp_tool_call item.started")
	}
}

func TestHandleItemCompleted_McpToolCallEmitsToolResult(t *testing.T) {
	cs := newMcpTestSession(t)

	cs.handleItemCompleted(map[string]any{"item": map[string]any{
		"type":   "mcp_tool_call",
		"server": "telegram",
		"tool":   "get_me",
		"status": "completed",
		"result": map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "{\"id\": 342262559}"},
		}},
	}})

	select {
	case evt := <-cs.events:
		if evt.Type != core.EventToolResult {
			t.Fatalf("event type = %v, want EventToolResult", evt.Type)
		}
		if evt.ToolName != "MCP telegram.get_me" {
			t.Fatalf("ToolName = %q", evt.ToolName)
		}
		if evt.ToolSuccess == nil || !*evt.ToolSuccess {
			t.Fatal("ToolSuccess = false, want true for completed status")
		}
		if evt.ToolResult != "{\"id\": 342262559}" {
			t.Fatalf("ToolResult = %q", evt.ToolResult)
		}
	default:
		t.Fatal("no event emitted for mcp_tool_call item.completed")
	}
}

func TestHandleItemCompleted_McpToolCallErrorMarksFailure(t *testing.T) {
	cs := newMcpTestSession(t)

	cs.handleItemCompleted(map[string]any{"item": map[string]any{
		"type":   "mcp_tool_call",
		"server": "telegram",
		"tool":   "send_message",
		"status": "failed",
		"error":  map[string]any{"message": "connection refused"},
	}})

	select {
	case evt := <-cs.events:
		if evt.ToolSuccess == nil || *evt.ToolSuccess {
			t.Fatal("ToolSuccess = true, want false for failed call")
		}
		if evt.ToolResult != "connection refused" {
			t.Fatalf("ToolResult = %q, want error message", evt.ToolResult)
		}
	default:
		t.Fatal("no event emitted for failed mcp_tool_call")
	}
}

func TestHandleItemStarted_FileChangeEmitsToolUse(t *testing.T) {
	cs := newMcpTestSession(t)

	cs.handleItemStarted(map[string]any{"item": map[string]any{
		"type":   "file_change",
		"status": "in_progress",
		"changes": map[string]any{
			"/home/billy/b.txt": map[string]any{"type": "add"},
			"/home/billy/a.go":  map[string]any{"type": "update", "unified_diff": "@@ -1 +1 @@"},
		},
	}})

	select {
	case evt := <-cs.events:
		if evt.Type != core.EventToolUse || evt.ToolName != "ApplyPatch" {
			t.Fatalf("got %v/%q, want EventToolUse/ApplyPatch", evt.Type, evt.ToolName)
		}
		if evt.ToolInput != "update /home/billy/a.go, add /home/billy/b.txt" {
			t.Fatalf("ToolInput = %q", evt.ToolInput)
		}
	default:
		t.Fatal("no event emitted for file_change item.started")
	}
}

func TestHandleItemCompleted_FileChangeEmitsToolResult(t *testing.T) {
	cs := newMcpTestSession(t)

	cs.handleItemCompleted(map[string]any{"item": map[string]any{
		"type":   "file_change",
		"status": "completed",
		"changes": map[string]any{
			"/home/billy/a.go": map[string]any{"type": "update"},
		},
	}})

	select {
	case evt := <-cs.events:
		if evt.Type != core.EventToolResult || evt.ToolName != "ApplyPatch" {
			t.Fatalf("got %v/%q, want EventToolResult/ApplyPatch", evt.Type, evt.ToolName)
		}
		if evt.ToolSuccess == nil || !*evt.ToolSuccess {
			t.Fatal("ToolSuccess = false, want true")
		}
	default:
		t.Fatal("no event emitted for file_change item.completed")
	}
}

func TestHandleItemUpdated_TodoListEmitsChecklist(t *testing.T) {
	cs := newMcpTestSession(t)

	cs.handleItemUpdated(map[string]any{"item": map[string]any{
		"type": "todo_list",
		"items": []any{
			map[string]any{"text": "посчитать строки", "completed": true},
			map[string]any{"text": "отчитаться", "completed": false},
		},
	}})

	select {
	case evt := <-cs.events:
		if evt.Type != core.EventToolUse || evt.ToolName != "UpdatePlan" {
			t.Fatalf("got %v/%q, want EventToolUse/UpdatePlan", evt.Type, evt.ToolName)
		}
		if evt.ToolInput != "✔ посчитать строки | ◻ отчитаться" {
			t.Fatalf("ToolInput = %q", evt.ToolInput)
		}
	default:
		t.Fatal("no event emitted for todo_list item.updated")
	}
}

func TestHandleItemCompleted_TodoListStaysSilent(t *testing.T) {
	cs := newMcpTestSession(t)

	cs.handleItemCompleted(map[string]any{"item": map[string]any{
		"type":  "todo_list",
		"items": []any{map[string]any{"text": "x", "completed": true}},
	}})

	select {
	case evt := <-cs.events:
		t.Fatalf("unexpected event for todo_list item.completed: %+v", evt)
	default:
	}
}
