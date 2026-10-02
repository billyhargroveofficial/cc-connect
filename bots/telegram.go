package bots

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

var telegramEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var telegramTokenFormat = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
var telegramErrorURL = regexp.MustCompile(`https?://[^\s]+`)
var errTelegramStatusUnchanged = errors.New("Telegram status unchanged")

type telegramRuntime interface {
	SendMessage(context.Context, string, MessageRequest) (string, error)
	WaitTurn(context.Context, string, string) (TurnResult, error)
	Permission(context.Context, string, string, core.PermissionResult) error
}

type telegramWorkspace interface {
	StoreUpload(string, string, string, []byte) (Attachment, error)
	ResolveAttachments(string, []Attachment) ([]Attachment, error)
	Transcribe(context.Context, []byte, string) (string, error)
}

// Delivery state belongs to one accepted Telegram turn, so a replay never
// duplicates a file and no recipient is inferred for turns started on the web.
type telegramArtifactDelivery struct {
	events   map[uint64]bool
	files    map[string]bool
	captions map[string]bool
}

// TelegramManager is an optional surface over the product runtime, rather than
// a second engine with separate agent sessions. Sync is called at startup and
// after an owner changes a bot profile. It never imports existing bot tokens.
type TelegramManager struct {
	store   *Store
	runtime telegramRuntime
	ctx     context.Context
	cancel  context.CancelFunc
	syncMu  sync.Mutex
	mu      sync.Mutex
	entries map[string]*telegramConnection
	closed  bool
	// These seams let tests exercise lifecycle and message routing without
	// connecting to Telegram, invoking Flov, or creating a real agent session.
	factory   core.PlatformFactory
	getenv    func(string) string
	workspace telegramWorkspace
}

type telegramConnection struct {
	manager  *TelegramManager
	botID    string
	config   telegramConfig
	platform core.Platform
	ctx      context.Context
	cancel   context.CancelFunc
	closed   atomic.Bool
	busy     atomic.Bool
	mu       sync.Mutex
	// Readiness fields are protected by manager.mu, including refreshes.
	state           string
	connectionError string
	pending         map[string]*telegramPermission
	seen            map[string]struct{}
	accepted        map[string]struct{}
	order           []string
}

type telegramPermission struct {
	requestID string
	turnID    string
	session   string
	userID    string
	replyCtx  any
	toolInput map[string]any
	questions []core.UserQuestion
	question  int
	answers   map[string]any
}

func NewTelegramManager(store *Store, runtime *Runtime) *TelegramManager {
	return newTelegramManager(store, runtime)
}

func newTelegramManager(store *Store, runtime telegramRuntime) *TelegramManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &TelegramManager{
		store: store, runtime: runtime, ctx: ctx, cancel: cancel,
		entries: make(map[string]*telegramConnection), getenv: os.Getenv,
		factory:   func(opts map[string]any) (core.Platform, error) { return core.CreatePlatform("telegram", opts) },
		workspace: NewWorkspace(store),
	}
}

// SetWorkspace shares the product's configured Flov endpoint and upload policy.
func (m *TelegramManager) SetWorkspace(workspace *Workspace) {
	if workspace == nil {
		return
	}
	m.mu.Lock()
	m.workspace = workspace
	m.mu.Unlock()
}

// SetEnvironmentResolver limits Telegram credentials to the deployment's
// approved scope. A nil resolver denies environment lookup; account workspaces
// must not be able to select a host owner's credential by its variable name.
func (m *TelegramManager) SetEnvironmentResolver(resolver func(string) string) {
	if resolver == nil {
		resolver = func(string) string { return "" }
	}
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	m.mu.Lock()
	m.getenv = resolver
	m.mu.Unlock()
}

