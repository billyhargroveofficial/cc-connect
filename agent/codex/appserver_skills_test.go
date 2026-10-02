package codex

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerExplicitSkillsAreNativePerMessageInputs(t *testing.T) {
	root, requests := nativeTestManagedServer(t)
	agent, err := New(map[string]any{
		"cmd": os.Args[0], "backend": "app_server", "app_server_url": "managed",
		"work_dir": t.TempDir(), "codex_home": root, "native_events": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	skillSession, ok := session.(core.AgentSkillSession)
	if !ok {
		t.Fatal("app-server session does not support native skill attachments")
	}
	skills := []core.SkillAttachment{{Name: "review", Path: "/installed/review/SKILL.md"}, {Name: "research", Path: "/installed/research/SKILL.md"}}
	if err := skillSession.SendWithSkills("Use $review and $research", "message-1", nil, nil, skills); err != nil {
		t.Fatal(err)
	}
	turn := takeNativeTestRequest(t, requests, "turn/start")
	input := turn.Params["input"].([]any)
	if len(input) != 3 || input[0].(map[string]any)["text"] != "Use $review and $research" {
		t.Fatalf("skill message input: %+v", input)
	}
	for i, skill := range skills {
		if !reflect.DeepEqual(input[i+1], map[string]any{"type": "skill", "name": skill.Name, "path": skill.Path}) {
			t.Fatalf("native skill input %d: %+v", i, input[i+1])
		}
	}
	if err := session.Send("ordinary next message", "message-2", nil, nil); err != nil {
		t.Fatal(err)
	}
	next := takeNativeTestRequest(t, requests, "turn/start")
	if input := next.Params["input"].([]any); len(input) != 1 || input[0].(map[string]any)["text"] != "ordinary next message" {
		t.Fatalf("previous selected skills leaked into next message: %+v", input)
	}
}
