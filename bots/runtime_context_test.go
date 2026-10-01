package bots

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestRuntimeContextUsesOwnedLastUsageAndLeavesUnknownWindowUnknown(t *testing.T) {
	_, runtime, factory, bot := setupRuntime(t)
	if _, err := runtime.Goal(context.Background(), bot.ID, "get", nil); err != nil {
		t.Fatal(err)
	}
	factory.mu.Lock()
	session := factory.created[0].session
	factory.mu.Unlock()
	session.native("thread/tokenUsage/updated", map[string]any{"threadId": session.id, "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 120, "inputTokens": 100, "cachedInputTokens": 80, "outputTokens": 20}, "total": map[string]any{"totalTokens": 10000}, "modelContextWindow": 2000}})
	session.native("thread/tokenUsage/updated", map[string]any{"threadId": "another-thread", "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 999}, "modelContextWindow": 1}})
	status, err := runtime.Context(context.Background(), bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status["usedTokens"] != 120 || status["contextWindow"] != 2000 || status["remainingTokens"] != 1880 || status["percent"] != float64(6) {
		t.Fatalf("context used cumulative, foreign, or double-counted cached tokens: %v", status)
	}
	session.native("thread/tokenUsage/updated", map[string]any{"threadId": session.id, "tokenUsage": map[string]any{"last": map[string]any{"inputTokens": 90, "outputTokens": 10}, "modelContextWindow": nil}})
	status, err = runtime.Context(context.Background(), bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status["usedTokens"] != 100 || status["contextWindow"] != nil || status["percent"] != nil || status["remainingTokens"] != nil {
		t.Fatalf("unknown context window was invented: %v", status)
	}
	item := map[string]any{"id": "compact-item", "type": "contextCompaction"}
	session.native("item/started", map[string]any{"threadId": session.id, "item": item})
	status, err = runtime.Context(context.Background(), bot.ID)
	if err != nil || status["compacting"] != true || status["usedTokens"] != nil {
		t.Fatalf("native compaction not observed: %v %v", status, err)
	}
	session.native("item/completed", map[string]any{"threadId": session.id, "item": item})
	status, err = runtime.Context(context.Background(), bot.ID)
	if err != nil || status["compacting"] != false || status["usedTokens"] != nil {
		t.Fatalf("old usage survives compaction: %v %v", status, err)
	}
	select {
	case <-factory.sends:
		t.Fatal("context read sent inference")
	default:
	}
}

func TestRuntimeContextKeepsPostCompactionNativeEstimate(t *testing.T) {
	_, runtime, factory, bot := setupRuntime(t)
	if _, err := runtime.Goal(context.Background(), bot.ID, "get", nil); err != nil {
		t.Fatal(err)
	}
	factory.mu.Lock()
	session := factory.created[0].session
	factory.mu.Unlock()
	usage := func(tokens, input, output int) map[string]any {
		return map[string]any{"threadId": session.id, "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": tokens, "inputTokens": input, "cachedInputTokens": 0, "cacheWriteInputTokens": 0, "outputTokens": output, "reasoningOutputTokens": 0}, "modelContextWindow": 258400}}
	}
	session.native("thread/tokenUsage/updated", usage(100000, 99000, 1000))
	item := map[string]any{"id": "compact-item", "type": "contextCompaction"}
	session.native("item/started", map[string]any{"threadId": session.id, "turnId": "compact-turn", "item": item})
	// Installed Codex sends this recomputed current-history estimate before
	// item/completed; the zero input/output breakdown is not summary cost.
	session.native("thread/tokenUsage/updated", usage(6072, 0, 0))
	session.native("item/completed", map[string]any{"threadId": session.id, "turnId": "compact-turn", "item": item})
	status, err := runtime.Context(context.Background(), bot.ID)
	if err != nil || status["compacting"] != false || status["usedTokens"] != 6072 || status["remainingTokens"] != 252328 || status["estimated"] != true {
		t.Fatalf("post-compaction estimate discarded or shown as measured: %v %v", status, err)
	}
	session.native("thread/tokenUsage/updated", usage(6200, 6000, 200))
	status, err = runtime.Context(context.Background(), bot.ID)
	if err != nil || status["usedTokens"] != 6200 || status["estimated"] != nil {
		t.Fatalf("next provider measurement retained old estimate: %v %v", status, err)
	}
}

type contextStatsSession struct {
	*runtimeFakeSession
	afterCompaction atomic.Bool
}

func (s *contextStatsSession) RPC(ctx context.Context, method string, params any, result any) error {
	var data any
	switch method {
	case "get_state":
		data = map[string]any{"model": map[string]any{"contextWindow": 1000}, "isCompacting": false}
	case "get_session_stats":
		usage := map[string]any{"tokens": 200, "contextWindow": 1000, "percent": 20}
		if s.afterCompaction.Load() {
			usage["tokens"], usage["percent"] = nil, nil
		}
		data = map[string]any{"tokens": map[string]any{"total": 999999}, "contextUsage": usage}
	default:
		return s.runtimeFakeSession.RPC(ctx, method, params, result)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, result)
}

type contextStatsAgent struct {
	*runtimeFakeAgent
	session *contextStatsSession
}

func (a *contextStatsAgent) StartSession(ctx context.Context, resume string) (core.AgentSession, error) {
	if _, err := a.runtimeFakeAgent.StartSession(ctx, resume); err != nil {
		return nil, err
	}
	return a.session, nil
}

func TestRuntimePiContextUsesCurrentStatsAndNullAfterCompaction(t *testing.T) {
	store := testStore(t)
	bot, err := store.CreateBot(Bot{Name: "Pi", Backend: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	factory := &runtimeFakeFactory{sends: make(chan runtimeFakeSend, 8)}
	var session *contextStatsSession
	runtime := NewRuntime(store, RuntimeConfig{AgentOptions: runtimeFakeAgentOptions(), AgentFactory: func(backend string, opts map[string]any) (core.Agent, error) {
		agent, err := factory.create(backend, opts)
		if err != nil {
			return nil, err
		}
		fake := agent.(*runtimeFakeAgent)
		session = &contextStatsSession{runtimeFakeSession: fake.session}
		return &contextStatsAgent{runtimeFakeAgent: fake, session: session}, nil
	}})
	t.Cleanup(func() { _ = runtime.Close() })
	status, err := runtime.Context(context.Background(), bot.ID)
	if err != nil || status["usedTokens"] != 200 || status["remainingTokens"] != 800 || status["percent"] != float64(20) || status["estimated"] != true {
		t.Fatalf("Pi current context: %v %v", status, err)
	}
	session.afterCompaction.Store(true)
	status, err = runtime.Context(context.Background(), bot.ID)
	if err != nil || status["contextWindow"] != 1000 || status["usedTokens"] != nil || status["remainingTokens"] != nil || status["percent"] != nil {
		t.Fatalf("Pi null context replaced by cumulative stats: %v %v", status, err)
	}
	select {
	case <-factory.sends:
		t.Fatal("Pi context read sent inference")
	default:
	}
}

func TestRuntimeCompactWaitsNativeCompletionAndKeepsStableOperationID(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	raw, err := runtime.Compact(context.Background(), bot.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	requestID := raw.(map[string]any)["requestId"].(string)
	factory.mu.Lock()
	session := factory.created[0].session
	factory.mu.Unlock()
	waitRuntimeCondition(t, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		for _, method := range session.rpcCalls {
			if method == "thread/compact/start" {
				return true
			}
		}
		return false
	})
	if !runtime.Busy(bot.ID) {
		t.Fatal("queue acknowledgement released compact busy gate")
	}
	if _, err := runtime.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "should not be sent"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("send raced compaction: %v", err)
	}
	if _, err := runtime.Compact(context.Background(), bot.ID, ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("duplicate compact accepted: %v", err)
	}
	session.native("turn/started", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "compact-turn"}})
	state, _ := runtime.state(bot.ID)
	state.mu.Lock()
	chatTurn := state.current
	state.mu.Unlock()
	if chatTurn != nil {
		t.Fatal("compaction created an autonomous chat turn")
	}
	session.native("item/started", map[string]any{"threadId": session.id, "turnId": "compact-turn", "item": map[string]any{"id": "own-item", "type": "contextCompaction"}})
	session.native("turn/completed", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "older-chat-turn", "status": "failed"}})
	session.native("item/completed", map[string]any{"threadId": "another-thread", "item": map[string]any{"id": "own-item", "type": "contextCompaction"}})
	status, err := runtime.Context(context.Background(), bot.ID)
	if err != nil || status["compacting"] != true {
		t.Fatalf("foreign completion released spinner: %v %v", status, err)
	}
	session.native("item/completed", map[string]any{"threadId": session.id, "turnId": "compact-turn", "item": map[string]any{"id": "own-item", "type": "contextCompaction"}})
	session.native("turn/completed", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "unrelated-root-turn", "status": "completed"}})
	session.native("turn/completed", map[string]any{"threadId": "another-thread", "turn": map[string]any{"id": "compact-turn", "status": "completed"}})
	assertRuntimeCompactionStillBusy(t, runtime, bot.ID)
	if _, err := runtime.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "hooks are still running"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("send accepted before compact terminal success: %v", err)
	}
	session.native("turn/completed", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "compact-turn", "status": "completed"}})
	waitRuntimeCondition(t, func() bool { return !runtime.Busy(bot.ID) })
	events, err := store.Events(bot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	statuses := []string{}
	for _, event := range events {
		if event.Type == "compact_action" {
			var action map[string]any
			if err := json.Unmarshal(event.Data, &action); err != nil {
				t.Fatal(err)
			}
			if action["requestId"] != requestID || action["threadId"] != session.id {
				t.Fatalf("compact identity changed: %v", action)
			}
			statuses = append(statuses, action["status"].(string))
		}
	}
	if len(statuses) != 2 || statuses[0] != "started" || statuses[1] != "completed" {
		t.Fatalf("compact lifecycle: %v", statuses)
	}
	select {
	case <-factory.sends:
		t.Fatal("compact resent a user prompt")
	default:
	}
}