func (m *TelegramManager) Sync(ctx context.Context) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return os.ErrClosed
	}
	bots := m.store.ListBots()
	configs := make(map[string]telegramConfig)
	invalid := make(map[string]error)
	credentials := make(map[[32]byte][]string)
	for _, bot := range bots {
		if bot.Status == "archived" || bot.Telegram == nil || !bot.Telegram.Enabled {
			continue
		}
		cfg, err := resolveTelegramConfig(bot.Telegram, m.getenv)
		if err != nil {
			invalid[bot.ID] = err
			continue
		}
		configs[bot.ID] = cfg
		digest := sha256.Sum256([]byte(cfg.token))
		credentials[digest] = append(credentials[digest], bot.ID)
	}
	for _, ids := range credentials {
		if len(ids) > 1 {
			for _, id := range ids {
				invalid[id] = errors.New("a Telegram credential must belong to only one bot")
			}
		}
	}
	// Stop obsolete connections before starting replacements. The cancellation
	// also detaches reply waiters, but does not cancel the shared runtime's turn.
	m.mu.Lock()
	var stopped []*telegramConnection
	for id, entry := range m.entries {
		cfg, exists := configs[id]
		if exists && invalid[id] == nil && cfg.identity == entry.config.identity {
			continue
		}
		entry.closed.Store(true)
		entry.cancel()
		delete(m.entries, id)
		stopped = append(stopped, entry)
	}
	m.mu.Unlock()
	var syncErrors []error
	for _, entry := range stopped {
		if err := entry.platform.Stop(); err != nil {
			message := sanitizeTelegramError(err, entry.config.token)
			slog.Warn("connect-bots: stop Telegram connection", "bot", entry.botID, "error", message)
			syncErrors = append(syncErrors, errors.New(message))
		}
	}
	for _, bot := range bots {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(syncErrors, err)...)
		}
		if bot.Telegram == nil {
			continue
		}
		if bot.Status == "archived" || !bot.Telegram.Enabled {
			m.bindingStatus(bot.ID, bot.Telegram, "disabled", "")
			continue
		}
		if err := invalid[bot.ID]; err != nil {
			message := sanitizeTelegramError(err, configs[bot.ID].token)
			m.bindingStatus(bot.ID, bot.Telegram, "error", message)
			syncErrors = append(syncErrors, fmt.Errorf("bot %s: %s", bot.ID, message))
			continue
		}
		cfg := configs[bot.ID]
		m.mu.Lock()
		existing := m.entries[bot.ID]
		m.mu.Unlock()
		if existing != nil {
			existing.refreshStatus()
			continue
		}
		if err := m.connect(bot, cfg); err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("bot %s: %s", bot.ID, sanitizeTelegramError(err, cfg.token)))
		}
	}
	return errors.Join(syncErrors...)
}

func (m *TelegramManager) connect(bot Bot, cfg telegramConfig) error {
	platform, err := m.factory(map[string]any{"token": cfg.token, "allow_from": strings.Join(cfg.users, ","), "group_reply_all": false})
	if platform == nil && err == nil {
		err = errors.New("Telegram adapter did not create a connection")
	}
	if err != nil {
		m.bindingStatus(bot.ID, bot.Telegram, "error", sanitizeTelegramError(err, cfg.token))
		return err
	}
	ctx, cancel := context.WithCancel(m.ctx)
	entry := &telegramConnection{
		manager: m, botID: bot.ID, config: cfg, platform: platform, ctx: ctx, cancel: cancel,
		state: "connecting", pending: make(map[string]*telegramPermission), seen: make(map[string]struct{}), accepted: make(map[string]struct{}),
	}
	m.mu.Lock()
	m.entries[bot.ID] = entry
	m.mu.Unlock()
	m.bindingStatus(bot.ID, bot.Telegram, "connecting", "")
	async, asynchronous := platform.(core.AsyncRecoverablePlatform)
	if asynchronous {
		async.SetLifecycleHandler(entry)
	}
	if err := platform.Start(entry.receive); err != nil {
		entry.closed.Store(true)
		entry.cancel()
		m.mu.Lock()
		delete(m.entries, bot.ID)
		m.mu.Unlock()
		if stopErr := platform.Stop(); stopErr != nil {
			slog.Warn("connect-bots: stop failed Telegram adapter", "bot", bot.ID, "error", sanitizeTelegramError(stopErr, cfg.token))
		}
		m.bindingStatus(bot.ID, bot.Telegram, "error", sanitizeTelegramError(err, cfg.token))
		return err
	}
	if !asynchronous {
		entry.OnPlatformReady(platform)
	}
	return nil
}

