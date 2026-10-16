package telegram

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"
)

// chatLog keeps an append-only JSONL cache of every message the bot sees in
// allowed chats and remembers group messages that arrived since the bot was
// last addressed, so they can be prepended to the next agent prompt.
//
// Options (telegram / telegram-rich platform):
//   - allow_chats        comma-separated chat IDs; messages from other chats
//     (including private chats) are ignored entirely.
//   - chat_log_dir       directory for <chat_id>.jsonl message logs.
//   - context_inject_max max number of pending group messages prepended to a
//     directed message (0 disables injection).
//   - ignore_file        JSON {"<user_id>": {"until": RFC3339, "reason": "…"}}
//     maintained by the agent itself; messages addressed to the bot by a user
//     whose "until" is in the future are logged but not dispatched.
//   - ignore_exempt      comma-separated user IDs that can never be ignored.
type chatLog struct {
	allowChats   map[int64]bool
	dir          string
	injectMax    int
	ignoreFile   string
	ignoreExempt map[int64]bool

	mu      sync.Mutex
	pending map[int64][]string
}

const chatLogMaxTextLen = 4000

func newChatLog(opts map[string]any) (*chatLog, error) {
	cl := &chatLog{pending: map[int64][]string{}}
	if raw, _ := opts["allow_chats"].(string); strings.TrimSpace(raw) != "" {
		cl.allowChats = map[int64]bool{}
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("telegram: invalid allow_chats entry %q: %w", part, err)
			}
			cl.allowChats[id] = true
		}
	}
	if dir, _ := opts["chat_log_dir"].(string); strings.TrimSpace(dir) != "" {
		cl.dir = strings.TrimSpace(dir)
		if err := os.MkdirAll(cl.dir, 0o755); err != nil {
			return nil, fmt.Errorf("telegram: chat_log_dir: %w", err)
		}
	}
	cl.ignoreFile = strings.TrimSpace(fmt.Sprint(opts["ignore_file"]))
	if cl.ignoreFile == "<nil>" {
		cl.ignoreFile = ""
	}
	if raw, _ := opts["ignore_exempt"].(string); strings.TrimSpace(raw) != "" {
		cl.ignoreExempt = map[int64]bool{}
		for _, part := range strings.Split(raw, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
				cl.ignoreExempt[id] = true
			}
		}
	}
	switch v := opts["context_inject_max"].(type) {
	case int64:
		cl.injectMax = int(v)
	case int:
		cl.injectMax = v
	case float64:
		cl.injectMax = int(v)
	}
	return cl, nil
}

func (cl *chatLog) chatAllowed(chatID int64) bool {
	return cl == nil || cl.allowChats == nil || cl.allowChats[chatID]
}

type chatLogEntry struct {
	ID       int    `json:"id"`
	Date     string `json:"date"`
	FromID   int64  `json:"from_id"`
	From     string `json:"from"`
	Username string `json:"username,omitempty"`
	Kind     string `json:"kind"`
	Text     string `json:"text,omitempty"`
	ReplyTo  int    `json:"reply_to,omitempty"`
	ToBot    bool   `json:"to_bot,omitempty"`
}

func messageKind(msg *models.Message) string {
	switch {
	case len(msg.Photo) > 0:
		return "photo"
	case msg.Video != nil:
		return "video"
	case msg.VideoNote != nil:
		return "video_note"
	case msg.Voice != nil:
		return "voice"
	case msg.Audio != nil:
		return "audio"
	case msg.Document != nil:
		return "document"
	case msg.Sticker != nil:
		return "sticker"
	case msg.Poll != nil:
		return "poll"
	case msg.Location != nil:
		return "location"
	}
	return "text"
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// record logs msg and, when it is not addressed to the bot, queues it for
// injection into the next directed prompt.
func (cl *chatLog) record(msg *models.Message, from string, directed bool) {
	if cl == nil || (cl.dir == "" && cl.injectMax <= 0) {
		return
	}
	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	if msg.Sticker != nil && text == "" {
		text = msg.Sticker.Emoji
	}
	if msg.Poll != nil && text == "" {
		text = msg.Poll.Question
	}
	e := chatLogEntry{
		ID:     msg.ID,
		Date:   time.Unix(int64(msg.Date), 0).Format(time.RFC3339),
		FromID: msg.From.ID,
		From:   strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName),
		Kind:   messageKind(msg),
		Text:   truncateRunes(text, chatLogMaxTextLen),
		ToBot:  directed,
	}
	if msg.From.Username != "" {
		e.Username = msg.From.Username
	}
	if msg.ReplyToMessage != nil {
		e.ReplyTo = msg.ReplyToMessage.ID
	}
	if cl.dir != "" {
		cl.appendLine(msg.Chat.ID, e)
	}
	isGroup := msg.Chat.Type == models.ChatTypeGroup || msg.Chat.Type == models.ChatTypeSupergroup
	if directed || cl.injectMax <= 0 || !isGroup {
		return
	}
	line := fmt.Sprintf("[%s #%d] %s", time.Unix(int64(msg.Date), 0).Format("15:04"), msg.ID, from)
	if e.Username != "" {
		line += " (@" + e.Username + ")"
	}
	if e.ReplyTo != 0 {
		line += fmt.Sprintf(" ↩#%d", e.ReplyTo)
	}
	if e.Kind != "text" {
		line += " [" + e.Kind + "]"
	}
	line += ": " + strings.ReplaceAll(truncateRunes(text, 1500), "\n", " ⏎ ")
	cl.mu.Lock()
	q := append(cl.pending[msg.Chat.ID], line)
	if len(q) > cl.injectMax {
		q = q[len(q)-cl.injectMax:]
	}
	cl.pending[msg.Chat.ID] = q
	cl.mu.Unlock()
}

func (cl *chatLog) appendLine(chatID int64, e chatLogEntry) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	path := filepath.Join(cl.dir, strconv.FormatInt(chatID, 10)+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("telegram: chat log write failed", "error", err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// takePending returns and clears the queued context block for chatID.
func (cl *chatLog) takePending(chatID int64) string {
	if cl == nil || cl.injectMax <= 0 {
		return ""
	}
	cl.mu.Lock()
	q := cl.pending[chatID]
	delete(cl.pending, chatID)
	cl.mu.Unlock()
	if len(q) == 0 {
		return ""
	}
	return "[Сообщения в чате с прошлого обращения к боту]\n" + strings.Join(q, "\n") + "\n[Конец; ниже — обращение к боту]"
}

// ignored reports whether userID is on the agent-maintained ignore list.
// A missing or malformed file ignores nobody.
func (cl *chatLog) ignored(userID int64) bool {
	if cl == nil || cl.ignoreFile == "" || cl.ignoreExempt[userID] {
		return false
	}
	data, err := os.ReadFile(cl.ignoreFile)
	if err != nil {
		return false
	}
	var list map[string]struct {
		Until string `json:"until"`
	}
	if json.Unmarshal(data, &list) != nil {
		slog.Warn("telegram: ignore_file is not valid JSON", "path", cl.ignoreFile)
		return false
	}
	entry, ok := list[strconv.FormatInt(userID, 10)]
	if !ok {
		return false
	}
	raw := strings.TrimSpace(entry.Until)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-0700", "2006-01-02 15:04:05-0700"} {
		if until, err := time.Parse(layout, raw); err == nil {
			return time.Now().Before(until)
		}
	}
	return false
}
