package bots

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func restartTestRuntime(t *testing.T, old *Runtime, store *Store, factory *runtimeFakeFactory) *Runtime {
	t.Helper()
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := NewRuntime(store, RuntimeConfig{AgentFactory: factory.create, AgentOptions: old.cfg.AgentOptions})
	t.Cleanup(func() { _ = restarted.Close() })
	return restarted
}

func completeRuntimePrompt(t *testing.T, r *Runtime, factory *runtimeFakeFactory, botID, prompt, answer string) runtimeFakeSend {
	t.Helper()
	turnID, err := r.SendMessage(context.Background(), botID, MessageRequest{Text: prompt})
	if err != nil {
		t.Fatal(err)
	}
	sent := nextRuntimeSend(t, factory)
	sent.session.complete(answer)
	if result := waitRuntimeTurn(t, r, botID, turnID); result.Status != "completed" {
		t.Fatalf("prompt result: %+v", result)
	}
	return sent
}

func TestRuntimePiCatalogRestartBeforeFirstPromptDoesNotResumePlannedSession(t *testing.T) {
	store, r, factory, bot := setupRuntime(t)
	factory.strictResume = true
	_, err := store.UpdateBot(bot.ID, func(bot *Bot) error {
		bot.Backend, bot.Model = "pi", "deepseek/deepseek-flash"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if caps, err := r.CapabilitiesForBot(context.Background(), bot.ID); err != nil || !caps.Backends["pi"].Available {
		t.Fatalf("Pi catalog: %+v %v", caps, err)
	}
	before, _ := store.GetBot(bot.ID)
	if before.Threads["pi"] != "" {
		t.Fatal("catalog persisted Pi's planned, nonexistent session file")
	}
	r = restartTestRuntime(t, r, store, factory)
	sent := completeRuntimePrompt(t, r, factory, bot.ID, "first actual prompt", "actual answer")
	if sent.session.agent.resume != "" {
		t.Fatalf("resumed nonexistent Pi session: %q", sent.session.agent.resume)
	}
	updated, _ := store.GetBot(bot.ID)
	if updated.Threads["pi"] != sent.session.id {
		t.Fatal("real Pi response did not persist its session")
	}
}

func TestRuntimeMissingOwnedPiSessionRecoversVisibleConversationButListingErrorDoesNot(t *testing.T) {
	store, r, factory, bot := setupRuntime(t)
	factory.strictResume = true
	_, _ = store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Backend = "pi"; return nil })
	first := completeRuntimePrompt(t, r, factory, bot.ID, "retain this question", "retain this answer")
	first.session.materialized.Store(false) // Native session file vanished.
	r = restartTestRuntime(t, r, store, factory)
	second := completeRuntimePrompt(t, r, factory, bot.ID, "continue", "continued")
	if second.session.agent.resume != "" || !strings.Contains(second.prompt, "retain this answer") {
		t.Fatalf("missing own Pi file did not receive explicit history: resume=%q prompt=%s", second.session.agent.resume, second.prompt)
	}
	r = restartTestRuntime(t, r, store, factory)
	factory.mu.Lock()
	factory.listErr = errors.New("session directory inaccessible")
	factory.mu.Unlock()
	turnID, err := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "do not silently reset"})
	if err != nil {
		t.Fatal(err)
	}
	result := waitRuntimeTurn(t, r, bot.ID, turnID)
	if result.Status != "error" || !strings.Contains(result.Error, "session directory inaccessible") {
		t.Fatalf("listing failure was hidden: %+v", result)
	}
	select {
	case prompt := <-factory.sends:
		t.Fatalf("sent inference after an arbitrary listing failure: %s", prompt.prompt)
	default:
	}
}

func TestRuntimeFreshConfigCatalogRestartDoesNotCommitUnmaterializedCodexThread(t *testing.T) {
	store, r, factory, bot := setupRuntime(t)
	factory.strictResume = true
	first := completeRuntimePrompt(t, r, factory, bot.ID, "original question", "original context")
	if err := r.WithIdleBot(bot.ID, func() error {
		_, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Role += " revised"; return nil })
		return err
	}); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.GetBot(bot.ID)
	state, _ := r.state(bot.ID)
	fresh, err := r.ensureSession(context.Background(), state, updated)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.CurrentSessionID() == first.session.id {
		t.Fatal("loaded Codex configuration was incorrectly resumed")
	}
	afterCatalog, _ := store.GetBot(bot.ID)
	if afterCatalog.Threads["codex"] != first.session.id {
		t.Fatal("catalog-only fresh thread replaced durable native history")
	}
	r = restartTestRuntime(t, r, store, factory)
	next := completeRuntimePrompt(t, r, factory, bot.ID, "first prompt after restart", "new result")
	if next.session.agent.resume != "" || !strings.Contains(next.prompt, "original context") {
		t.Fatalf("configuration restart lost context or resumed a ghost: resume=%s prompt=%s", next.session.agent.resume, next.prompt)
	}
}