func (m *TelegramManager) bindingStatus(botID string, expected *TelegramBinding, status, message string) {
	if expected == nil {
		return
	}
	_, err := m.store.UpdateBot(botID, func(bot *Bot) error {
		binding := bot.Telegram
		if binding == nil || binding.Enabled != expected.Enabled || binding.TokenEnv != expected.TokenEnv || strings.Join(binding.AllowedUserIDs, ",") != strings.Join(expected.AllowedUserIDs, ",") {
			return errTelegramStatusUnchanged
		}
		if binding.Status == status && binding.Error == message {
			return errTelegramStatusUnchanged
		}
		binding.Status, binding.Error = status, message
		return nil
	})
	if err != nil && !errors.Is(err, errTelegramStatusUnchanged) {
		slog.Warn("connect-bots: persist Telegram status", "bot", botID, "error", err)
		return
	}
	if message != "" && err == nil {
		m.system(botID, "", "Telegram: "+message)
	}
}

func (entry *telegramConnection) OnPlatformReady(_ core.Platform) {
	entry.status("connected", "")
}

func (entry *telegramConnection) OnPlatformUnavailable(_ core.Platform, err error) {
	entry.status("error", sanitizeTelegramError(err, entry.config.token))
}

func (entry *telegramConnection) status(status, message string) {
	entry.manager.mu.Lock()
	defer entry.manager.mu.Unlock()
	if entry.closed.Load() || entry.manager.entries[entry.botID] != entry {
		return
	}
	entry.state, entry.connectionError = status, message
	entry.publishStatusLocked()
}

func (entry *telegramConnection) refreshStatus() {
	entry.manager.mu.Lock()
	defer entry.manager.mu.Unlock()
	if entry.closed.Load() || entry.manager.entries[entry.botID] != entry {
		return
	}
	entry.publishStatusLocked()
}

func (entry *telegramConnection) publishStatusLocked() {
	bot, err := entry.manager.store.GetBot(entry.botID)
	if err != nil || bot.Telegram == nil {
		return
	}
	cfg, err := resolveTelegramConfig(bot.Telegram, entry.manager.getenv)
	if err != nil || cfg.identity != entry.config.identity || bot.Status == "archived" {
		return
	}
	entry.manager.bindingStatus(entry.botID, bot.Telegram, entry.state, entry.connectionError)
}

func (m *TelegramManager) system(botID, turnID, message string) {
	if _, err := m.store.AppendEvent(botID, turnID, "system", map[string]any{"content": message, "source": "telegram"}); err != nil {
		slog.Warn("connect-bots: persist Telegram event", "bot", botID, "error", err)
	}
}

func (entry *telegramConnection) receive(_ core.Platform, msg *core.Message) {
	if msg == nil || msg.Recalled || entry.closed.Load() {
		return
	}
	if _, allowed := entry.config.allowed[msg.UserID]; !allowed {
		return // Defense in depth: the platform's allow-list is not the only gate.
	}
	if msg.IsPermissionResponse {
		return // Generic legacy callbacks carry no request identity; drop stale ones.
	}
	if strings.HasPrefix(msg.Content, "askq:") {
		go entry.permissionCallback(msg)
		return
	}
	if entry.answerQuestion(msg) {
		return
	}
	key := msg.SessionKey + "\x00" + msg.MessageID
	entry.mu.Lock()
	_, duplicate := entry.accepted[key]
	entry.mu.Unlock()
	if msg.MessageID != "" && duplicate {
		return
	}
	if runtime, ok := entry.manager.runtime.(interface{ Busy(string) bool }); ok && runtime.Busy(entry.botID) {
		go entry.reply(msg.ReplyCtx, "The bot is busy. Wait for its reply or stop it in Connect Bots.")
		return
	}
	if !entry.busy.CompareAndSwap(false, true) {
		go entry.reply(msg.ReplyCtx, "The bot is still working on the previous task. Wait for its reply or stop it in Connect Bots.")
		return
	}
	// Adapter handlers must return promptly so polling can receive approvals.
	go entry.runMessage(msg, key)
}

