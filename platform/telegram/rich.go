package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/chenhg5/cc-connect/core"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// The "telegram-rich" platform renders agent replies as Bot API 10.1 Rich
// Messages (June 11, 2026) instead of the simplified HTML subset.
//
// The HTML path can only express <b>, <i>, <s>, <code>, <pre>, <a> and
// <blockquote>, so headings degrade to bold, task lists stay literal "[x]"
// text, and tables are flattened into a preformatted ASCII block. Rich
// Messages carry real headings, tables, lists, task lists and mathematical
// expressions, and raise the size limit to 32768 characters.
//
// Telegram parses the markdown server-side via InputRichMessage.Markdown, so
// the agent's markdown is forwarded as-is and no block builder is needed here.
//
// All four delivery methods are overridden. Reply and Send cover one-shot
// messages, while SendPreviewStart and UpdateMessage cover the streaming
// preview — with progress_style = "compact" the final answer is delivered by
// editing that preview and never reaches Reply, so overriding only Reply
// leaves the visible result entirely on the HTML path.
//
// Every override falls back to the inherited implementation on any failure, so
// an older client, a server-side rejection or an oversized body degrades to
// HTML instead of dropping the message.
//
// This file is deliberately additive: it adds no symbol to and changes no line
// of telegram.go, so it rebases cleanly onto upstream.
func init() {
	core.RegisterPlatform("telegram-rich", NewRich)
}

// richSender is the subset of the Telegram client used for rich messages.
//
// It is asserted at runtime instead of being added to the telegramBot
// interface, which would mean editing telegram.go. Test doubles that do not
// implement it simply fall back to the HTML path.
type richSender interface {
	SendRichMessage(ctx context.Context, params *tgbot.SendRichMessageParams) (*models.Message, error)
}

// Compile-time guarantee that the real client satisfies richSender. Without
// this a signature drift in the library would turn into a silent runtime
// fallback to HTML rather than a build failure.
var _ richSender = (*tgbot.Bot)(nil)

type richPlatform struct {
	*Platform
}

// NewRich builds the rich-rendering Telegram platform. It accepts exactly the
// same options as the plain "telegram" platform.
func NewRich(opts map[string]any) (core.Platform, error) {
	base, err := New(opts)
	if err != nil {
		return nil, err
	}
	p, ok := base.(*Platform)
	if !ok {
		return nil, fmt.Errorf("telegram-rich: unexpected base platform type %T", base)
	}
	return &richPlatform{Platform: p}, nil
}

// Name reports "telegram" so that session keys, routing and stored history stay
// identical to the plain platform; rich rendering is a display variant, not a
// different account.
func (r *richPlatform) Name() string { return "telegram" }

// markdownHardBreaks makes single newlines survive Telegram's server-side
// markdown parsing. CommonMark treats a lone "\n" as a soft break and renders
// it as a space, so bridge-generated line lists (/sessions, /status, tool
// progress) and the agent's own chat-style line breaks collapse into one run.
// The HTML fallback path preserves every "\n" literally; appending a two-space
// hard break restores parity for the rich path. Fenced code blocks are left
// untouched, blank lines already separate paragraphs, and lines that end in a
// hard break (or backslash) are skipped, so the transform is idempotent.
func markdownHardBreaks(md string) string {
	lines := strings.Split(md, "\n")
	inFence := false
	for i := 0; i < len(lines)-1; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || trimmed == "" {
			continue
		}
		if strings.TrimSpace(lines[i+1]) == "" {
			continue
		}
		if strings.HasSuffix(lines[i], "  ") || strings.HasSuffix(lines[i], "\\") {
			continue
		}
		lines[i] += "  "
	}
	return strings.Join(lines, "\n")
}