func TestRuntimeCompactNativeFailureKeepsOperationIdentity(t *testing.T) {
	for _, nativeStatus := range []string{"failed", "interrupted"} {
		t.Run(nativeStatus, func(t *testing.T) {
			store, runtime, factory, bot := setupRuntime(t)
			raw, err := runtime.Compact(context.Background(), bot.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			requestID := raw.(map[string]any)["requestId"]
			factory.mu.Lock()
			session := factory.created[0].session
			factory.mu.Unlock()
			session.native("turn/started", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "compact-turn"}})
			item := map[string]any{"id": "compact-item", "type": "contextCompaction"}
			session.native("item/started", map[string]any{"threadId": session.id, "turnId": "compact-turn", "item": item})
			session.native("item/completed", map[string]any{"threadId": session.id, "turnId": "compact-turn", "item": item})
			session.native("turn/completed", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "older-turn", "status": nativeStatus}})
			assertRuntimeCompactionStillBusy(t, runtime, bot.ID)
			session.native("turn/completed", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "compact-turn", "status": nativeStatus, "error": map[string]any{"message": "post-compaction hook stopped"}}})
			waitRuntimeCondition(t, func() bool { return !runtime.Busy(bot.ID) })
			events, err := store.Events(bot.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			var action map[string]any
			for _, event := range events {
				if event.Type == "compact_action" {
					if err := json.Unmarshal(event.Data, &action); err != nil {
						t.Fatal(err)
					}
				}
			}
			if action["requestId"] != requestID || action["status"] != "failed" || action["error"] != "compaction turn "+nativeStatus+": post-compaction hook stopped" {
				t.Fatalf("native compact failure: %v", action)
			}
		})
	}
}