func (entry *telegramConnection) runMessage(msg *core.Message, key string) {
	defer entry.busy.Store(false)
	request, err := entry.prepareMessage(msg)
	if err != nil {
		entry.failure("", msg.ReplyCtx, "Failed to read the message", err)
		return
	}
	// Subscribe before sending: an agent can ask for permission immediately.
	cursor := entry.manager.store.Cursor()
	startedAt := cursor
	delivery := &telegramArtifactDelivery{events: make(map[uint64]bool), files: make(map[string]bool), captions: make(map[string]bool)}
	stream, unsubscribe, err := entry.manager.store.Subscribe(cursor)
	if err != nil {
		entry.failure("", msg.ReplyCtx, "Failed to connect to the conversation history", err)
		return
	}
	defer func() { unsubscribe() }()
	turnID, err := entry.manager.runtime.SendMessage(entry.ctx, entry.botID, request)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			entry.reply(msg.ReplyCtx, "The bot is busy. Wait for its reply or stop it in Connect Bots.")
			return
		}
		entry.failure("", msg.ReplyCtx, "Failed to start the task", err)
		return
	}
	entry.acceptMessage(key, msg)
	defer entry.clearPermissions(turnID)
	var stopTyping func()
	if indicator, ok := entry.platform.(core.TypingIndicator); ok {
		stopTyping = indicator.StartTyping(entry.ctx, msg.ReplyCtx)
	}
	if stopTyping != nil {
		defer stopTyping()
	}
	type completedTurn struct {
		result TurnResult
		err    error
	}
	completed := make(chan completedTurn, 1)
	go func() {
		result, waitErr := entry.manager.runtime.WaitTurn(entry.ctx, entry.botID, turnID)
		completed <- completedTurn{result: result, err: waitErr}
	}()
	for {
		select {
		case <-entry.ctx.Done():
			return
		case event, open := <-stream:
			if !open {
				unsubscribe()
				nextStream, nextUnsubscribe, subscribeErr := entry.manager.store.Subscribe(cursor)
				if subscribeErr != nil {
					entry.failure(turnID, msg.ReplyCtx, "Connection to the conversation history was lost", subscribeErr)
					return
				}
				stream, unsubscribe = nextStream, nextUnsubscribe
				continue
			}
			cursor = event.Seq
			if event.BotID == entry.botID && event.TurnID == turnID {
				if event.Type == "agent" {
					entry.permissionEvent(event, msg)
				} else if event.Type == "permission" {
					entry.permissionResolved(event)
				} else if event.Type == "message" {
					entry.deliverArtifact(event, msg.ReplyCtx, delivery)
				}
			}
		case done := <-completed:
			if done.err != nil {
				if entry.ctx.Err() == nil {
					entry.failure(turnID, msg.ReplyCtx, "Failed to get the reply", done.err)
				}
				return
			}
			// Completion and the event stream can become ready together. Replay
			// the durable turn before sending its final answer, so fast turns do
			// not lose their last published file to select scheduling.
			entry.flushArtifacts(turnID, startedAt, msg.ReplyCtx, delivery)
			text := strings.TrimSpace(done.result.Text)
			if done.result.Error != "" {
				entry.failure(turnID, msg.ReplyCtx, "The task ended with an error", errors.New(done.result.Error))
			}
			if text != "" && !delivery.captions[text] {
				entry.reply(msg.ReplyCtx, text)
			} else if done.result.Status == "interrupted" {
				entry.reply(msg.ReplyCtx, "The task was stopped.")
			}
			return
		}
	}
}

func (entry *telegramConnection) acceptMessage(key string, msg *core.Message) {
	if msg.MessageID != "" {
		entry.mu.Lock()
		entry.accepted[key] = struct{}{}
		entry.order = append(entry.order, key)
		if len(entry.order) > 256 {
			delete(entry.accepted, entry.order[0])
			entry.order = entry.order[1:]
		}
		entry.mu.Unlock()
	}
	if msg.OnAccepted != nil {
		msg.OnAccepted()
	}
}

