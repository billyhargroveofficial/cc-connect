package telegram

import (
	"os"

	"github.com/go-telegram/bot/models"

	"path/filepath"
	"testing"
	"time"
)

func TestChatLogIgnored(t *testing.T) {
	f := filepath.Join(t.TempDir(), "ignore.json")
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	os.WriteFile(f, []byte(`{"1":{"until":"`+future+`"},"2":{"until":"`+past+`"},"3":{"until":"`+future+`"}}`), 0o644)
	cl, err := newChatLog(map[string]any{"ignore_file": f, "ignore_exempt": "3"})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]bool{1: true, 2: false, 3: false, 4: false} {
		if got := cl.ignored(id); got != want {
			t.Errorf("ignored(%d) = %v, want %v", id, got, want)
		}
	}
	os.WriteFile(f, []byte(`{"5":{"until":"`+time.Now().Add(time.Hour).Format("2006-01-02T15:04:05-0700")+`"}}`), 0o644)
	if !cl.ignored(5) {
		t.Error("BSD date offset format must parse")
	}
	os.WriteFile(f, []byte(`not json`), 0o644)
	if cl.ignored(1) {
		t.Error("malformed file must ignore nobody")
	}
}

func TestIsDirectedAtBotMentionOnly(t *testing.T) {
	p := &Platform{token: "token", mentionOnly: true}
	p.selfUser = &models.User{ID: 42, Username: "mybot"}
	group := models.Chat{ID: 1, Type: models.ChatTypeSupergroup}
	reply := &models.Message{Text: "ну и что", Chat: group, ReplyToMessage: &models.Message{From: &models.User{ID: 42}}}
	if p.isDirectedAtBot(reply) {
		t.Error("reply to bot without mention must not wake it")
	}
	mention := &models.Message{Text: "@mybot привет", Chat: group, Entities: []models.MessageEntity{{Type: models.MessageEntityTypeMention, Offset: 0, Length: 6}}}
	if !p.isDirectedAtBot(mention) {
		t.Error("@mention must wake the bot")
	}
	bare := &models.Message{Text: "/new", Chat: group, Entities: []models.MessageEntity{{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: 4}}}
	if p.isDirectedAtBot(bare) {
		t.Error("bare command must not wake a mention_only bot")
	}
	cmd := &models.Message{Text: "/new@mybot", Chat: group, Entities: []models.MessageEntity{{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: 10}}}
	if !p.isDirectedAtBot(cmd) {
		t.Error("/cmd@mybot must wake the bot")
	}
}