func TestRuntimePausedGoalCarryKeepsRemainingBudgetAndPendingContextAcrossRestart(t *testing.T) {
	store, r, factory, bot := setupRuntime(t)
	factory.strictResume = true
	first := completeRuntimePrompt(t, r, factory, bot.ID, "remember the red project", "the project is red")
	if _, err := r.Goal(context.Background(), bot.ID, "set", map[string]any{"objective": "Finish red project", "status": "active", "tokenBudget": 10000}); err != nil {
		t.Fatal(err)
	}
	first.session.mu.Lock()
	first.session.goal["tokensUsed"] = 3000
	first.session.mu.Unlock()
	if err := r.WithIdleBot(bot.ID, func() error {
		_, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Role += " changed"; return nil })
		return err
	}); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.GetBot(bot.ID)
	state, _ := r.state(bot.ID)
	freshSession, err := r.ensureSession(context.Background(), state, updated)
	if err != nil {
		t.Fatal(err)
	}
	fresh := freshSession.(*runtimeFakeSession)
	fresh.mu.Lock()
	status, remaining := fresh.goal["status"], integerValue(fresh.goal["tokenBudget"])
	fresh.mu.Unlock()
	if status != "paused" || remaining != 7000 {
		t.Fatalf("goal carry status=%v remaining=%d", status, remaining)
	}
	materialized, _ := store.GetBot(bot.ID)
	if materialized.Threads["codex"] != fresh.id {
		t.Fatal("successful paused goal did not materialize fresh native thread")
	}
	// The native goal has persisted this new thread, but the conversation has
	// not been sent there yet. Its separate pending marker must survive this.
	r = restartTestRuntime(t, r, store, factory)
	next := completeRuntimePrompt(t, r, factory, bot.ID, "continue red project", "red project continued")
	if next.session.agent.resume != fresh.id || !strings.Contains(next.prompt, "the project is red") {
		t.Fatalf("goal-first materialization lost pending context: resume=%s prompt=%s", next.session.agent.resume, next.prompt)
	}
	events, _ := store.Events(bot.ID, 0)
	var applied bool
	for _, event := range events {
		if event.Type == "handoff_applied" {
			applied = true
		}
	}
	if !applied {
		t.Fatal("accepted handoff did not close its durable pending marker")
	}
}

func TestRuntimeHandoffRejectedSendRetainsContextForRetry(t *testing.T) {
	store, r, factory, bot := setupRuntime(t)
	completeRuntimePrompt(t, r, factory, bot.ID, "previous question", "previous useful answer")
	if err := r.WithIdleBot(bot.ID, func() error {
		_, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Backend = "pi"; return nil })
		return err
	}); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.GetBot(bot.ID)
	state, _ := r.state(bot.ID)
	session, err := r.ensureSession(context.Background(), state, updated)
	if err != nil {
		t.Fatal(err)
	}
	fake := session.(*runtimeFakeSession)
	fake.mu.Lock()
	fake.sendErr = errors.New("transient prompt rejection")
	fake.mu.Unlock()
	turnID, _ := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "first attempt"})
	first := nextRuntimeSend(t, factory)
	if result := waitRuntimeTurn(t, r, bot.ID, turnID); result.Status != "error" {
		t.Fatalf("rejected send: %+v", result)
	}
	fake.mu.Lock()
	fake.sendErr = nil
	fake.mu.Unlock()
	second := completeRuntimePrompt(t, r, factory, bot.ID, "retry", "done")
	if !strings.Contains(first.prompt, "previous useful answer") || !strings.Contains(second.prompt, "previous useful answer") {
		t.Fatalf("rejected handoff was prematurely cleared: first=%s retry=%s", first.prompt, second.prompt)
	}
}

func TestRuntimeExhaustedGoalCarryIsReceiptWithoutBudgetResetOrRepeatedRPC(t *testing.T) {
	store, r, factory, bot := setupRuntime(t)
	sent := completeRuntimePrompt(t, r, factory, bot.ID, "work", "finished allocation")
	sent.session.mu.Lock()
	sent.session.goal = map[string]any{"objective": "Unfinished work", "status": "budgetLimited", "tokenBudget": 1000, "tokensUsed": 1200}
	sent.session.mu.Unlock()
	if err := r.WithIdleBot(bot.ID, func() error {
		_, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Role += " refreshed"; return nil })
		return err
	}); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.GetBot(bot.ID)
	state, _ := r.state(bot.ID)
	if _, err := r.ensureSession(context.Background(), state, updated); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	pending, suppressed := state.pendingGoal, state.goalCarrySuppressed
	state.mu.Unlock()
	if pending != nil || !suppressed {
		t.Fatal("exhausted goal remained a pending, unbounded retry")
	}
	events, _ := store.Events(bot.ID, 0)
	var receipt bool
	for _, event := range events {
		if event.Type != "goal_carry" {
			continue
		}
		var carry struct {
			Status string
			Fields map[string]any
		}
		_ = json.Unmarshal(event.Data, &carry)
		if carry.Status == "exhausted" && carry.Fields["objective"] == "Unfinished work" && integerValue(carry.Fields["tokenBudget"]) == 0 {
			receipt = true
		}
	}
	if !receipt {
		t.Fatal("exhausted goal objective/budget not retained in journal")
	}
	completeRuntimePrompt(t, r, factory, bot.ID, "new bounded task", "done")
	state.mu.Lock()
	fresh := state.session.(*runtimeFakeSession)
	state.mu.Unlock()
	fresh.mu.Lock()
	defer fresh.mu.Unlock()
	for _, method := range fresh.rpcCalls {
		if method == "thread/goal/set" {
			t.Fatal("zero budget goal was reset or repeatedly restored")
		}
	}
}