func assertRuntimeCompactionStillBusy(t *testing.T, runtime *Runtime, botID string) {
	t.Helper()
	deadline := time.NewTimer(350 * time.Millisecond)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !runtime.Busy(botID) {
			t.Fatal("compaction ended before its owned terminal event")
		}
		select {
		case <-deadline.C:
			return
		case <-ticker.C:
		}
	}
}

type unsupportedCompactSession struct{ *runtimeFakeSession }

func (s *unsupportedCompactSession) RPC(ctx context.Context, method string, params any, result any) error {
	err := s.runtimeFakeSession.RPC(ctx, method, params, result)
	if method == "compact" {
		return errors.New("pi RPC compact: Unknown command: compact")
	}
	return err
}

type unsupportedCompactAgent struct {
	*runtimeFakeAgent
	session *unsupportedCompactSession
}

func (a *unsupportedCompactAgent) StartSession(ctx context.Context, resume string) (core.AgentSession, error) {
	if _, err := a.runtimeFakeAgent.StartSession(ctx, resume); err != nil {
		return nil, err
	}
	return a.session, nil
}

func TestRuntimePiCompactReportsUnsupportedCommandWithoutResending(t *testing.T) {
	store := testStore(t)
	bot, err := store.CreateBot(Bot{Name: "Pi", Backend: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	factory := &runtimeFakeFactory{sends: make(chan runtimeFakeSend, 8)}
	var session *unsupportedCompactSession
	runtime := NewRuntime(store, RuntimeConfig{AgentOptions: runtimeFakeAgentOptions(), AgentFactory: func(backend string, opts map[string]any) (core.Agent, error) {
		agent, err := factory.create(backend, opts)
		if err != nil {
			return nil, err
		}
		fake := agent.(*runtimeFakeAgent)
		session = &unsupportedCompactSession{runtimeFakeSession: fake.session}
		return &unsupportedCompactAgent{runtimeFakeAgent: fake, session: session}, nil
	}})
	t.Cleanup(func() { _ = runtime.Close() })
	raw, err := runtime.Compact(context.Background(), bot.ID, "keep the user's goal")
	if err != nil {
		t.Fatal(err)
	}
	requestID := raw.(map[string]any)["requestId"]
	waitRuntimeCondition(t, func() bool { return !runtime.Busy(bot.ID) })
	session.mu.Lock()
	var compactParams map[string]any
	for i, method := range session.rpcCalls {
		if method == "compact" {
			compactParams = session.rpcParams[i]
		}
	}
	session.mu.Unlock()
	if compactParams["customInstructions"] != "keep the user's goal" || compactParams["threadId"] != nil {
		t.Fatalf("wrong Pi compact parameters: %v", compactParams)
	}
	events, err := store.Events(bot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var action map[string]any
	for _, event := range events {
		if event.Type == "compact_action" {
			if err := json.Unmarshal(event.Data, &action); err != nil {
				t.Fatal(err)
			}
		}
	}
	if action["requestId"] != requestID || action["status"] != "failed" || action["error"] != "pi RPC compact: Unknown command: compact" {
		t.Fatalf("unsupported command falsely completed: %v", action)
	}
	select {
	case <-factory.sends:
		t.Fatal("unsupported compact retried using inference")
	default:
	}
}