func (entry *telegramConnection) prepareMessage(msg *core.Message) (MessageRequest, error) {
	entry.manager.mu.Lock()
	workspace := entry.manager.workspace
	entry.manager.mu.Unlock()
	request := MessageRequest{Text: strings.TrimSpace(strings.Join([]string{msg.ExtraContent, msg.Content}, "\n\n")), Source: "telegram"}
	store := func(name, mimeType string, data []byte) error {
		attachment, err := workspace.StoreUpload(entry.botID, name, mimeType, data)
		if err == nil {
			request.Attachments = append(request.Attachments, attachment)
		}
		return err
	}
	for i, image := range msg.Images {
		name := image.FileName
		if name == "" {
			name = fmt.Sprintf("photo-%d%s", i+1, telegramImageExtension(image.MimeType))
		}
		if err := store(name, image.MimeType, image.Data); err != nil {
			return MessageRequest{}, err
		}
	}
	for _, file := range msg.Files {
		if err := store(file.FileName, file.MimeType, file.Data); err != nil {
			return MessageRequest{}, err
		}
	}
	if msg.Audio != nil {
		format := strings.ToLower(strings.TrimSpace(msg.Audio.Format))
		if format == "" {
			format = "ogg"
		}
		if err := store("voice."+format, msg.Audio.MimeType, msg.Audio.Data); err != nil {
			return MessageRequest{}, err
		}
		ctx, cancel := context.WithTimeout(entry.ctx, 2*time.Minute)
		defer cancel()
		transcript, err := workspace.Transcribe(ctx, msg.Audio.Data, format)
		if err != nil {
			return MessageRequest{}, fmt.Errorf("voice transcription: %w", err)
		}
		if strings.TrimSpace(transcript) == "" {
			return MessageRequest{}, errors.New("no speech was recognized in the voice message")
		}
		request.Text = strings.TrimSpace(request.Text + "\n\n" + transcript)
	}
	return request, nil
}

func telegramImageExtension(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/heic", "image/heif":
		return ".heic"
	default:
		return ".jpg"
	}
}

func (entry *telegramConnection) reply(replyCtx any, content string) error {
	if entry.closed.Load() || entry.ctx.Err() != nil {
		return context.Canceled
	}
	ctx, cancel := context.WithTimeout(entry.ctx, 30*time.Second)
	defer cancel()
	if err := entry.platform.Reply(ctx, replyCtx, content); err != nil {
		entry.manager.system(entry.botID, "", "Telegram reply failed: "+sanitizeTelegramError(err, entry.config.token))
		return err
	}
	return nil
}

func (entry *telegramConnection) flushArtifacts(turnID string, after uint64, replyCtx any, delivery *telegramArtifactDelivery) {
	events, err := entry.manager.store.Events(entry.botID, after)
	if err != nil {
		entry.failure(turnID, replyCtx, "Failed to get output files", err)
		return
	}
	for _, event := range events {
		if event.TurnID == turnID && event.Type == "message" {
			entry.deliverArtifact(event, replyCtx, delivery)
		}
	}
}

func (entry *telegramConnection) deliverArtifact(event Event, replyCtx any, delivery *telegramArtifactDelivery) {
	if delivery.events[event.Seq] || entry.closed.Load() {
		return
	}
	var message struct {
		Role        string       `json:"role"`
		Content     string       `json:"content"`
		Attachments []Attachment `json:"attachments"`
	}
	if json.Unmarshal(event.Data, &message) != nil || message.Role != "assistant" || len(message.Attachments) == 0 {
		return
	}
	delivery.events[event.Seq] = true
	entry.manager.mu.Lock()
	workspace := entry.manager.workspace
	entry.manager.mu.Unlock()
	// Resolve opaque IDs inside this bot's upload directory. Paths, filenames,
	// URLs and MIME types from event data are not filesystem authority.
	attachments, err := workspace.ResolveAttachments(entry.botID, message.Attachments)
	if err != nil {
		entry.failure(event.TurnID, replyCtx, "Failed to read output files", err)
		return
	}
	delivered := false
	for _, attachment := range attachments {
		if delivery.files[attachment.ID] {
			delivered = true
			continue
		}
		if err := entry.sendArtifact(replyCtx, attachment); err != nil {
			entry.failure(event.TurnID, replyCtx, "Failed to send file \""+attachment.Name+"\"", err)
			continue
		}
		delivery.files[attachment.ID], delivered = true, true
	}
	caption := strings.TrimSpace(message.Content)
	if delivered && caption != "" && !delivery.captions[caption] {
		// Legacy ImageSender/FileSender expose binary payloads without captions.
		// Keep one normal text reply alongside the files and suppress an equal
		// final answer, rather than modifying existing platform adapters.
		if entry.reply(replyCtx, caption) == nil {
			delivery.captions[caption] = true
		}
	}
}

