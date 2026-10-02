package bots

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRuntimeToolJournalRetainsDistinctNativeCallIDs(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	turn, err := runtime.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "list twice"})
	if err != nil {
		t.Fatal(err)
	}
	sent := nextRuntimeSend(t, factory)
	for _, id := range []string{"native-call-1", "native-call-2"} {
		result, err := runtime.DynamicTool(context.Background(), bot.ID, "bots_list", json.RawMessage(`{}`), id)
		if err != nil || !result.Success {
			t.Fatalf("tool %s: %+v %v", id, result, err)
		}
	}
	sent.session.complete("done")
	waitRuntimeTurn(t, runtime, bot.ID, turn)
	events, err := store.Events(bot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]map[string]int{}
	for _, event := range events {
		if event.Type != "agent" {
			continue
		}
		var data struct{ Type, ToolName, ToolCallID string }
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.ToolName != "bots_list" {
			continue
		}
		if counts[data.ToolCallID] == nil {
			counts[data.ToolCallID] = map[string]int{}
		}
		counts[data.ToolCallID][data.Type]++
	}
	for _, id := range []string{"native-call-1", "native-call-2"} {
		if counts[id]["tool_use"] != 1 || counts[id]["tool_result"] != 1 {
			t.Fatalf("call identity lost: %+v", counts)
		}
	}
}
