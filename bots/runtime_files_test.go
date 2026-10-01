package bots

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimePublishedFilesRemainDownloadableApartFromFinalAnswer(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	workspace := NewWorkspace(store)
	runtime.cfg.PublishFiles = workspace.PublishFiles
	path := filepath.Join(bot.WorkDir, "tmp", "report.txt")
	if err := os.WriteFile(path, []byte("verified result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	turn, err := runtime.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "prepare and send report"})
	if err != nil {
		t.Fatal(err)
	}
	sent := nextRuntimeSend(t, factory)
	result, err := runtime.DynamicTool(context.Background(), bot.ID, "bots_publish_files", json.RawMessage(`{"paths":["tmp/report.txt"],"caption":"Report ready"}`), "publish-1")
	if err != nil || !result.Success {
		t.Fatalf("publish: %+v %v", result, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	sent.session.complete("Report ready")
	waitRuntimeTurn(t, runtime, bot.ID, turn)
	events, err := store.Events(bot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var published []Attachment
	artifactMessages, finalMessages := 0, 0
	for _, event := range events {
		if event.Type != "message" || event.TurnID != turn {
			continue
		}
		var message struct {
			Role, Content, Source string
			Artifact              bool
			Attachments           []Attachment
		}
		if err := json.Unmarshal(event.Data, &message); err != nil {
			t.Fatal(err)
		}
		if message.Role != "assistant" {
			continue
		}
		if message.Artifact {
			artifactMessages++
			if message.Source != "files" || message.Content != "Report ready" {
				t.Fatalf("artifact changed: %+v", message)
			}
			published = message.Attachments
		} else {
			finalMessages++
		}
	}
	if artifactMessages != 1 || finalMessages != 1 || len(published) != 1 || published[0].Path != "" {
		t.Fatalf("separate final/artifact identity lost: artifacts=%d finals=%d files=%+v", artifactMessages, finalMessages, published)
	}
	resolved, err := workspace.ResolveAttachments(bot.ID, published)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(resolved[0].Path)
	if err != nil || string(data) != "verified result\n" {
		t.Fatalf("published file did not survive original removal: %q %v", data, err)
	}
}
