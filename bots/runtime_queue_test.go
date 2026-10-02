package bots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func submitRuntimeMessage(t *testing.T, runtime *Runtime, botID, text string) MessageReceipt {
	t.Helper()
	receipt, err := runtime.SubmitMessage(context.Background(), botID, MessageRequest{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func runtimeMessageQueue(t *testing.T, runtime *Runtime, botID string) MessageQueue {
	t.Helper()
	queue, err := runtime.Queue(botID)
	if err != nil {
		t.Fatal(err)
	}
	return queue
}

func TestRuntimeBusyOwnerMessagesQueueAndDispatchFIFOAfterFinalAnswer(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	first := submitRuntimeMessage(t, runtime, bot.ID, "first")
	sent := nextRuntimeSend(t, factory)
	second := submitRuntimeMessage(t, runtime, bot.ID, "second")
	third := submitRuntimeMessage(t, runtime, bot.ID, "third")
	if first.Status != "running" || second.Status != "queued" || second.QueueID != second.TurnID || third.Status != "queued" {
		t.Fatalf("receipts: %+v %+v %+v", first, second, third)
	}
	queue := runtimeMessageQueue(t, runtime, bot.ID)
	if queue.Paused || len(queue.Messages) != 2 || queue.Messages[0].ID != second.TurnID || queue.Messages[1].ID != third.TurnID {
		t.Fatalf("FIFO queue: %+v", queue)
	}
	select {
	case extra := <-factory.sends:
		t.Fatalf("queued prompt reached active provider: %q", extra.prompt)
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := runtime.WaitTurn(ctx, bot.ID, second.TurnID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued wait returned before dispatch: %v", err)
	}
	sent.session.complete("first answer")
	waitRuntimeTurn(t, runtime, bot.ID, first.TurnID)
	secondSend := nextRuntimeSend(t, factory)
	if secondSend.prompt != "second" || secondSend.session != sent.session {
		t.Fatalf("second dispatch: %+v", secondSend)
	}
	secondSend.session.complete("second answer")
	waitRuntimeTurn(t, runtime, bot.ID, second.TurnID)
	thirdSend := nextRuntimeSend(t, factory)
	if thirdSend.prompt != "third" {
		t.Fatalf("third dispatch: %q", thirdSend.prompt)
	}
	thirdSend.session.complete("third answer")
	if result := waitRuntimeTurn(t, runtime, bot.ID, third.TurnID); result.Text != "third answer" {
		t.Fatalf("queued turn result: %+v", result)
	}
	if len(runtimeMessageQueue(t, runtime, bot.ID).Messages) != 0 {
		t.Fatal("dispatched messages remained queued")
	}
	events, _ := store.Events(bot.ID, 0)
	var firstFinal, secondUser uint64
	for _, event := range events {
		if event.Type == "queue" && event.TurnID != "" {
			t.Fatal("queue event masqueraded as a dialogue turn")
		}
		if event.Type == "message" && event.TurnID == first.TurnID && strings.Contains(string(event.Data), "first answer") {
			firstFinal = event.Seq
		}
		if event.Type == "message" && event.TurnID == second.TurnID && strings.Contains(string(event.Data), `"role":"user"`) {
			secondUser = event.Seq
		}
	}
	if firstFinal == 0 || secondUser <= firstFinal {
		t.Fatalf("queued turn appeared before final answer: final=%d next=%d", firstFinal, secondUser)
	}
}

func TestRuntimeCancelQueuedMessageNeverSendsAndKeepsOtherBotsIsolated(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	other, err := store.CreateBot(Bot{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	first := submitRuntimeMessage(t, runtime, bot.ID, "active")
	sent := nextRuntimeSend(t, factory)
	removed := submitRuntimeMessage(t, runtime, bot.ID, "remove this")
	kept := submitRuntimeMessage(t, runtime, bot.ID, "keep this")
	if err := runtime.CancelQueuedMessage(context.Background(), other.ID, removed.QueueID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-bot cancellation: %v", err)
	}
	if err := runtime.CancelQueuedMessage(context.Background(), bot.ID, removed.QueueID); err != nil {
		t.Fatal(err)
	}
	if result := waitRuntimeTurn(t, runtime, bot.ID, removed.TurnID); result.Status != "cancelled" {
		t.Fatalf("cancelled queue result: %+v", result)
	}
	sent.session.complete("done")
	waitRuntimeTurn(t, runtime, bot.ID, first.TurnID)
	next := nextRuntimeSend(t, factory)
	if next.prompt != "keep this" {
		t.Fatalf("cancelled message sent: %q", next.prompt)
	}
	next.session.complete("kept")
	waitRuntimeTurn(t, runtime, bot.ID, kept.TurnID)
}

func TestRuntimeStopAndFailedTurnPauseQueueUntilOwnerResume(t *testing.T) {
	for _, action := range []string{"stop", "error"} {
		t.Run(action, func(t *testing.T) {
			_, runtime, factory, bot := setupRuntime(t)
			first := submitRuntimeMessage(t, runtime, bot.ID, "active")
			sent := nextRuntimeSend(t, factory)
			queued := submitRuntimeMessage(t, runtime, bot.ID, "later")
			if action == "stop" {
				if err := runtime.Stop(context.Background(), bot.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				sent.session.emit(core.Event{Type: core.EventError, Error: errors.New("provider failed")})
				sent.session.complete("")
			}
			waitRuntimeTurn(t, runtime, bot.ID, first.TurnID)
			waitRuntimeCondition(t, func() bool { return runtimeMessageQueue(t, runtime, bot.ID).Paused })
			queue := runtimeMessageQueue(t, runtime, bot.ID)
			if len(queue.Messages) != 1 || queue.Messages[0].ID != queued.TurnID {
				t.Fatalf("paused queue: %+v", queue)
			}
			select {
			case extra := <-factory.sends:
				t.Fatalf("Stop/failure dispatched queued message: %q", extra.prompt)
			default:
			}
			if err := runtime.ResumeQueue(context.Background(), bot.ID); err != nil {
				t.Fatal(err)
			}
			next := nextRuntimeSend(t, factory)
			if next.prompt != "later" {
				t.Fatalf("resumed prompt: %q", next.prompt)
			}
			next.session.complete("later answer")
			waitRuntimeTurn(t, runtime, bot.ID, queued.TurnID)
		})
	}
}

func TestRuntimeQueuedMessagesSurviveRestartWithoutReplayingStartedTurn(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	root := store.Root()
	active := submitRuntimeMessage(t, runtime, bot.ID, "do not replay")
	nextRuntimeSend(t, factory)
	queued := submitRuntimeMessage(t, runtime, bot.ID, "recover me")
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	nextFactory := &runtimeFakeFactory{sends: make(chan runtimeFakeSend, 8)}
	recovered := NewRuntime(reopened, RuntimeConfig{AgentFactory: nextFactory.create, AgentOptions: runtimeFakeAgentOptions()})
	defer recovered.Close()
	queue := runtimeMessageQueue(t, recovered, bot.ID)
	if !queue.Paused || len(queue.Messages) != 1 || queue.Messages[0].ID != queued.TurnID {
		t.Fatalf("recovered queue: %+v", queue)
	}
	if result := waitRuntimeTurn(t, recovered, bot.ID, active.TurnID); result.Status != "interrupted" {
		t.Fatalf("recovered active turn: %+v", result)
	}
	select {
	case unexpected := <-nextFactory.sends:
		t.Fatalf("restart submitted work automatically: %q", unexpected.prompt)
	default:
	}
	if err := recovered.ResumeQueue(context.Background(), bot.ID); err != nil {
		t.Fatal(err)
	}
	sent := nextRuntimeSend(t, nextFactory)
	if sent.prompt != "recover me" {
		t.Fatalf("replayed wrong prompt: %q", sent.prompt)
	}
	sent.session.complete("recovered")
	waitRuntimeTurn(t, recovered, bot.ID, queued.TurnID)
}

func TestRuntimeQueuedSteerAcknowledgesExactActiveTurnBeforeRemovingItem(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	active := submitRuntimeMessage(t, runtime, bot.ID, "active")
	sent := nextRuntimeSend(t, factory)
	sent.session.native("turn/started", map[string]any{"threadId": sent.session.id, "turn": map[string]any{"id": "owned-active"}})
	queued := submitRuntimeMessage(t, runtime, bot.ID, "change direction")
	entered, release := make(chan struct{}), make(chan struct{})
	sent.session.mu.Lock()
	sent.session.steerEntered, sent.session.steerRelease = entered, release
	sent.session.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		receipt, err := runtime.SteerQueuedMessage(context.Background(), bot.ID, queued.QueueID)
		if err == nil && (receipt.TurnID != active.TurnID || receipt.QueueID != queued.QueueID || receipt.Status != "steered") {
			err = fmt.Errorf("wrong steer receipt: %+v", receipt)
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("native steer was not called")
	}
	beforeAck := runtimeMessageQueue(t, runtime, bot.ID)
	if len(beforeAck.Messages) != 1 {
		t.Fatal("queue item removed before native acknowledgement")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if queue := runtimeMessageQueue(t, runtime, bot.ID); len(queue.Messages) != 0 || !runtime.Busy(bot.ID) {
		t.Fatalf("steer started a different turn: %+v", queue)
	}
	sent.session.mu.Lock()
	params := sent.session.rpcParams[len(sent.session.rpcParams)-1]
	sent.session.mu.Unlock()
	if params["threadId"] != sent.session.id || params["expectedTurnId"] != "owned-active" {
		t.Fatalf("unscoped steer: %+v", params)
	}
	events, _ := store.Events(bot.ID, 0)
	steerMessages := 0
	for _, event := range events {
		if event.Type == "message" && strings.Contains(string(event.Data), `"mode":"steer"`) {
			steerMessages++
			if event.TurnID != active.TurnID {
				t.Fatal("steer attached to a different product turn")
			}
		}
	}
	if steerMessages != 1 {
		t.Fatalf("steer transcript messages=%d", steerMessages)
	}
	sent.session.complete("steered answer")
	waitRuntimeTurn(t, runtime, bot.ID, active.TurnID)
}

func TestRuntimeSteerFailurePreservesQueuedItemAndStartingTurnIsNeverGuessed(t *testing.T) {
	_, runtime, factory, bot := setupRuntime(t)
	active := submitRuntimeMessage(t, runtime, bot.ID, "active")
	sent := nextRuntimeSend(t, factory)
	queued := submitRuntimeMessage(t, runtime, bot.ID, "later")
	if _, err := runtime.SteerQueuedMessage(context.Background(), bot.ID, queued.QueueID); !errors.Is(err, ErrConflict) {
		t.Fatalf("starting turn steer: %v", err)
	}
	sent.session.mu.Lock()
	guessed := false
	for _, method := range sent.session.rpcCalls {
		guessed = guessed || method == "turn/steer"
	}
	sent.session.steerErr = &core.RPCRejectionError{Message: "expected turn no longer active"}
	sent.session.mu.Unlock()
	if guessed {
		t.Fatal("starting turn identity was guessed")
	}
	sent.session.native("turn/started", map[string]any{"threadId": sent.session.id, "turn": map[string]any{"id": "native-active"}})
	if _, err := runtime.SteerQueuedMessage(context.Background(), bot.ID, queued.QueueID); !errors.Is(err, ErrConflict) {
		t.Fatalf("rejected native steer: %v", err)
	}
	queue := runtimeMessageQueue(t, runtime, bot.ID)
	if len(queue.Messages) != 1 || queue.Messages[0].ID != queued.QueueID {
		t.Fatalf("failed steer lost queued message: %+v", queue)
	}
	sent.session.complete("finished")
	waitRuntimeTurn(t, runtime, bot.ID, active.TurnID)
	next := nextRuntimeSend(t, factory)
	if next.prompt != "later" {
		t.Fatalf("failed steer was not subsequently queued: %q", next.prompt)
	}
	next.session.complete("later")
	waitRuntimeTurn(t, runtime, bot.ID, queued.TurnID)
}

type failSteerMessageStore struct {
	RuntimeStore
	mu           sync.Mutex
	fired        bool
	failTerminal bool
}

func (s *failSteerMessageStore) AppendEvent(botID, turnID, kind string, data any) (Event, error) {
	encoded, _ := json.Marshal(data)
	s.mu.Lock()
	fail := kind == "message" && strings.Contains(string(encoded), `"mode":"steer"`) && !s.fired
	if fail {
		s.fired = true
	}
	fail = fail || (s.failTerminal && kind == "queue" && strings.Contains(string(encoded), `"status":"steered"`))
	s.mu.Unlock()
	if fail {
		return Event{}, errors.New("journal write failed after native ACK")
	}
	return s.RuntimeStore.AppendEvent(botID, turnID, kind, data)
}

func TestRuntimeSteerPostAckJournalFailureCannotDuplicateQueuedMessage(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	runtime.store = &failSteerMessageStore{RuntimeStore: store, failTerminal: true}
	active := submitRuntimeMessage(t, runtime, bot.ID, "active")
	sent := nextRuntimeSend(t, factory)
	sent.session.native("turn/started", map[string]any{"threadId": sent.session.id, "turn": map[string]any{"id": "native-active"}})
	queued := submitRuntimeMessage(t, runtime, bot.ID, "accept once")
	receipt, err := runtime.SteerQueuedMessage(context.Background(), bot.ID, queued.QueueID)
	if err != nil || receipt.Status != "steered" {
		t.Fatalf("ACK turned into retryable failure: %+v %v", receipt, err)
	}
	if len(runtimeMessageQueue(t, runtime, bot.ID).Messages) != 0 {
		t.Fatal("provider-accepted steer remained actionable")
	}
	if _, err := runtime.SteerQueuedMessage(context.Background(), bot.ID, queued.QueueID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("duplicate steer accepted: %v", err)
	}
	sent.session.complete("once")
	waitRuntimeTurn(t, runtime, bot.ID, active.TurnID)
	select {
	case replay := <-factory.sends:
		t.Fatalf("accepted steer replayed as a prompt: %q", replay.prompt)
	default:
	}
	root := store.Root()
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	nextFactory := &runtimeFakeFactory{sends: make(chan runtimeFakeSend, 8)}
	recovered := NewRuntime(reopened, RuntimeConfig{AgentFactory: nextFactory.create, AgentOptions: runtimeFakeAgentOptions()})
	defer recovered.Close()
	if len(runtimeMessageQueue(t, recovered, bot.ID).Messages) != 0 {
		t.Fatal("restart replayed an accepted steer when its terminal journal record failed")
	}
}

func TestRuntimeQueueAttachmentsAreResolvedAtDispatchAndNativePiSteerKeepsVision(t *testing.T) {
	for _, backend := range []string{"codex", "pi"} {
		t.Run(backend, func(t *testing.T) {
			store, runtime, factory, bot := setupRuntime(t)
			if backend == "pi" {
				var err error
				bot, err = store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Backend, bot.Model = "pi", "deepseek/deepseek-flash"; return nil })
				if err != nil {
					t.Fatal(err)
				}
			}
			workspace := NewWorkspace(store)
			runtime.cfg.ResolveAttachments = workspace.ResolveAttachments
			file, err := workspace.StoreUpload(bot.ID, "report.txt", "text/plain", []byte("report content"))
			if err != nil {
				t.Fatal(err)
			}
			image, err := workspace.StoreUpload(bot.ID, "probe.png", "image/png", []byte("image bytes"))
			if err != nil {
				t.Fatal(err)
			}
			active := submitRuntimeMessage(t, runtime, bot.ID, "active")
			sent := nextRuntimeSend(t, factory)
			receipt, err := runtime.SubmitMessage(context.Background(), bot.ID, MessageRequest{Text: "inspect", Attachments: []Attachment{file, image}})
			if err != nil {
				t.Fatal(err)
			}
			queue := runtimeMessageQueue(t, runtime, bot.ID)
			for _, attachment := range queue.Messages[0].Attachments {
				if attachment.Path != "" {
					t.Fatal("queue leaked filesystem paths")
				}
			}
			if backend == "pi" {
				if _, err := runtime.SteerQueuedMessage(context.Background(), bot.ID, receipt.QueueID); err != nil {
					t.Fatal(err)
				}
				sent.session.mu.Lock()
				method := sent.session.rpcCalls[len(sent.session.rpcCalls)-1]
				params := sent.session.rpcParams[len(sent.session.rpcParams)-1]
				sent.session.mu.Unlock()
				encoded, _ := json.Marshal(params)
				if method != "steer" || !strings.Contains(string(encoded), `"mimeType":"image/png"`) || !strings.Contains(string(encoded), `aW1hZ2UgYnl0ZXM=`) || !strings.Contains(params["message"].(string), "report.txt") {
					t.Fatalf("Pi steer lost attachments: %s %s", method, encoded)
				}
				sent.session.complete("vision answer")
				waitRuntimeTurn(t, runtime, bot.ID, active.TurnID)
				return
			}
			sent.session.complete("first")
			waitRuntimeTurn(t, runtime, bot.ID, active.TurnID)
			next := nextRuntimeSend(t, factory)
			if len(next.files) != 1 || string(next.files[0].Data) != "report content" || len(next.images) != 1 || string(next.images[0].Data) != "image bytes" {
				t.Fatalf("queued attachment content lost: %+v", next)
			}
			next.session.complete("file answer")
			waitRuntimeTurn(t, runtime, bot.ID, receipt.TurnID)
		})
	}
}

func TestTenantMessageQueueAPIRejectsForeignWorkspaceAndReturnsQueueReceipt(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	server, err := NewTenantServer(store, runtime, ServerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	active := submitRuntimeMessage(t, runtime, bot.ID, "active")
	sent := nextRuntimeSend(t, factory)
	request := httptest.NewRequest(http.MethodPost, "/api/studio/bots/"+bot.ID+"/messages", strings.NewReader(`{"text":"HTTP queued"}`))
	response := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(response, request)
	var receipt MessageReceipt
	_ = json.Unmarshal(response.Body.Bytes(), &receipt)
	if response.Code != http.StatusAccepted || receipt.Status != "queued" || receipt.QueueID == "" {
		t.Fatalf("HTTP queue receipt: %d %s", response.Code, response.Body.String())
	}
	foreignStore, foreignRuntime, _, _ := setupRuntime(t)
	foreignServer, err := NewTenantServer(foreignStore, foreignRuntime, ServerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer foreignServer.Close()
	for _, route := range []struct{ method, suffix string }{
		{http.MethodGet, "/queue"},
		{http.MethodDelete, "/queue/" + receipt.QueueID},
		{http.MethodPost, "/queue/" + receipt.QueueID + "/steer"},
		{http.MethodPost, "/queue/resume"},
	} {
		out := httptest.NewRecorder()
		foreignServer.APIHandler().ServeHTTP(out, httptest.NewRequest(route.method, "/api/studio/bots/"+bot.ID+route.suffix, nil))
		if out.Code != http.StatusNotFound {
			t.Fatalf("foreign queue route %s: %d %s", route.suffix, out.Code, out.Body.String())
		}
	}
	out := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(out, httptest.NewRequest(http.MethodDelete, "/api/studio/bots/"+bot.ID+"/queue/"+receipt.QueueID, nil))
	if out.Code != http.StatusOK {
		t.Fatalf("delete queue: %d %s", out.Code, out.Body.String())
	}
	sent.session.complete("active done")
	waitRuntimeTurn(t, runtime, bot.ID, active.TurnID)
}

func TestRuntimeCancelledAndSteeredQueueWaitSurvivesTurnPruning(t *testing.T) {
	for _, status := range []string{"cancelled", "steered"} {
		t.Run(status, func(t *testing.T) {
			_, runtime, factory, bot := setupRuntime(t)
			active := submitRuntimeMessage(t, runtime, bot.ID, "active")
			sent := nextRuntimeSend(t, factory)
			queued := submitRuntimeMessage(t, runtime, bot.ID, "follow up")
			if status == "cancelled" {
				if err := runtime.CancelQueuedMessage(context.Background(), bot.ID, queued.QueueID); err != nil {
					t.Fatal(err)
				}
			} else {
				sent.session.native("turn/started", map[string]any{"threadId": sent.session.id, "turn": map[string]any{"id": "native-active"}})
				if _, err := runtime.SteerQueuedMessage(context.Background(), bot.ID, queued.QueueID); err != nil {
					t.Fatal(err)
				}
			}
			runtime.mu.Lock()
			delete(runtime.turns, queued.TurnID)
			runtime.mu.Unlock()
			result, err := runtime.WaitTurn(context.Background(), bot.ID, queued.TurnID)
			if err != nil || result.Status != status {
				t.Fatalf("persisted queue completion: %+v %v", result, err)
			}
			sent.session.complete("done")
			waitRuntimeTurn(t, runtime, bot.ID, active.TurnID)
		})
	}
}

func TestRuntimeSteerLostAckNeverAutomaticallyReplaysAcrossCompletionOrRestart(t *testing.T) {
	for _, cause := range []error{context.DeadlineExceeded, context.Canceled, io.EOF} {
		t.Run(cause.Error(), func(t *testing.T) {
			store, runtime, factory, bot := setupRuntime(t)
			active := submitRuntimeMessage(t, runtime, bot.ID, "active")
			sent := nextRuntimeSend(t, factory)
			sent.session.native("turn/started", map[string]any{"threadId": sent.session.id, "turn": map[string]any{"id": "native-active"}})
			queued := submitRuntimeMessage(t, runtime, bot.ID, "accept once")
			sent.session.mu.Lock()
			sent.session.steerErr = cause
			sent.session.mu.Unlock()
			if _, err := runtime.SteerQueuedMessage(context.Background(), bot.ID, queued.QueueID); !errors.Is(err, ErrConflict) || !errors.Is(err, cause) {
				t.Fatalf("lost acknowledgement: %v", err)
			}
			if queue := runtimeMessageQueue(t, runtime, bot.ID); len(queue.Messages) != 0 || !queue.Paused {
				t.Fatalf("uncertain steer remained actionable: %+v", queue)
			}
			sent.session.complete("possibly accepted steering")
			waitRuntimeTurn(t, runtime, bot.ID, active.TurnID)
			select {
			case replay := <-factory.sends:
				t.Fatalf("lost-ACK steer replayed: %q", replay.prompt)
			case <-time.After(20 * time.Millisecond):
			}
			if err := runtime.Close(); err != nil {
				t.Fatal(err)
			}
			recovered := NewRuntime(store, RuntimeConfig{AgentFactory: factory.create, AgentOptions: runtimeFakeAgentOptions()})
			defer recovered.Close()
			if len(runtimeMessageQueue(t, recovered, bot.ID).Messages) != 0 {
				t.Fatal("restart resurrected a possibly accepted steer")
			}
			if result := waitRuntimeTurn(t, recovered, bot.ID, queued.TurnID); result.Status != "uncertain" {
				t.Fatalf("uncertain delivery lost from journal: %+v", result)
			}
		})
	}
}

type blockingQueueRecordStore struct {
	RuntimeStore
	once    sync.Once
	status  string
	entered chan struct{}
	release chan struct{}
}

func (s *blockingQueueRecordStore) AppendEvent(botID, turnID, kind string, data any) (Event, error) {
	if kind == "queue" {
		raw, _ := json.Marshal(data)
		var record struct{ Status string }
		_ = json.Unmarshal(raw, &record)
		if record.Status == s.status {
			s.once.Do(func() { close(s.entered); <-s.release })
		}
	}
	return s.RuntimeStore.AppendEvent(botID, turnID, kind, data)
}

func TestRuntimeCloseBetweenQueueStartedMarkerAndAdmissionPreservesUnsentMessage(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	block := &blockingQueueRecordStore{RuntimeStore: store, status: "started", entered: make(chan struct{}), release: make(chan struct{})}
	runtime.store = block
	active := submitRuntimeMessage(t, runtime, bot.ID, "active")
	sent := nextRuntimeSend(t, factory)
	queued := submitRuntimeMessage(t, runtime, bot.ID, "survive shutdown")
	sent.session.complete("done")
	waitRuntimeTurn(t, runtime, bot.ID, active.TurnID)
	select {
	case <-block.entered:
	case <-time.After(time.Second):
		t.Fatal("queued dispatch did not reach its started marker")
	}
	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	waitRuntimeCondition(t, func() bool { runtime.mu.Lock(); defer runtime.mu.Unlock(); return runtime.closed })
	close(block.release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	recovered := NewRuntime(store, RuntimeConfig{AgentFactory: factory.create, AgentOptions: runtimeFakeAgentOptions()})
	defer recovered.Close()
	queue := runtimeMessageQueue(t, recovered, bot.ID)
	if !queue.Paused || len(queue.Messages) != 1 || queue.Messages[0].ID != queued.QueueID {
		t.Fatalf("graceful shutdown lost an unsent message: %+v", queue)
	}
	select {
	case extra := <-factory.sends:
		t.Fatalf("shutdown admitted a new provider prompt: %q", extra.prompt)
	default:
	}
}

func TestRuntimeCompletionWhileQueueAcceptsMessageCannotStrandIt(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	bot, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Backend, bot.Model = "pi", "deepseek/deepseek-flash"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	block := &blockingQueueRecordStore{RuntimeStore: store, status: "queued", entered: make(chan struct{}), release: make(chan struct{})}
	runtime.store = block
	active := submitRuntimeMessage(t, runtime, bot.ID, "active")
	sent := nextRuntimeSend(t, factory)
	receipts := make(chan MessageReceipt, 1)
	errs := make(chan error, 1)
	go func() {
		receipt, err := runtime.SubmitMessage(context.Background(), bot.ID, MessageRequest{Text: "finish-race"})
		receipts <- receipt
		errs <- err
	}()
	<-block.entered
	sent.session.complete("done while enqueueing")
	waitRuntimeTurn(t, runtime, bot.ID, active.TurnID)
	close(block.release)
	receipt := <-receipts
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	next := nextRuntimeSend(t, factory)
	if next.prompt != "finish-race" {
		t.Fatalf("accepted queue message stranded: %q", next.prompt)
	}
	next.session.complete("not stranded")
	waitRuntimeTurn(t, runtime, bot.ID, receipt.TurnID)
}

func TestRuntimePiCompletionDuringSteerClearsOwnedNativeQueueAndQuarantinesMessage(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	bot, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Backend, bot.Model = "pi", "deepseek/deepseek-flash"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	active := submitRuntimeMessage(t, runtime, bot.ID, "active")
	sent := nextRuntimeSend(t, factory)
	queued := submitRuntimeMessage(t, runtime, bot.ID, "settlement race")
	entered, release := make(chan struct{}), make(chan struct{})
	sent.session.mu.Lock()
	sent.session.steerEntered, sent.session.steerRelease = entered, release
	sent.session.mu.Unlock()
	errs := make(chan error, 1)
	go func() {
		_, err := runtime.SteerQueuedMessage(context.Background(), bot.ID, queued.QueueID)
		errs <- err
	}()
	<-entered
	sent.session.complete("finished before ACK")
	waitRuntimeTurn(t, runtime, bot.ID, active.TurnID)
	close(release)
	if err := <-errs; !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "could not be confirmed") {
		t.Fatalf("settlement race was treated as accepted steer: %v", err)
	}
	sent.session.mu.Lock()
	cleared := false
	for _, method := range sent.session.rpcCalls {
		cleared = cleared || method == "clear_queue"
	}
	sent.session.mu.Unlock()
	if !cleared {
		t.Fatal("Pi's idle native steering queue could contaminate a later turn")
	}
	if queue := runtimeMessageQueue(t, runtime, bot.ID); len(queue.Messages) != 0 || !queue.Paused {
		t.Fatalf("settled Pi steer left an actionable product message: %+v", queue)
	}
	select {
	case next := <-factory.sends:
		t.Fatalf("uncertain Pi steer started a new turn: %q", next.prompt)
	default:
	}
}