func (entry *telegramConnection) sendArtifact(replyCtx any, attachment Attachment) error {
	file, err := os.Open(attachment.Path)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	if len(data) > maxUploadBytes {
		return errors.New("file exceeds the 25 MiB transfer limit")
	}
	ctx, cancel := context.WithTimeout(entry.ctx, 90*time.Second)
	defer cancel()
	// Ordinary raster images render inline. Other image formats, animations,
	// videos and documents retain their original bytes as Telegram documents.
	if telegramInlineImage(attachment.MimeType, data) {
		if sender, ok := entry.platform.(core.ImageSender); ok {
			return sender.SendImage(ctx, replyCtx, core.ImageAttachment{MimeType: attachment.MimeType, FileName: attachment.Name, Data: data})
		}
	}
	if sender, ok := entry.platform.(core.FileSender); ok {
		return sender.SendFile(ctx, replyCtx, core.FileAttachment{MimeType: attachment.MimeType, FileName: attachment.Name, Data: data})
	}
	return fmt.Errorf("%w: this Telegram surface cannot send files", core.ErrNotSupported)
}

func telegramInlineImage(mimeType string, data []byte) bool {
	// https://core.telegram.org/bots/api#sendphoto. Larger or unusually shaped
	// images go through SendFile without resizing or discarding their bytes.
	if (mimeType != "image/jpeg" && mimeType != "image/png") || len(data) > 10_000_000 {
		return false
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	return err == nil && config.Width > 0 && config.Height > 0 && config.Width+config.Height <= 10_000 && config.Width <= 20*config.Height && config.Height <= 20*config.Width
}

func (entry *telegramConnection) failure(turnID string, replyCtx any, prefix string, err error) {
	if entry.ctx.Err() != nil {
		return
	}
	message := prefix + ": " + sanitizeTelegramError(err, entry.config.token)
	entry.manager.system(entry.botID, turnID, message)
	entry.reply(replyCtx, message)
}

func (entry *telegramConnection) permissionEvent(event Event, msg *core.Message) {
	var request struct {
		Type         string              `json:"type"`
		RequestID    string              `json:"requestId"`
		ToolName     string              `json:"toolName"`
		ToolInput    string              `json:"toolInput"`
		ToolInputRaw map[string]any      `json:"toolInputRaw"`
		Content      string              `json:"content"`
		Questions    []core.UserQuestion `json:"questions"`
	}
	if json.Unmarshal(event.Data, &request) != nil || request.Type != string(core.EventPermissionRequest) || request.RequestID == "" {
		return
	}
	key := event.TurnID + "\x00" + request.RequestID
	entry.mu.Lock()
	_, seen := entry.seen[key]
	entry.seen[key] = struct{}{}
	entry.mu.Unlock()
	if seen {
		return
	}
	permission := &telegramPermission{
		requestID: request.RequestID, turnID: event.TurnID, session: msg.SessionKey, userID: msg.UserID,
		replyCtx: msg.ReplyCtx, toolInput: request.ToolInputRaw, questions: request.Questions, answers: make(map[string]any),
	}
	content := "Permission required: " + request.ToolName
	if request.ToolInput != "" {
		content += "\n\n" + request.ToolInput
	} else if request.Content != "" {
		content += "\n\n" + request.Content
	}
	entry.presentPermission(permission, content)
}

func (entry *telegramConnection) presentPermission(permission *telegramPermission, content string) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		entry.failure(permission.turnID, permission.replyCtx, "Failed to show the permission request", err)
		return
	}
	nonce := hex.EncodeToString(random[:])
	button := func(label, action string) core.ButtonOption {
		// The existing adapter displays the clicked label and forwards askq data.
		// The nonce binds this callback to a particular request and presentation.
		return core.ButtonOption{Text: label, Data: "askq:" + nonce + ":" + action}
	}
	buttons := [][]core.ButtonOption{{button("Allow", "allow"), button("Deny", "deny")}}
	if len(permission.questions) > 0 {
		question := permission.questions[permission.question]
		content = question.Question
		if len(permission.questions) > 1 {
			content = fmt.Sprintf("Question %d of %d\n\n%s", permission.question+1, len(permission.questions), content)
		}
		buttons = nil
		for i, option := range question.Options {
			if option.Description != "" {
				content += "\n\n" + option.Label + ": " + option.Description
			}
			if !question.MultiSelect {
				buttons = append(buttons, []core.ButtonOption{button(option.Label, strconv.Itoa(i))})
			}
		}
		content += "\n\nYou can type your answer."
		if question.MultiSelect {
			content += " Separate options with commas."
		}
		buttons = append(buttons, []core.ButtonOption{button("Cancel", "deny")})
	}
	entry.mu.Lock()
	if entry.closed.Load() {
		entry.mu.Unlock()
		return
	}
	entry.pending[nonce] = permission
	entry.mu.Unlock()
	ctx, cancel := context.WithTimeout(entry.ctx, 30*time.Second)
	defer cancel()
	if len([]rune(content)) > 3500 {
		content = string([]rune(content)[:3500]) + "…"
	}
	var err error
	if sender, ok := entry.platform.(core.InlineButtonSender); ok {
		err = sender.SendWithButtons(ctx, permission.replyCtx, content, buttons)
	} else {
		content += "\n\nOpen Connect Bots to respond to the request."
		err = entry.platform.Reply(ctx, permission.replyCtx, content)
	}
	if err != nil {
		entry.manager.system(entry.botID, permission.turnID, "Telegram permission prompt failed: "+sanitizeTelegramError(err, entry.config.token))
	}
}