// sendRich delivers content as a rich message. A nil return means the caller
// should fall back to the inherited HTML path.
func (r *richPlatform) sendRich(ctx context.Context, rc replyContext, content string, replyToMessageID int) *models.Message {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	content = markdownHardBreaks(content)
	bot, err := r.connectedBot("rich send")
	if err != nil {
		slog.Warn("telegram-rich: no connected bot, falling back to HTML", "error", err)
		return nil
	}
	sender, ok := bot.(richSender)
	if !ok {
		slog.Warn("telegram-rich: client lacks SendRichMessage, falling back to HTML",
			"bot_type", fmt.Sprintf("%T", bot))
		return nil
	}
	params := &tgbot.SendRichMessageParams{
		ChatID:          rc.chatID,
		MessageThreadID: rc.threadID,
		RichMessage:     models.InputRichMessage{Markdown: content},
	}
	if replyToMessageID != 0 {
		params.ReplyParameters = &models.ReplyParameters{MessageID: replyToMessageID}
	}
	sent, err := sender.SendRichMessage(ctx, params)
	if err != nil {
		slog.Warn("telegram-rich: SendRichMessage failed, falling back to HTML",
			"error", err, "content_len", len(content))
		return nil
	}
	return sent
}

// Start rebinds the incoming-message handler to this wrapper.
//
// core.MessageHandler receives the platform as its first argument, and the
// embedded Platform passes its own receiver — an embedded type cannot know it
// is embedded. The engine stores that argument and replies through it, so
// without this override every reply goes to the base implementation and all
// the rich overrides below are dead code, even though the engine really does
// hold a *richPlatform.
func (r *richPlatform) Start(handler core.MessageHandler) error {
	return r.Platform.Start(func(_ core.Platform, msg *core.Message) {
		handler(r, msg)
	})
}

func (r *richPlatform) Send(ctx context.Context, rctx any, content string) error {
	if rc, ok := rctx.(replyContext); ok {
		if r.sendRich(ctx, rc, content, 0) != nil {
			return nil
		}
	}
	return r.Platform.Send(ctx, rctx, content)
}

func (r *richPlatform) Reply(ctx context.Context, rctx any, content string) error {
	if rc, ok := rctx.(replyContext); ok {
		if r.sendRich(ctx, rc, content, rc.messageID) != nil {
			return nil
		}
	}
	return r.Platform.Reply(ctx, rctx, content)
}

// SendPreviewStart opens the streaming preview as a rich message, so a reply
// short enough to arrive in a single frame is still rendered richly.
func (r *richPlatform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	if rc, ok := rctx.(replyContext); ok {
		if sent := r.sendRich(ctx, rc, content, 0); sent != nil {
			return &telegramPreviewHandle{chatID: rc.chatID, threadID: rc.threadID, messageID: sent.ID}, nil
		}
	}
	return r.Platform.SendPreviewStart(ctx, rctx, content)
}

// UpdateMessage edits the streaming preview in place. With
// progress_style = "compact" this carries the final answer, so it is the
// override that actually decides how the result looks.
func (r *richPlatform) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	h, ok := previewHandle.(*telegramPreviewHandle)
	if !ok || strings.TrimSpace(content) == "" {
		return r.Platform.UpdateMessage(ctx, previewHandle, content)
	}
	bot, err := r.connectedBot("rich update")
	if err != nil {
		return r.Platform.UpdateMessage(ctx, previewHandle, content)
	}
	if _, ok := bot.(richSender); !ok {
		return r.Platform.UpdateMessage(ctx, previewHandle, content)
	}
	_, err = bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{
		ChatID:      h.chatID,
		MessageID:   h.messageID,
		RichMessage: &models.InputRichMessage{Markdown: markdownHardBreaks(content)},
	})
	if err == nil {
		return nil
	}
	// An unchanged body is not an error; the base implementation swallows it too.
	if strings.Contains(err.Error(), "not modified") {
		return nil
	}
	slog.Warn("telegram-rich: rich edit failed, falling back to HTML",
		"error", err, "content_len", len(content))
	return r.Platform.UpdateMessage(ctx, previewHandle, content)
}
