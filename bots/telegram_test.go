package bots

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const testTelegramToken = "123456789:unit_test_token_not_a_real_secret"

func TestTelegramBindingRejectsPermitAllBeforeReadingToken(t *testing.T) {
	for _, users := range [][]string{nil, {}, {""}, {"*"}, {"123", "*"}, {"-123"}, {"user"}, {"1,2"}, {"+123"}, {"0123"}} {
		t.Run(strings.Join(users, ","), func(t *testing.T) {
			readToken := false
			_, err := resolveTelegramConfig(&TelegramBinding{Enabled: true, TokenEnv: "TEST_BOT_TOKEN", AllowedUserIDs: users}, func(string) string {
				readToken = true
				return testTelegramToken
			})
			if err == nil {
				t.Fatal("unsafe allow list was accepted")
			}
			if readToken {
				t.Fatal("invalid allow list must be rejected before resolving a credential")
			}
		})
	}
}

func TestTelegramBindingRequiresExplicitEnvironmentCredential(t *testing.T) {
	for _, env := range []string{"", " ", "123456789:secret", "TOKEN\nOTHER_TOKEN", "TOKEN=value"} {
		_, err := resolveTelegramConfig(&TelegramBinding{Enabled: true, TokenEnv: env, AllowedUserIDs: []string{"123"}}, func(string) string { return testTelegramToken })
		if err == nil {
			t.Fatalf("invalid environment name %q was accepted", env)
		}
	}
	_, err := resolveTelegramConfig(&TelegramBinding{Enabled: true, TokenEnv: "TEST_BOT_TOKEN", AllowedUserIDs: []string{"123"}}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "not set") {
		t.Fatalf("missing credential: %v", err)
	}
}

func TestTelegramCredentialRotationChangesConnectionIdentity(t *testing.T) {
	binding := &TelegramBinding{Enabled: true, TokenEnv: "TEST_BOT_TOKEN", AllowedUserIDs: []string{"456", "123", "123"}}
	first, err := resolveTelegramConfig(binding, func(string) string { return testTelegramToken })
	if err != nil {
		t.Fatal(err)
	}
	binding.AllowedUserIDs = []string{"123", "456"}
	unchanged, _ := resolveTelegramConfig(binding, func(string) string { return testTelegramToken })
	rotated, _ := resolveTelegramConfig(binding, func(string) string { return testTelegramToken + "_rotated" })
	if first.identity != unchanged.identity {
		t.Fatal("allow-list order or duplicates changed connection identity")
	}
	if first.identity == rotated.identity {
		t.Fatal("token rotation must reconnect the adapter")
	}
}

func TestTelegramConnectionErrorsDoNotExposeCredentialOrHTTPURL(t *testing.T) {
	err := errors.New("Get https://api.telegram.org/bot" + testTelegramToken + "/getMe failed: token " + testTelegramToken)
	message := sanitizeTelegramError(err, testTelegramToken)
	if strings.Contains(message, testTelegramToken) || strings.Contains(message, "api.telegram.org") || strings.Contains(message, "https://") {
		t.Fatalf("credential or token-bearing endpoint leaked: %q", message)
	}
}

type testTelegramPlatform struct {
	handler   core.MessageHandler
	lifecycle core.PlatformLifecycleHandler
	replies   chan string
	buttons   chan [][]core.ButtonOption
	images    chan testTelegramImage
	files     chan testTelegramFile
	fileError error
	stops     atomic.Int32
	startErr  error
}

func newTestTelegramPlatform() *testTelegramPlatform {
	return &testTelegramPlatform{replies: make(chan string, 32), buttons: make(chan [][]core.ButtonOption, 16), images: make(chan testTelegramImage, 16), files: make(chan testTelegramFile, 16)}
}

type testTelegramImage struct {
	replyCtx any
	image    core.ImageAttachment
}

type testTelegramFile struct {
	replyCtx any
	file     core.FileAttachment
}