func (entry *telegramConnection) permissionCallback(msg *core.Message) {
	parts := strings.Split(msg.Content, ":")
	if len(parts) != 3 || parts[0] != "askq" {
		return
	}
	nonce, action := parts[1], parts[2]
	entry.mu.Lock()
	permission := entry.pending[nonce]
	if permission == nil || permission.userID != msg.UserID || permission.session != msg.SessionKey {
		entry.mu.Unlock()
		return
	}
	answer := ""
	switch {
	case action == "deny":
	case action == "allow" && len(permission.questions) == 0:
	case len(permission.questions) > 0:
		index, err := strconv.Atoi(action)
		question := permission.questions[permission.question]
		if err != nil || index < 0 || index >= len(question.Options) || question.MultiSelect {
			entry.mu.Unlock()
			return
		}
		answer = question.Options[index].Label
	default:
		entry.mu.Unlock()
		return
	}
	delete(entry.pending, nonce)
	entry.mu.Unlock()
	if action == "deny" {
		entry.resolvePermission(permission, core.PermissionResult{Behavior: "deny", Message: "Declined in Telegram"})
	} else if len(permission.questions) == 0 {
		entry.resolvePermission(permission, core.PermissionResult{Behavior: "allow", UpdatedInput: permission.toolInput})
	} else {
		entry.provideAnswer(permission, answer)
	}
}

func (entry *telegramConnection) answerQuestion(msg *core.Message) bool {
	if strings.TrimSpace(msg.Content) == "" || len(msg.Images) > 0 || len(msg.Files) > 0 || msg.Audio != nil {
		return false
	}
	entry.mu.Lock()
	var permission *telegramPermission
	for nonce, candidate := range entry.pending {
		if len(candidate.questions) > 0 && candidate.userID == msg.UserID && candidate.session == msg.SessionKey {
			permission = candidate
			delete(entry.pending, nonce)
			break
		}
	}
	entry.mu.Unlock()
	if permission == nil {
		return false
	}
	go entry.provideAnswer(permission, strings.TrimSpace(msg.Content))
	return true
}

func (entry *telegramConnection) provideAnswer(permission *telegramPermission, answer string) {
	question := permission.questions[permission.question]
	if question.MultiSelect {
		var values []string
		for _, value := range strings.Split(answer, ",") {
			if value = strings.TrimSpace(value); value != "" {
				values = append(values, value)
			}
		}
		permission.answers[question.Question] = values
	} else {
		permission.answers[question.Question] = answer
	}
	permission.question++
	if permission.question < len(permission.questions) {
		entry.presentPermission(permission, "")
		return
	}
	entry.resolvePermission(permission, core.PermissionResult{Behavior: "allow", UpdatedInput: map[string]any{"answers": permission.answers}})
}

func (entry *telegramConnection) resolvePermission(permission *telegramPermission, result core.PermissionResult) {
	if entry.closed.Load() {
		return
	}
	if err := entry.manager.runtime.Permission(entry.ctx, entry.botID, permission.requestID, result); err != nil {
		// A web client may already have answered. Never turn a stale click into a
		// new prompt or apply it to a newer request.
		entry.failure(permission.turnID, permission.replyCtx, "This request is no longer waiting for a response", err)
	}
}

