package telegram

import (
	"context"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"testing"
)

func TestRichMath_TeXDelimitersFromCodex(t *testing.T) {
	in := "Если \\(\\tau=1-t\\), то:\n\n\\[\nE_{\\text{ядра}}\\sim\n\\tau^{1/2-3h}\\longrightarrow0.\n\\]"
	want := "Если $\\tau=1-t$, то:\n\n\n```math\nE_{\\text{ядра}}\\sim\n\\tau^{1/2-3h}\\longrightarrow0.\n```\n"
	if got := prepareRichMarkdown(in); got != want {
		t.Fatalf("formula source corrupted:\ngot  %q\nwant %q", got, want)
	}
}

func TestRichMath_PreservesCodeAndExistingMath(t *testing.T) {
	for _, in := range []string{
		"`\\(x\\)`", "``a ` \\(x\\)``", "```tex\n\\[x\\]\n```",
		"~~~~tex\n\\[x\\]\n~~~~", "```tex\n\\[unfinished", `\\(literal\\)`,
		`$x + \text{\(literal\)}$`, `$$x^2$$`, "```math\nx^2\n```", `\(unfinished`,
	} {
		if got := normalizeRichMath(in); got != in {
			t.Errorf("changed literal %q to %q", in, got)
		}
	}
}

func TestRichMath_InlineAndDisplayAreIdempotent(t *testing.T) {
	in := `Before \(x^2\) and \[y^2\] after.`
	once := prepareRichMarkdown(in)
	if got := prepareRichMarkdown(once); got != once {
		t.Fatalf("not idempotent: %q != %q", got, once)
	}
}

type mathRichBot struct {
	*stubTelegramBot
	sent, edited string
}

func (b *mathRichBot) SendRichMessage(_ context.Context, p *tgbot.SendRichMessageParams) (*models.Message, error) {
	b.sent = p.RichMessage.Markdown
	return &models.Message{ID: 42}, nil
}
func (b *mathRichBot) EditMessageText(_ context.Context, p *tgbot.EditMessageTextParams) (*models.Message, error) {
	if p.RichMessage != nil {
		b.edited = p.RichMessage.Markdown
	}
	return &models.Message{ID: 42}, nil
}
func TestRichPlatform_NormalizesMathInSendAndPreviewEdit(t *testing.T) {
	b := &mathRichBot{stubTelegramBot: newStubTelegramBot()}
	r := &richPlatform{Platform: &Platform{bot: b}}
	ctx := context.Background()
	rc := replyContext{chatID: 1, messageID: 7}
	in, want := `Energy \(E=mc^2\)`, `Energy $E=mc^2$`
	if err := r.Send(ctx, rc, in); err != nil {
		t.Fatal(err)
	}
	if b.sent != want {
		t.Fatalf("Send math = %q, want %q", b.sent, want)
	}
	if err := r.Reply(ctx, rc, in); err != nil {
		t.Fatal(err)
	}
	if b.sent != want {
		t.Fatalf("Reply math = %q", b.sent)
	}
	handle, err := r.SendPreviewStart(ctx, rc, in)
	if err != nil {
		t.Fatal(err)
	}
	if b.sent != want {
		t.Fatalf("Preview math = %q", b.sent)
	}
	if err := r.UpdateMessage(ctx, handle, in); err != nil {
		t.Fatal(err)
	}
	if b.edited != want {
		t.Fatalf("Edit math = %q, want %q", b.edited, want)
	}
}