func (p *testTelegramPlatform) SendImage(ctx context.Context, replyCtx any, image core.ImageAttachment) error {
	select {
	case p.images <- testTelegramImage{replyCtx: replyCtx, image: image}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *testTelegramPlatform) SendFile(ctx context.Context, replyCtx any, file core.FileAttachment) error {
	if p.fileError != nil {
		return p.fileError
	}
	select {
	case p.files <- testTelegramFile{replyCtx: replyCtx, file: file}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *testTelegramPlatform) Name() string { return "telegram" }
func (p *testTelegramPlatform) SetLifecycleHandler(handler core.PlatformLifecycleHandler) {
	p.lifecycle = handler
}
func (p *testTelegramPlatform) Start(handler core.MessageHandler) error {
	p.handler = handler
	if p.startErr != nil {
		return p.startErr
	}
	// This fake deliberately invokes readiness synchronously: Sync must not hold
	// the manager mutex while starting a platform that can call back immediately.
	p.lifecycle.OnPlatformReady(p)
	return nil
}
func (p *testTelegramPlatform) Reply(ctx context.Context, _ any, content string) error {
	select {
	case p.replies <- content:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *testTelegramPlatform) Send(ctx context.Context, replyCtx any, content string) error {
	return p.Reply(ctx, replyCtx, content)
}
func (p *testTelegramPlatform) SendWithButtons(ctx context.Context, _ any, _ string, buttons [][]core.ButtonOption) error {
	select {
	case p.buttons <- buttons:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *testTelegramPlatform) Stop() error               { p.stops.Add(1); return nil }
func (p *testTelegramPlatform) message(msg *core.Message) { p.handler(p, msg) }

type testTelegramRequest struct {
	botID, turnID string
	request       MessageRequest
}
type testTelegramDecision struct {
	requestID string
	result    core.PermissionResult
}
type testTelegramRuntime struct {
	store      *Store
	mu         sync.Mutex
	results    map[string]chan TurnResult
	requests   chan testTelegramRequest
	decisions  chan testTelegramDecision
	sends      atomic.Int32
	event      func(string) map[string]any
	onSend     func(botID, turnID string) error
	autoFinish bool
}

func newTestTelegramRuntime(store *Store) *testTelegramRuntime {
	return &testTelegramRuntime{store: store, results: make(map[string]chan TurnResult), requests: make(chan testTelegramRequest, 16), decisions: make(chan testTelegramDecision, 16)}
}
func (r *testTelegramRuntime) SendMessage(_ context.Context, botID string, request MessageRequest) (string, error) {
	turnID := fmt.Sprintf("test-turn-%d", r.sends.Add(1))
	result := make(chan TurnResult, 1)
	r.mu.Lock()
	r.results[turnID] = result
	r.mu.Unlock()
	if _, err := r.store.AppendEvent(botID, turnID, "message", map[string]any{"role": "user", "content": request.Text, "attachments": request.Attachments, "source": request.Source}); err != nil {
		return "", err
	}
	r.requests <- testTelegramRequest{botID: botID, turnID: turnID, request: request}
	if r.onSend != nil {
		if err := r.onSend(botID, turnID); err != nil {
			return "", err
		}
	}
	if r.event != nil {
		if _, err := r.store.AppendEvent(botID, turnID, "agent", r.event(turnID)); err != nil {
			return "", err
		}
	}
	if r.autoFinish {
		result <- TurnResult{Text: "Готово", Status: "completed"}
	}
	return turnID, nil
}
func (r *testTelegramRuntime) WaitTurn(ctx context.Context, botID, turnID string) (TurnResult, error) {
	r.mu.Lock()
	result := r.results[turnID]
	r.mu.Unlock()
	select {
	case completed := <-result:
		_, err := r.store.AppendEvent(botID, turnID, "message", map[string]any{"role": "assistant", "content": completed.Text})
		return completed, err
	case <-ctx.Done():
		return TurnResult{}, ctx.Err()
	}
}
func (r *testTelegramRuntime) Permission(_ context.Context, _, requestID string, result core.PermissionResult) error {
	r.decisions <- testTelegramDecision{requestID: requestID, result: result}
	r.complete(requestID, TurnResult{Text: "Готово", Status: "completed"})
	return nil
}
func (r *testTelegramRuntime) complete(turnID string, result TurnResult) {
	r.mu.Lock()
	channel := r.results[turnID]
	r.mu.Unlock()
	if channel != nil {
		channel <- result
	}
}

func telegramTestHarness(t *testing.T) (*Store, *TelegramManager, *testTelegramRuntime, Bot, *testTelegramPlatform) {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	bot, err := store.CreateBot(Bot{Name: "Telegram bot", Telegram: &TelegramBinding{Enabled: true, TokenEnv: "CONNECT_TEST_TELEGRAM", AllowedUserIDs: []string{"123"}}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newTestTelegramRuntime(store)
	manager := newTelegramManager(store, runtime)
	platform := newTestTelegramPlatform()
	manager.getenv = func(env string) string {
		if env == "CONNECT_TEST_TELEGRAM" {
			return testTelegramToken
		}
		return ""
	}
	manager.factory = func(opts map[string]any) (core.Platform, error) {
		if opts["allow_from"] != "123" {
			t.Errorf("adapter must receive the explicit owner allow list: %v", opts["allow_from"])
		}
		return platform, nil
	}
	t.Cleanup(func() { manager.Close() })
	return store, manager, runtime, bot, platform
}

func telegramTestMessage(id, text string) *core.Message {
	return &core.Message{UserID: "123", SessionKey: "telegram:123:123", MessageID: id, Content: text, ReplyCtx: "owner-reply"}
}

func telegramTestReceive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Telegram surface")
		var zero T
		return zero
	}
}

func telegramTestWaitIdle(t *testing.T, manager *TelegramManager, botID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		entry := manager.entries[botID]
		manager.mu.Unlock()
		if entry != nil && !entry.busy.Load() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("Telegram surface remained busy")
}

func TestTelegramSurfaceTracksReadinessAndStopsDisabledConnection(t *testing.T) {
	store, manager, _, bot, platform := telegramTestHarness(t)
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	connected, _ := store.GetBot(bot.ID)
	if connected.Telegram.Status != "connected" {
		t.Fatalf("actual readiness not exposed: %+v", connected.Telegram)
	}
	if err := manager.Sync(context.Background()); err != nil || platform.stops.Load() != 0 {
		t.Fatalf("unchanged profile reconnected: %v", err)
	}
	_, err := store.UpdateBot(bot.ID, func(current *Bot) error { current.Telegram.Enabled = false; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	platform.lifecycle.OnPlatformReady(platform) // Late callback from old poller.
	disabled, _ := store.GetBot(bot.ID)
	if platform.stops.Load() != 1 || disabled.Telegram.Status != "disabled" {
		t.Fatalf("disabled connection was not stopped or stale readiness won: %+v", disabled.Telegram)
	}
}

func TestTelegramSurfaceStartsNoPlatformForMissingOrDuplicateCredentials(t *testing.T) {
	store, manager, _, bot, _ := telegramTestHarness(t)
	var starts atomic.Int32
	manager.factory = func(map[string]any) (core.Platform, error) { starts.Add(1); return newTestTelegramPlatform(), nil }
	manager.getenv = func(string) string { return "" }
	if err := manager.Sync(context.Background()); err == nil || starts.Load() != 0 {
		t.Fatal("missing credential activated a platform")
	}
	manager.getenv = func(string) string { return testTelegramToken }
	_, err := store.CreateBot(Bot{Name: "Duplicate", Telegram: &TelegramBinding{Enabled: true, TokenEnv: "ANOTHER_ENV", AllowedUserIDs: []string{"123"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Sync(context.Background()); err == nil || starts.Load() != 0 {
		t.Fatal("duplicate Telegram credential activated a platform")
	}
	state, _ := store.GetBot(bot.ID)
	if state.Telegram.Status != "error" || state.Telegram.Error == "" {
		t.Fatal("connection configuration error must be visible")
	}
}

func TestTelegramSurfaceRedactsLifecycleErrorsInStateAndJournal(t *testing.T) {
	store, manager, _, bot, platform := telegramTestHarness(t)
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	platform.lifecycle.OnPlatformUnavailable(platform, errors.New("Get https://api.telegram.org/bot"+testTelegramToken+"/getMe failed"))
	state, _ := store.GetBot(bot.ID)
	events, _ := store.Events(bot.ID, 0)
	encoded, _ := json.Marshal(struct {
		Bot    Bot
		Events []Event
	}{state, events})
	if state.Telegram.Status != "error" || strings.Contains(string(encoded), testTelegramToken) || strings.Contains(string(encoded), "api.telegram.org") {
		t.Fatalf("lifecycle status or credential redaction failed: %s", encoded)
	}
}

func TestTelegramSurfaceUsesSharedJournalRejectsUnknownUserAndDuplicateMessage(t *testing.T) {
	store, manager, runtime, bot, platform := telegramTestHarness(t)
	runtime.autoFinish = true
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	unauthorized := telegramTestMessage("foreign", "must not become a prompt")
	unauthorized.UserID = "999"
	platform.message(unauthorized)
	platform.message(telegramTestMessage("first", "Проверь проект"))
	request := telegramTestReceive(t, runtime.requests)
	if request.botID != bot.ID || request.request.Text != "Проверь проект" || request.request.Source != "telegram" {
		t.Fatalf("message did not route to the canonical runtime: %+v", request)
	}
	if reply := telegramTestReceive(t, platform.replies); reply != "Готово" {
		t.Fatalf("final runtime answer was not returned: %s", reply)
	}
	telegramTestWaitIdle(t, manager, bot.ID)
	platform.message(telegramTestMessage("first", "Проверь проект"))
	if runtime.sends.Load() != 1 {
		t.Fatal("redelivered Telegram message reran an accepted turn")
	}
	events, err := store.Events(bot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var user, assistant bool
	for _, event := range events {
		if event.Type != "message" {
			continue
		}
		var data struct{ Role, Content, Source string }
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatal(err)
		}
		user = user || data.Role == "user" && data.Source == "telegram" && data.Content == "Проверь проект"
		assistant = assistant || data.Role == "assistant" && data.Content == "Готово"
	}
	if !user || !assistant {
		t.Fatal("web replay did not retain the Telegram user message and answer")
	}
}

func TestTelegramSurfaceRepliesBrieflyWhenItsCanonicalTurnIsBusy(t *testing.T) {
	_, manager, runtime, bot, platform := telegramTestHarness(t)
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	platform.message(telegramTestMessage("first", "Долгая задача"))
	request := telegramTestReceive(t, runtime.requests)
	platform.message(telegramTestMessage("second", "Ещё одна задача"))
	if reply := telegramTestReceive(t, platform.replies); !strings.Contains(reply, "предыдущую") {
		t.Fatalf("busy reply was not clear: %s", reply)
	}
	if runtime.sends.Load() != 1 {
		t.Fatal("busy Telegram message entered another runtime turn")
	}
	runtime.complete(request.turnID, TurnResult{Text: "Закончено", Status: "completed"})
	telegramTestReceive(t, platform.replies)
	telegramTestWaitIdle(t, manager, bot.ID)
}

func TestTelegramSurfacePermissionButtonsCannotAuthorizeAnotherTurn(t *testing.T) {
	_, manager, runtime, bot, platform := telegramTestHarness(t)
	runtime.event = func(turnID string) map[string]any {
		return map[string]any{"type": "permission_request", "requestId": turnID, "toolName": "commandExecution", "toolInput": "pwd", "toolInputRaw": map[string]any{"command": "pwd"}}
	}
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	platform.message(telegramTestMessage("one", "Запусти команду"))
	first := telegramTestReceive(t, runtime.requests)
	firstButtons := telegramTestReceive(t, platform.buttons)
	firstCallback := firstButtons[0][0].Data
	if len(firstCallback) > 64 {
		t.Fatal("callback exceeds Telegram's 64-byte limit")
	}
	unauthorized := telegramTestMessage("permission-one", firstCallback)
	unauthorized.UserID = "999"
	platform.message(unauthorized)
	foreignSession := telegramTestMessage("permission-one", firstCallback)
	foreignSession.SessionKey = "telegram:456:123"
	platform.message(foreignSession)
	platform.message(telegramTestMessage("permission-one", firstCallback))
	firstDecision := telegramTestReceive(t, runtime.decisions)
	if firstDecision.requestID != first.turnID || firstDecision.result.Behavior != "allow" || firstDecision.result.UpdatedInput["command"] != "pwd" {
		t.Fatalf("approval did not preserve request scope/input: %+v", firstDecision)
	}
	telegramTestReceive(t, platform.replies)
	telegramTestWaitIdle(t, manager, bot.ID)
	platform.message(telegramTestMessage("two", "Запусти следующую команду"))
	second := telegramTestReceive(t, runtime.requests)
	secondButtons := telegramTestReceive(t, platform.buttons)
	if secondButtons[0][0].Data == firstCallback {
		t.Fatal("permission nonce was reused between turns")
	}
	platform.message(telegramTestMessage("old-button", firstCallback))
	legacy := telegramTestMessage("legacy-button", "allow")
	legacy.IsPermissionResponse = true
	platform.message(legacy)
	platform.message(telegramTestMessage("permission-two", secondButtons[0][1].Data))
	secondDecision := telegramTestReceive(t, runtime.decisions)
	if secondDecision.requestID != second.turnID || secondDecision.result.Behavior != "deny" {
		t.Fatalf("stale button authorized the newer request: %+v", secondDecision)
	}
	telegramTestReceive(t, platform.replies)
	telegramTestWaitIdle(t, manager, bot.ID)
	if runtime.sends.Load() != 2 || len(runtime.decisions) != 0 {
		t.Fatal("permission callbacks became prompts or resolved a request more than once")
	}
}

func TestTelegramSurfaceCollectsUserQuestionsWithoutCreatingNewPrompts(t *testing.T) {
	_, manager, runtime, bot, platform := telegramTestHarness(t)
	runtime.event = func(turnID string) map[string]any {
		return map[string]any{"type": "permission_request", "requestId": turnID, "toolName": "AskUserQuestion", "questions": []core.UserQuestion{
			{Question: "Какой формат?", Options: []core.UserQuestionOption{{Label: "JSON"}, {Label: "CSV"}}},
			{Question: "Как назвать файл?"},
		}}
	}
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	platform.message(telegramTestMessage("request", "Подготовь отчёт"))
	request := telegramTestReceive(t, runtime.requests)
	firstButtons := telegramTestReceive(t, platform.buttons)
	platform.message(telegramTestMessage("answer-one", firstButtons[1][0].Data))
	telegramTestReceive(t, platform.buttons) // Next free-text question.
	platform.message(telegramTestMessage("answer-two", "report.csv"))
	decision := telegramTestReceive(t, runtime.decisions)
	answers, _ := decision.result.UpdatedInput["answers"].(map[string]any)
	if decision.requestID != request.turnID || answers["Какой формат?"] != "CSV" || answers["Как назвать файл?"] != "report.csv" {
		t.Fatalf("question answers were not sent to their original request: %+v", decision)
	}
	telegramTestReceive(t, platform.replies)
	telegramTestWaitIdle(t, manager, bot.ID)
	if runtime.sends.Load() != 1 {
		t.Fatal("answer or option callback became a new agent turn")
	}
}

type testTelegramWorkspace struct {
	workspace *Workspace
	formats   chan string
}

func (w *testTelegramWorkspace) StoreUpload(botID, name, mimeType string, data []byte) (Attachment, error) {
	return w.workspace.StoreUpload(botID, name, mimeType, data)
}
func (w *testTelegramWorkspace) ResolveAttachments(botID string, attachments []Attachment) ([]Attachment, error) {
	return w.workspace.ResolveAttachments(botID, attachments)
}
func (w *testTelegramWorkspace) Transcribe(_ context.Context, audio []byte, format string) (string, error) {
	w.formats <- format
	if string(audio) != "original voice bytes" {
		return "", errors.New("voice bytes changed before transcription")
	}
	return "Голосовая просьба", nil
}

func TestTelegramSurfacePreservesMediaAndTranscribesVoiceThroughConfiguredWorkspace(t *testing.T) {
	store, manager, runtime, bot, platform := telegramTestHarness(t)
	runtime.autoFinish = true
	workspace := &testTelegramWorkspace{workspace: NewWorkspace(store), formats: make(chan string, 1)}
	manager.workspace = workspace
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	msg := telegramTestMessage("media", "Подпись")
	msg.ExtraContent = "Цитата"
	msg.Images = []core.ImageAttachment{{MimeType: "image/png", Data: []byte("original image bytes")}}
	msg.Files = []core.FileAttachment{{FileName: "report.txt", MimeType: "text/plain", Data: []byte("original file bytes")}}
	msg.Audio = &core.AudioAttachment{Format: "ogg", MimeType: "audio/ogg", Data: []byte("original voice bytes")}
	platform.message(msg)
	request := telegramTestReceive(t, runtime.requests)
	if request.request.Source != "telegram" || len(request.request.Attachments) != 3 || !strings.Contains(request.request.Text, "Цитата") || !strings.Contains(request.request.Text, "Голосовая просьба") {
		t.Fatalf("media or voice not routed into shared conversation: %+v", request.request)
	}
	if format := telegramTestReceive(t, workspace.formats); format != "ogg" {
		t.Fatalf("voice format was lost: %s", format)
	}
	attachments, err := workspace.workspace.ResolveAttachments(bot.ID, request.request.Attachments)
	if err != nil {
		t.Fatal(err)
	}
	for i, expected := range []string{"original image bytes", "original file bytes", "original voice bytes"} {
		bytes, err := os.ReadFile(attachments[i].Path)
		if err != nil || string(bytes) != expected {
			t.Fatalf("original media %d changed or was not durable: %v", i, err)
		}
		if request.request.Attachments[i].Path != "" {
			t.Fatal("upload exposed a server filesystem path to the public message")
		}
	}
	telegramTestReceive(t, platform.replies)
	telegramTestWaitIdle(t, manager, bot.ID)
}

func TestTelegramSurfaceRotatesOnlyTheChangedConnection(t *testing.T) {
	store, manager, _, _, first := telegramTestHarness(t)
	_, err := store.CreateBot(Bot{Name: "Another bot", Telegram: &TelegramBinding{Enabled: true, TokenEnv: "SECOND_TOKEN", AllowedUserIDs: []string{"123"}}})
	if err != nil {
		t.Fatal(err)
	}
	credential := testTelegramToken
	manager.getenv = func(env string) string {
		if env == "SECOND_TOKEN" {
			return "987654321:another_unit_test_token"
		}
		return credential
	}
	var created []*testTelegramPlatform
	manager.factory = func(map[string]any) (core.Platform, error) {
		platform := first
		if len(created) > 0 {
			platform = newTestTelegramPlatform()
		}
		created = append(created, platform)
		return platform, nil
	}
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	credential += "_rotated"
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(created) != 3 || first.stops.Load() != 1 || created[1].stops.Load() != 0 || created[2].stops.Load() != 0 {
		t.Fatal("token rotation did not stop and replace the old connection")
	}
}

func TestTelegramSurfaceKeepsActualReadinessForEquivalentAllowListEdit(t *testing.T) {
	store, manager, _, bot, platform := telegramTestHarness(t)
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	patch := map[string]json.RawMessage{"telegram": json.RawMessage(`{"enabled":true,"tokenEnv":"CONNECT_TEST_TELEGRAM","allowedUserIds":["123","123"]}`)}
	if _, err := store.PatchBot(bot.ID, patch); err != nil {
		t.Fatal(err)
	}
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _ := store.GetBot(bot.ID)
	if state.Telegram.Status != "connected" || platform.stops.Load() != 0 {
		t.Fatalf("equivalent profile edit lost actual connection state: %+v", state.Telegram)
	}
}

func telegramTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var body bytes.Buffer
	if err := png.Encode(&body, image.NewNRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func telegramTestArtifact(t *testing.T, store *Store, botID, turnID, caption string, attachments []Attachment) Event {
	t.Helper()
	event, err := store.AppendEvent(botID, turnID, "message", map[string]any{"role": "assistant", "content": caption, "attachments": attachments, "source": "files", "backend": "codex", "artifact": true})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestTelegramSurfaceSendsPublishedImageAndDocumentToOriginalChatOnce(t *testing.T) {
	store, manager, runtime, bot, platform := telegramTestHarness(t)
	workspace := NewWorkspace(store)
	imageBytes := telegramTestPNG(t, 8, 8)
	imageAttachment, err := workspace.StoreUpload(bot.ID, "chart.png", "image/png", imageBytes)
	if err != nil {
		t.Fatal(err)
	}
	document, err := workspace.StoreUpload(bot.ID, "report.pdf", "application/pdf", []byte("original PDF bytes"))
	if err != nil {
		t.Fatal(err)
	}
	runtime.onSend = func(botID, turnID string) error {
		_, err := store.AppendEvent(botID, turnID, "message", map[string]any{"role": "assistant", "content": "Готово", "attachments": []Attachment{imageAttachment, document}, "source": "files", "artifact": true})
		return err
	}
	runtime.autoFinish = true // Makes completion race with live artifact events.
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	message := telegramTestMessage("publish", "Сделай график и PDF")
	message.ReplyCtx = "original-chat-and-message"
	platform.message(message)
	telegramTestReceive(t, runtime.requests)
	photo := telegramTestReceive(t, platform.images)
	file := telegramTestReceive(t, platform.files)
	if photo.replyCtx != message.ReplyCtx || file.replyCtx != message.ReplyCtx {
		t.Fatal("media delivery guessed another recipient")
	}
	if photo.image.FileName != "chart.png" || photo.image.MimeType != "image/png" || !bytes.Equal(photo.image.Data, imageBytes) {
		t.Fatal("published image metadata or original bytes changed")
	}
	if file.file.FileName != "report.pdf" || file.file.MimeType != "application/pdf" || string(file.file.Data) != "original PDF bytes" {
		t.Fatal("published document metadata or original bytes changed")
	}
	if caption := telegramTestReceive(t, platform.replies); caption != "Готово" {
		t.Fatalf("missing artifact caption: %s", caption)
	}
	telegramTestWaitIdle(t, manager, bot.ID)
	if len(platform.images) != 0 || len(platform.files) != 0 || len(platform.replies) != 0 {
		t.Fatal("live replay or final answer duplicated file delivery or caption")
	}
}

func TestTelegramSurfaceResolvesPublishedIDsInsteadOfEventPathsOrMetadata(t *testing.T) {
	store, manager, runtime, bot, platform := telegramTestHarness(t)
	attachment, err := NewWorkspace(store).StoreUpload(bot.ID, "safe.txt", "text/plain", []byte("the published copy"))
	if err != nil {
		t.Fatal(err)
	}
	attachment.Path = "/etc/passwd"
	attachment.Name = "forged.png"
	attachment.MimeType = "image/png"
	runtime.onSend = func(botID, turnID string) error {
		_, err := store.AppendEvent(botID, turnID, "message", map[string]any{"role": "assistant", "attachments": []Attachment{attachment}, "artifact": true})
		return err
	}
	runtime.autoFinish = true
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	platform.message(telegramTestMessage("publish", "Отправь файл"))
	telegramTestReceive(t, runtime.requests)
	file := telegramTestReceive(t, platform.files)
	if file.file.FileName != "safe.txt" || file.file.MimeType != "text/plain" || string(file.file.Data) != "the published copy" || len(platform.images) != 0 {
		t.Fatal("event-provided path or MIME controlled the outgoing file")
	}
	telegramTestReceive(t, platform.replies)
	telegramTestWaitIdle(t, manager, bot.ID)
}

func TestTelegramSurfaceSendsAnimatedAndUnusuallyShapedImagesAsOriginalDocuments(t *testing.T) {
	store, manager, runtime, bot, platform := telegramTestHarness(t)
	workspace := NewWorkspace(store)
	tallBytes := telegramTestPNG(t, 1, 64)
	tall, err := workspace.StoreUpload(bot.ID, "long.png", "image/png", tallBytes)
	if err != nil {
		t.Fatal(err)
	}
	animation, err := workspace.StoreUpload(bot.ID, "animation.gif", "image/gif", []byte("original animated GIF bytes"))
	if err != nil {
		t.Fatal(err)
	}
	video, err := workspace.StoreUpload(bot.ID, "video.mp4", "video/mp4", []byte("original video bytes"))
	if err != nil {
		t.Fatal(err)
	}
	runtime.onSend = func(botID, turnID string) error {
		_, err := store.AppendEvent(botID, turnID, "message", map[string]any{"role": "assistant", "attachments": []Attachment{tall, animation, video}, "artifact": true})
		return err
	}
	runtime.autoFinish = true
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	platform.message(telegramTestMessage("publish", "Отправь картинки и видео"))
	telegramTestReceive(t, runtime.requests)
	for i, expected := range [][]byte{tallBytes, []byte("original animated GIF bytes"), []byte("original video bytes")} {
		file := telegramTestReceive(t, platform.files)
		if !bytes.Equal(file.file.Data, expected) {
			t.Fatalf("document %d was resized, flattened, or recompressed", i)
		}
	}
	telegramTestReceive(t, platform.replies)
	telegramTestWaitIdle(t, manager, bot.ID)
	if len(platform.images) != 0 {
		t.Fatal("unsupported photo was sent as a resized or flattened image")
	}
}

func TestTelegramSurfaceArtifactReplayIsIdempotentAndRejectsOtherBotsFiles(t *testing.T) {
	store, manager, _, bot, platform := telegramTestHarness(t)
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	workspace := NewWorkspace(store)
	attachment, err := workspace.StoreUpload(bot.ID, "own.txt", "text/plain", []byte("own output"))
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	entry := manager.entries[bot.ID]
	manager.mu.Unlock()
	delivery := &telegramArtifactDelivery{events: map[uint64]bool{}, files: map[string]bool{}, captions: map[string]bool{}}
	event := telegramTestArtifact(t, store, bot.ID, "artifact-turn", "Готовый файл", []Attachment{attachment})
	entry.deliverArtifact(event, "bound-chat", delivery)
	entry.deliverArtifact(event, "bound-chat", delivery)
	entry.flushArtifacts("artifact-turn", 0, "bound-chat", delivery)
	if len(platform.files) != 1 || len(platform.replies) != 1 {
		t.Fatal("artifact replay redelivered a file or its caption")
	}
	telegramTestReceive(t, platform.files)
	telegramTestReceive(t, platform.replies)
	otherBot, err := store.CreateBot(Bot{Name: "Another workspace"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := workspace.StoreUpload(otherBot.ID, "foreign.txt", "text/plain", []byte("another bot's file"))
	if err != nil {
		t.Fatal(err)
	}
	foreignEvent := telegramTestArtifact(t, store, bot.ID, "artifact-turn", "Чужой файл", []Attachment{foreign})
	entry.deliverArtifact(foreignEvent, "bound-chat", delivery)
	if len(platform.files) != 0 {
		t.Fatal("a foreign opaque ID escaped its bot upload directory")
	}
	if errorReply := telegramTestReceive(t, platform.replies); !strings.Contains(errorReply, "Не удалось прочитать") {
		t.Fatalf("missing delivery error: %s", errorReply)
	}
}

func TestTelegramSurfaceDoesNotDeliverWebArtifactsWithoutAnIncomingTelegramTarget(t *testing.T) {
	store, manager, _, bot, platform := telegramTestHarness(t)
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	attachment, err := NewWorkspace(store).StoreUpload(bot.ID, "web.txt", "text/plain", []byte("web-only output"))
	if err != nil {
		t.Fatal(err)
	}
	telegramTestArtifact(t, store, bot.ID, "web-turn", "Результат в вебе", []Attachment{attachment})
	select {
	case <-platform.files:
		t.Fatal("web turn guessed a Telegram recipient")
	case <-platform.images:
		t.Fatal("web turn guessed a Telegram recipient")
	case <-platform.replies:
		t.Fatal("web turn created an unsolicited Telegram notification")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestTelegramSurfaceReportsMediaSendFailureWithoutRetryOrCredentialLeak(t *testing.T) {
	store, manager, runtime, bot, platform := telegramTestHarness(t)
	platform.fileError = errors.New("Post https://api.telegram.org/bot" + testTelegramToken + "/sendDocument: upload failed")
	attachment, err := NewWorkspace(store).StoreUpload(bot.ID, "report.txt", "text/plain", []byte("output"))
	if err != nil {
		t.Fatal(err)
	}
	runtime.onSend = func(botID, turnID string) error {
		_, err := store.AppendEvent(botID, turnID, "message", map[string]any{"role": "assistant", "content": "Готовые файлы", "attachments": []Attachment{attachment}, "artifact": true})
		return err
	}
	runtime.autoFinish = true
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	platform.message(telegramTestMessage("publish", "Отправь отчёт"))
	telegramTestReceive(t, runtime.requests)
	if errorReply := telegramTestReceive(t, platform.replies); !strings.Contains(errorReply, "Не удалось отправить файл") || strings.Contains(errorReply, testTelegramToken) {
		t.Fatalf("unsafe media delivery error: %s", errorReply)
	}
	telegramTestReceive(t, platform.replies) // Normal final answer remains visible.
	telegramTestWaitIdle(t, manager, bot.ID)
	if len(platform.replies) != 0 {
		t.Fatal("completion flush retried and reported the same failed upload twice")
	}
	events, _ := store.Events(bot.ID, 0)
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), testTelegramToken) || strings.Contains(string(encoded), "api.telegram.org") {
		t.Fatal("outbound media failure leaked its credential into durable events")
	}
}