func (entry *telegramConnection) clearPermissions(turnID string) {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	for nonce, permission := range entry.pending {
		if permission.turnID == turnID {
			delete(entry.pending, nonce)
		}
	}
	for key := range entry.seen {
		if strings.HasPrefix(key, turnID+"\x00") {
			delete(entry.seen, key)
		}
	}
}

func (entry *telegramConnection) permissionResolved(event Event) {
	var resolved struct {
		RequestID string `json:"requestId"`
		Status    string `json:"status"`
	}
	if json.Unmarshal(event.Data, &resolved) != nil || resolved.Status != "resolved" || resolved.RequestID == "" {
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	for nonce, pending := range entry.pending {
		if pending.requestID == resolved.RequestID && pending.turnID == event.TurnID {
			delete(entry.pending, nonce)
		}
	}
}

func (m *TelegramManager) Close() error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.cancel()
	entries := m.entries
	m.entries = make(map[string]*telegramConnection)
	for _, entry := range entries {
		entry.closed.Store(true)
		entry.cancel()
	}
	m.mu.Unlock()
	var closeErrors []error
	for _, entry := range entries {
		if err := entry.platform.Stop(); err != nil {
			message := sanitizeTelegramError(err, entry.config.token)
			slog.Warn("connect-bots: close Telegram connection", "bot", entry.botID, "error", message)
			closeErrors = append(closeErrors, errors.New(message))
		}
		bot, err := m.store.GetBot(entry.botID)
		if err == nil {
			status := "disconnected"
			if bot.Status == "archived" || bot.Telegram == nil || !bot.Telegram.Enabled {
				status = "disabled"
			}
			m.bindingStatus(entry.botID, bot.Telegram, status, "")
		}
	}
	return errors.Join(closeErrors...)
}

// A Telegram token is intentionally resolved only from an explicitly selected
// environment variable. Existing cc-connect Telegram projects are never read.
type telegramConfig struct {
	tokenEnv string
	token    string
	users    []string
	allowed  map[string]struct{}
	identity [32]byte
}

func resolveTelegramConfig(binding *TelegramBinding, getenv func(string) string) (telegramConfig, error) {
	var cfg telegramConfig
	if binding == nil || !binding.Enabled {
		return cfg, errors.New("Telegram connection is disabled")
	}
	cfg.tokenEnv = strings.TrimSpace(binding.TokenEnv)
	if !telegramEnvName.MatchString(cfg.tokenEnv) {
		return cfg, errors.New("Telegram requires an environment variable name for its token")
	}
	if len(binding.AllowedUserIDs) == 0 {
		return cfg, errors.New("Telegram requires at least one explicit allowed user ID")
	}
	cfg.allowed = make(map[string]struct{}, len(binding.AllowedUserIDs))
	for _, raw := range binding.AllowedUserIDs {
		id := strings.TrimSpace(raw)
		userID, err := strconv.ParseInt(id, 10, 64)
		if err != nil || userID <= 0 || strconv.FormatInt(userID, 10) != id {
			return cfg, errors.New("Telegram allowed user IDs must be positive numeric IDs; wildcard access is disabled")
		}
		if _, found := cfg.allowed[id]; !found {
			cfg.allowed[id] = struct{}{}
			cfg.users = append(cfg.users, id)
		}
	}
	sort.Strings(cfg.users)
	cfg.token = strings.TrimSpace(getenv(cfg.tokenEnv))
	if cfg.token == "" {
		return cfg, fmt.Errorf("Telegram token environment variable %s is not set", cfg.tokenEnv)
	}
	if !telegramTokenFormat.MatchString(cfg.token) {
		return cfg, fmt.Errorf("Telegram token environment variable %s does not contain a bot token", cfg.tokenEnv)
	}
	// Includes the secret's digest rather than its plaintext in configuration
	// identity, so rotating an environment token restarts only that connection.
	cfg.identity = sha256.Sum256([]byte(cfg.tokenEnv + "\x00" + cfg.token + "\x00" + strings.Join(cfg.users, ",")))
	return cfg, nil
}

func sanitizeTelegramError(err error, token string) string {
	if err == nil {
		return ""
	}
	message := core.RedactToken(err.Error(), token)
	message = core.RedactToken(message, url.QueryEscape(token))
	message = telegramErrorURL.ReplaceAllString(message, "[endpoint]")
	if len(message) > 512 {
		message = message[:512] + "…"
	}
	return message
}
