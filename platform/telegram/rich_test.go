package telegram

import (
	"context"
	"net/http"
	"testing"

	"github.com/chenhg5/cc-connect/core"

	"github.com/go-telegram/bot/models"
)

func TestNewRich_RegisteredUnderOwnName(t *testing.T) {
	p, err := core.CreatePlatform("telegram-rich", map[string]any{"token": "test-token"})
	if err != nil {
		t.Fatalf("CreatePlatform(telegram-rich) returned error: %v", err)
	}
	if _, ok := p.(*richPlatform); !ok {
		t.Fatalf("expected *richPlatform, got %T", p)
	}
}

// Session keys, routing and stored history are derived from Name(), so the rich
// variant must not present itself as a different platform.
func TestNewRich_ReportsPlainTelegramName(t *testing.T) {
	p, err := NewRich(map[string]any{"token": "test-token"})
	if err != nil {
		t.Fatalf("NewRich returned error: %v", err)
	}
	if got := p.Name(); got != "telegram" {
		t.Errorf("Name() = %q, want \"telegram\"", got)
	}
}

func TestNewRich_InheritsBaseOptionValidation(t *testing.T) {
	if _, err := NewRich(map[string]any{"token": "test-token", "progress_style": "nonsense"}); err == nil {
		t.Error("expected the base platform's option validation to reject an invalid progress_style")
	}
}

// A client that does not implement richSender (as in these tests, and as on any
// older Bot API build) must fall back to the inherited HTML path rather than
// dropping the reply.
func TestRichPlatform_FallsBackWhenClientLacksRichSupport(t *testing.T) {
	stub := newStubTelegramBot()
	r := &richPlatform{Platform: &Platform{bot: stub}}

	if err := r.Send(context.Background(), replyContext{chatID: 1}, "# heading"); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if err := r.Reply(context.Background(), replyContext{chatID: 1, messageID: 7}, "# heading"); err != nil {
		t.Fatalf("Reply returned error: %v", err)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.sendMessageCalls != 2 {
		t.Errorf("expected 2 fallback SendMessage calls, got %d", stub.sendMessageCalls)
	}
}

// An unusable reply context must reach the inherited implementation, which owns
// the error message for it.
func TestRichPlatform_ForeignReplyContextDelegates(t *testing.T) {
	r := &richPlatform{Platform: &Platform{bot: newStubTelegramBot()}}
	if err := r.Send(context.Background(), "not-a-reply-context", "text"); err == nil {
		t.Error("expected the inherited Send to reject a foreign reply context")
	}
}

// With progress_style = "compact" the final answer is delivered by editing the
// streaming preview, so these two methods decide how the result actually looks.
// A client without rich support must still degrade to the inherited HTML path.
func TestRichPlatform_PreviewMethodsFallBack(t *testing.T) {
	stub := newStubTelegramBot()
	r := &richPlatform{Platform: &Platform{bot: stub}}

	handle, err := r.SendPreviewStart(context.Background(), replyContext{chatID: 1}, "# heading")
	if err != nil {
		t.Fatalf("SendPreviewStart returned error: %v", err)
	}
	if _, ok := handle.(*telegramPreviewHandle); !ok {
		t.Fatalf("expected *telegramPreviewHandle, got %T", handle)
	}
	if err := r.UpdateMessage(context.Background(), handle, "# heading\n\nbody"); err != nil {
		t.Fatalf("UpdateMessage returned error: %v", err)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.sendMessageCalls != 1 {
		t.Errorf("expected 1 fallback SendMessage, got %d", stub.sendMessageCalls)
	}
	if stub.editMessageTextCalls == 0 {
		t.Error("expected the inherited HTML edit path to run")
	}
}

// An unusable preview handle must reach the inherited implementation, which
// owns the error message for it.
func TestRichPlatform_ForeignPreviewHandleDelegates(t *testing.T) {
	r := &richPlatform{Platform: &Platform{bot: newStubTelegramBot()}}
	if err := r.UpdateMessage(context.Background(), "not-a-handle", "text"); err == nil {
		t.Error("expected the inherited UpdateMessage to reject a foreign handle")
	}
}

// Regression: core.MessageHandler receives the platform as its first argument,
// and the embedded Platform passes its own receiver, because an embedded type
// cannot know it is embedded. The engine stores that argument and replies
// through it, so without the Start override every reply bypasses this wrapper
// and lands on the plain HTML path — with no error anywhere to show for it.
func TestRichPlatform_StartRebindsHandlerToWrapper(t *testing.T) {
	p := &Platform{
		token: "test-token",
		newBot: func(_ string, _ func(context.Context, *models.Update), _ *http.Client) (telegramBot, *models.User, func(context.Context), error) {
			return newStubTelegramBot(), &models.User{ID: 1, Username: "testbot"}, func(context.Context) {}, nil
		},
	}
	r := &richPlatform{Platform: p}

	var got core.Platform
	if err := r.Start(func(pl core.Platform, _ *core.Message) { got = pl }); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	defer func() { _ = r.Stop() }()

	// Reproduce what the base does on an incoming update: invoke the stored
	// handler with the base receiver.
	p.mu.RLock()
	stored := p.handler
	p.mu.RUnlock()
	if stored == nil {
		t.Fatal("Start did not store a handler")
	}
	stored(p, &core.Message{})

	if got == nil {
		t.Fatal("handler was never invoked")
	}
	if _, ok := got.(*richPlatform); !ok {
		t.Fatalf("handler received %T, want *richPlatform; replies would bypass rich rendering", got)
	}
}
