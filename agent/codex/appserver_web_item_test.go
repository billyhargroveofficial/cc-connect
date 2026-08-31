package codex

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func newAppServerItemTestSession(t *testing.T) *appServerSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &appServerSession{
		ctx:    ctx,
		cancel: cancel,
		events: make(chan core.Event, 8),
	}
}

func nextAppServerItemEvent(t *testing.T, s *appServerSession) core.Event {
	t.Helper()
	select {
	case evt := <-s.events:
		return evt
	default:
		t.Fatal("no event emitted")
		return core.Event{}
	}
}

func TestAppServerSession_WebFetchDoesNotRenderBlankWebSearch(t *testing.T) {
	s := newAppServerItemTestSession(t)
	item := map[string]any{
		"type":   "webSearch",
		"query":  "",
		"action": map[string]any{"type": "other"},
	}

	s.handleItemStarted(item)
	started := nextAppServerItemEvent(t, s)
	if started.Type != core.EventToolUse {
		t.Fatalf("started event type = %v, want EventToolUse", started.Type)
	}
	if started.ToolName != "WebFetch" {
		t.Fatalf("started ToolName = %q, want WebFetch", started.ToolName)
	}
	if started.ToolInput != "open page" {
		t.Fatalf("started ToolInput = %q, want open page", started.ToolInput)
	}

	item["results"] = []any{map[string]any{
		"type":   "text_result",
		"ref_id": "turn1view0",
		"title":  "Responses | OpenAI API Reference",
		"url":    "https://developers.openai.com/api/reference/responses",
	}}
	s.handleItemCompleted(item)
	completed := nextAppServerItemEvent(t, s)
	if completed.Type != core.EventToolResult {
		t.Fatalf("completed event type = %v, want EventToolResult", completed.Type)
	}
	if completed.ToolName != "WebFetch" {
		t.Fatalf("completed ToolName = %q, want WebFetch", completed.ToolName)
	}
	if completed.ToolResult != "Responses | OpenAI API Reference" {
		t.Fatalf("completed ToolResult = %q", completed.ToolResult)
	}
}

func TestAppServerSession_WebSearchKeepsQuery(t *testing.T) {
	s := newAppServerItemTestSession(t)
	item := map[string]any{
		"type":  "webSearch",
		"query": "Codex CLI tools shell web search",
		"action": map[string]any{
			"type":  "search",
			"query": "Codex CLI tools shell web search",
		},
	}

	s.handleItemStarted(item)
	evt := nextAppServerItemEvent(t, s)
	if evt.Type != core.EventToolUse || evt.ToolName != "WebSearch" {
		t.Fatalf("got %v/%q, want EventToolUse/WebSearch", evt.Type, evt.ToolName)
	}
	if evt.ToolInput != "Codex CLI tools shell web search" {
		t.Fatalf("ToolInput = %q", evt.ToolInput)
	}
}

func TestAppServerWebToolDisplay_UsesStructuredActions(t *testing.T) {
	tests := []struct {
		name      string
		item      map[string]any
		wantTool  string
		wantInput string
	}{
		{
			name: "search query list",
			item: map[string]any{
				"type":  "webSearch",
				"query": "",
				"action": map[string]any{
					"type":    "search",
					"queries": []any{"first query", "second query"},
				},
			},
			wantTool:  "WebSearch",
			wantInput: "first query | second query",
		},
		{
			name: "open page",
			item: map[string]any{
				"type":  "webSearch",
				"query": "",
				"action": map[string]any{
					"type": "openPage",
					"url":  "https://example.com/docs?token=secret#section",
				},
			},
			wantTool:  "WebFetch",
			wantInput: "https://example.com/docs",
		},
		{
			name: "find in page",
			item: map[string]any{
				"type":  "webSearch",
				"query": "",
				"action": map[string]any{
					"type":    "findInPage",
					"url":     "https://example.com/docs",
					"pattern": "WebSearchAction",
				},
			},
			wantTool:  "WebFetch",
			wantInput: "WebSearchAction · https://example.com/docs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTool, gotInput := appServerWebToolDisplay(tt.item)
			if gotTool != tt.wantTool || gotInput != tt.wantInput {
				t.Fatalf("got %q/%q, want %q/%q", gotTool, gotInput, tt.wantTool, tt.wantInput)
			}
		})
	}
}