func TestRuntimeNativeFailedAndInterruptedTurnsSettleWithAuthoritativeStatus(t *testing.T) {
	for _, nativeStatus := range []string{"failed", "interrupted"} {
		t.Run(nativeStatus, func(t *testing.T) {
			_, r, factory, bot := setupRuntime(t)
			turnID, _ := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "work"})
			sent := nextRuntimeSend(t, factory)
			sent.session.native("turn/started", map[string]any{"threadId": sent.session.id, "turn": map[string]any{"id": "terminal"}})
			terminal := map[string]any{"id": "terminal", "status": nativeStatus}
			if nativeStatus == "failed" {
				terminal["error"] = map[string]any{"message": "provider failed"}
			}
			sent.session.native("turn/completed", map[string]any{"threadId": sent.session.id, "turn": terminal})
			if nativeStatus == "failed" {
				sent.session.emit(core.Event{Type: core.EventError, Error: errors.New("provider failed"), Metadata: map[string]any{"turnId": "terminal"}})
			}
			sent.session.emit(core.Event{Type: core.EventResult, Done: true, Metadata: map[string]any{"turnId": "terminal"}})
			result := waitRuntimeTurn(t, r, bot.ID, turnID)
			want := nativeStatus
			if nativeStatus == "failed" {
				want = "error"
			}
			if result.Status != want || r.Busy(bot.ID) {
				t.Fatalf("terminal result %+v busy=%v", result, r.Busy(bot.ID))
			}
		})
	}
}

func TestRuntimeExplicitGoalResumeTransfersPendingContextBeforeActivatingAfterRestart(t *testing.T) {
	store, r, factory, bot := setupRuntime(t)
	factory.strictResume = true
	completeRuntimePrompt(t, r, factory, bot.ID, "retain the project fact", "the project owner chose red")
	if _, err := r.Goal(context.Background(), bot.ID, "set", map[string]any{"objective": "Continue the project", "status": "paused", "tokenBudget": 1000}); err != nil {
		t.Fatal(err)
	}
	if err := r.WithIdleBot(bot.ID, func() error {
		_, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Role += " refreshed"; return nil })
		return err
	}); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.GetBot(bot.ID)
	state, _ := r.state(bot.ID)
	if _, err := r.ensureSession(context.Background(), state, updated); err != nil {
		t.Fatal(err)
	}
	r = restartTestRuntime(t, r, store, factory)
	resumed := make(chan error, 2)
	resume := func() {
		_, err := r.Goal(context.Background(), bot.ID, "set", map[string]any{"status": "active"})
		resumed <- err
	}
	go resume()
	ack := nextRuntimeSend(t, factory)
	if !strings.Contains(ack.prompt, "the project owner chose red") || !strings.Contains(ack.prompt, "Do not use tools") {
		t.Fatalf("goal activated without bounded context handoff: %s", ack.prompt)
	}
	ack.session.mu.Lock()
	statusBeforeAck := ack.session.goal["status"]
	ack.session.mu.Unlock()
	if statusBeforeAck != "paused" {
		t.Fatalf("goal ran before handoff completed: %v", statusBeforeAck)
	}
	// A second Resume waits for this same service acknowledgment instead of
	// either sending a duplicate or activating the goal while it is unfinished.
	go resume()
	ack.session.complete("Контекст принят.")
	for range 2 {
		select {
		case err := <-resumed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("goal resume did not complete after handoff")
		}
	}
	ack.session.mu.Lock()
	statusAfterAck := ack.session.goal["status"]
	ack.session.mu.Unlock()
	if statusAfterAck != "active" {
		t.Fatalf("goal did not resume: %v", statusAfterAck)
	}
	select {
	case sent := <-factory.sends:
		t.Fatalf("Resume duplicated the handoff: %s", sent.prompt)
	default:
	}
	events, _ := store.Events(bot.ID, 0)
	var serviceSource bool
	for _, event := range events {
		if event.Type != "message" {
			continue
		}
		var message struct{ Source string }
		_ = json.Unmarshal(event.Data, &message)
		if message.Source == "goal_context" {
			serviceSource = true
		}
	}
	if !serviceSource {
		t.Fatal("context acknowledgment lacks durable service provenance")
	}
}
