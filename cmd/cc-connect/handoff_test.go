package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestHandoffSelectsIntactMessagesInOrder(t *testing.T) {
	history := []core.HistoryEntry{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: strings.Repeat("old", 1000)},
		{Role: "tool", Content: "raw tool output"},
		{Role: "user", Content: "recent question"},
		{Role: "assistant", Content: "recent answer"},
	}
	prompt, kept, total := handoffPrompt(history, 300)
	if kept != 3 || total != 4 || strings.Contains(prompt, "raw tool output") ||
		strings.Index(prompt, "first question") < 0 ||
		strings.Index(prompt, "first question") >= strings.Index(prompt, "recent question") ||
		strings.Index(prompt, "recent question") >= strings.Index(prompt, "recent answer") {
		t.Fatalf("unexpected handoff (%d of %d): %s", kept, total, prompt)
	}
}

func TestHandoffRebindsOnlyActiveConversations(t *testing.T) {
	source := []byte(`{
		"active_session":{"owner":"s1"},
		"sessions":{
			"s1":{"id":"s1","agent_session_id":"old","agent_type":"previous","active_provider":"previous","history":[{"role":"user","content":"keep"}],"future_field":true},
			"s2":{"id":"s2","agent_session_id":"archived"}
		},
		"future_field":true
	}`)
	data, err := migrateActiveSessions(source, "destination", func(s *core.Session) (string, error) {
		if s.ID != "s1" || len(s.History) != 1 || s.History[0].Content != "keep" {
			t.Fatalf("unexpected source conversation: %q", s.ID)
		}
		return "new", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Sessions map[string]core.Session
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	s := got.Sessions["s1"]
	if s.AgentSessionID != "new" || s.AgentType != "destination" || s.ActiveProvider != "" ||
		len(s.PastAgentSessionIDs) != 1 || s.PastAgentSessionIDs[0] != "old" ||
		got.Sessions["s2"].AgentSessionID != "archived" || strings.Count(string(data), `"future_field": true`) != 2 {
		t.Fatalf("unexpected staged store: %s", data)
	}
}
